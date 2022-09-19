package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/matheuscscp/net-sim/layers/common"
	"github.com/matheuscscp/net-sim/layers/link"
	pkgcontext "github.com/matheuscscp/net-sim/pkg/context"
	pkgnet "github.com/matheuscscp/net-sim/pkg/net"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/sirupsen/logrus"
)

type (
	// Interface represents a hypothetical network interface, composed
	// by an ethernet card, an IP address, a gateway IP address and a
	// network CIDR block.
	//
	// When sending a datagram out the interface sets the src IP address
	// of the datagram to its own IP address, unless if running on
	// "forwarding mode".
	//
	// Inbound datagrams with dst IP address not matching the interface's
	// IP address will be discarded, unless if running on "forwarding mode"
	// or if the dst IP address is the broadcast IP address of the network.
	//
	// The interface uses ARP queries to resolve dst IP addresses inside
	// the network. When the dst IP address is outside the network, an ARP
	// query is sent to resolve the MAC address of the gateway instead.
	Interface interface {
		Send(ctx context.Context, datagram *gplayers.IPv4) error
		// SendTransportSegment is a hack for the TCP/UDP checksums to be
		// computed by gopacket during serialization.
		SendTransportSegment(
			ctx context.Context,
			datagramHeader *gplayers.IPv4,
			segment gopacket.TransportLayer,
		) error
		Recv() <-chan *gplayers.IPv4
		Close() error
		ForwardingMode() bool
		Name() string
		IPAddress() gopacket.Endpoint
		Gateway() gopacket.Endpoint
		Network() *net.IPNet
		BroadcastIPAddress() gopacket.Endpoint
		Card() link.EthernetPort
	}

	// InterfaceConfig contains the configs for the
	// concrete implementation of Interface.
	InterfaceConfig struct {
		// ForwardingMode keeps inbound datagrams with wrong dst IP address.
		ForwardingMode bool   `yaml:"forwardingMode"`
		Name           string `yaml:"name"`
		IPAddress      string `yaml:"ipAddress"`
		Gateway        string `yaml:"gateway"`
		NetworkCIDR    string `yaml:"networkCIDR"`

		Card link.EthernetPortConfig `yaml:"ethernetPort"`
	}

	interfaceImpl struct {
		ctx         context.Context
		cancelCtx   context.CancelFunc
		conf        *InterfaceConfig
		l           logrus.FieldLogger
		ipAddress   gopacket.Endpoint
		gateway     gopacket.Endpoint
		network     *net.IPNet
		broadcast   gopacket.Endpoint
		card        link.EthernetPort
		in          chan *gplayers.IPv4
		arpTable    ARPTable
		arpEvents   *sync.Cond
		arpEventsMu sync.Mutex
		wg          sync.WaitGroup
	}
)

var (
	errPayloadTooLarge = fmt.Errorf("payload is larger than network layer MTU (%d)", MTU)
)

// NewInterface creates an Interface from config.
func NewInterface(ctx context.Context, conf InterfaceConfig) (Interface, error) {
	if conf.Name == "" {
		return nil, errors.New("interface name cannot be empty")
	}
	ipAddress := net.ParseIP(conf.IPAddress)
	if ipAddress == nil { // net.ParseID() does not return an error
		return nil, fmt.Errorf("unknown error parsing ip address '%s'", conf.IPAddress)
	}
	gateway := net.ParseIP(conf.Gateway)
	if gateway == nil {
		return nil, fmt.Errorf("unknown error parsing gateway '%s'", conf.Gateway)
	}
	network, err := pkgnet.ParseNetworkCIDR(conf.NetworkCIDR)
	if err != nil {
		return nil, fmt.Errorf("error parsing network cidr: %w", err)
	}
	if !network.Contains(ipAddress) {
		return nil, errors.New("the ip address does not match the network cidr")
	}
	if !network.Contains(gateway) {
		return nil, errors.New("the gateway ip address does not match the network cidr")
	}
	card, err := link.NewEthernetPort(ctx, conf.Card)
	if err != nil {
		return nil, fmt.Errorf("error creating card: %w", err)
	}
	intfCtx, cancel := context.WithCancel(context.Background())
	intf := &interfaceImpl{
		ctx:       intfCtx,
		cancelCtx: cancel,
		conf:      &conf,
		l:         logrus.WithField("interface_ip_address", conf.IPAddress),
		ipAddress: gplayers.NewIPEndpoint(ipAddress),
		gateway:   gplayers.NewIPEndpoint(gateway),
		network:   network,
		broadcast: gplayers.NewIPEndpoint(pkgnet.BroadcastIPAddress(network)),
		card:      card,
		in:        make(chan *gplayers.IPv4, channelSize),
	}
	intf.arpEvents = sync.NewCond(&intf.arpEventsMu)
	intf.startThreads()
	return intf, nil
}

func (i *interfaceImpl) startThreads() {
	// recv
	ctxDone := i.ctx.Done()
	i.wg.Add(1)
	go func() {
		defer i.wg.Done()
		for {
			select {
			case <-ctxDone:
				return
			case frame := <-i.card.Recv():
				i.decapAndRecv(frame)
			}
		}
	}()
}

func (i *interfaceImpl) Send(ctx context.Context, datagram *gplayers.IPv4) error {
	if len(datagram.Payload) == 0 {
		return common.ErrCannotSendEmpty
	}
	i.setDatagramHeaderFields(datagram)
	datagramBuf, err := SerializeDatagram(datagram)
	if err != nil {
		return err
	}
	return i.send(ctx, datagram, datagramBuf)
}

func (i *interfaceImpl) SendTransportSegment(
	ctx context.Context,
	datagramHeader *gplayers.IPv4,
	segment gopacket.TransportLayer,
) error {
	i.setDatagramHeaderFields(datagramHeader)
	datagramBuf, err := SerializeDatagramWithTransportSegment(datagramHeader, segment)
	if err != nil {
		return err
	}
	return i.send(ctx, datagramHeader, datagramBuf)
}

func (i *interfaceImpl) setDatagramHeaderFields(datagramHeader *gplayers.IPv4) {
	if !i.ForwardingMode() {
		datagramHeader.SrcIP = i.ipAddress.Raw()
	}
}

func (i *interfaceImpl) send(
	ctx context.Context,
	datagramHeader *gplayers.IPv4,
	datagramBuf []byte,
) error {
	if len(datagramBuf)-HeaderLength > MTU {
		return errPayloadTooLarge
	}
	ctx, cancel := pkgcontext.WithCancelOnAnotherContext(ctx, i.ctx)
	defer cancel()

	// calculate ARP target IP address
	dstIPAddress := gplayers.NewIPEndpoint(datagramHeader.DstIP)
	arpDstIPAddress := dstIPAddress
	if !i.network.Contains(dstIPAddress.Raw()) {
		arpDstIPAddress = i.gateway
	}

	// find ARP target MAC address
	dstMACAddress := link.BroadcastMACEndpoint()
	if arpDstIPAddress != i.broadcast {
		var hasL2Endpoint bool
		refresh := func() { dstMACAddress, hasL2Endpoint = i.arpTable.FindRoute(arpDstIPAddress) }
		refresh()
		if !hasL2Endpoint {
			// send arp request
			err := i.sendARP(ctx, &gplayers.ARP{
				Operation:      gplayers.ARPRequest,
				DstProtAddress: arpDstIPAddress.Raw(),
				DstHwAddress:   link.BroadcastMACAddress(),
			})
			if err != nil {
				return fmt.Errorf("error sending arp request: %w", err)
			}

			// wait for arp reply
			i.arpEventsMu.Lock()
			refresh()
			for ctx.Err() == nil && !hasL2Endpoint {
				i.arpEvents.Wait()
				refresh()
			}
			i.arpEventsMu.Unlock()

			if !hasL2Endpoint {
				return ctx.Err()
			}
		}
	}

	// send
	err := i.card.Send(ctx, &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: datagramBuf,
		},
		DstMAC:       dstMACAddress.Raw(),
		EthernetType: gplayers.EthernetTypeIPv4,
	})
	if err != nil {
		return fmt.Errorf("error sending ip datagram: %w", err)
	}

	return nil
}

func (i *interfaceImpl) sendARP(ctx context.Context, arp *gplayers.ARP) error {
	// fill default fields
	arp.AddrType = gplayers.LinkTypeEthernet
	arp.Protocol = gplayers.EthernetTypeIPv4
	arp.HwAddressSize = 6
	arp.ProtAddressSize = 4
	arp.SourceHwAddress = i.card.MACAddress().Raw()
	arp.SourceProtAddress = i.ipAddress.Raw()

	// serialize
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{}
	if err := gopacket.SerializeLayers(buf, opts, arp); err != nil {
		return fmt.Errorf("error serializing arp layer: %w", err)
	}

	// send
	err := i.card.Send(ctx, &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: buf.Bytes(),
		},
		DstMAC:       arp.DstHwAddress,
		EthernetType: gplayers.EthernetTypeARP,
	})
	if err != nil {
		return err
	}

	return nil
}

func (i *interfaceImpl) Recv() <-chan *gplayers.IPv4 {
	return i.in
}

func (i *interfaceImpl) decapAndRecv(frame *gplayers.Ethernet) {
	l := i.l.
		WithField("frame", frame)

	var datagram *gplayers.IPv4
	err := func() error {
		switch frame.EthernetType {
		case gplayers.EthernetTypeARP:
			return i.decapARPAndReply(frame)
		case gplayers.EthernetTypeIPv4:
			// deserialize datagram
			var err error
			datagram, err = DeserializeDatagram(frame.Payload)
			if err != nil {
				return err
			}

			// check discard
			dstIPAddress := gplayers.NewIPEndpoint(datagram.DstIP)
			if dstIPAddress != i.broadcast &&
				!i.ForwardingMode() &&
				i.ipAddress != dstIPAddress {
				datagram = nil
			}

			return nil
		}
		return nil
	}()

	if err != nil {
		l.
			WithError(err).
			Error("error decapsulating network layer")
		return
	}

	if datagram != nil {
		i.recv(datagram)
	}
}

func (i *interfaceImpl) recv(datagram *gplayers.IPv4) {
	select {
	case <-i.ctx.Done():
	case i.in <- datagram:
	}
}

func (i *interfaceImpl) decapARPAndReply(frame *gplayers.Ethernet) error {
	// deserialize arp
	pkt := gopacket.NewPacket(frame.Payload, gplayers.LayerTypeARP, gopacket.Lazy)
	arp := pkt.Layer(gplayers.LayerTypeARP).(*gplayers.ARP)
	if arp == nil || pkt.ErrorLayer() != nil {
		return fmt.Errorf("error deserializing arp packet: %w", pkt.ErrorLayer().Error())
	}

	// cache arp mapping and notify
	i.arpTable.StoreRoute(arp.SourceProtAddress, arp.SourceHwAddress)
	i.notifyARPEvent()

	// reply arp request if necessary
	arpDstIPAddress := gplayers.NewIPEndpoint(arp.DstProtAddress)
	if arp.Operation == gplayers.ARPRequest && i.ipAddress == arpDstIPAddress {
		err := i.sendARP(i.ctx, &gplayers.ARP{
			Operation:      gplayers.ARPReply,
			DstHwAddress:   arp.SourceHwAddress,
			DstProtAddress: arp.SourceProtAddress,
		})
		if err != nil {
			return fmt.Errorf("error sending arp reply: %w", err)
		}
	}

	return nil
}

func (i *interfaceImpl) notifyARPEvent() {
	i.arpEventsMu.Lock()
	i.arpEvents.Broadcast()
	i.arpEventsMu.Unlock()
}

func (i *interfaceImpl) Close() error {
	// cancel ctx and wait threads
	var cancel context.CancelFunc
	cancel, i.cancelCtx = i.cancelCtx, nil
	if cancel == nil {
		return nil
	}
	cancel()
	i.wg.Wait()

	// unblock send()
	i.notifyARPEvent()

	// close channels
	close(i.in)

	return i.card.Close()
}

func (i *interfaceImpl) ForwardingMode() bool {
	return i.conf.ForwardingMode
}

func (i *interfaceImpl) Name() string {
	return i.conf.Name
}

func (i *interfaceImpl) IPAddress() gopacket.Endpoint {
	return i.ipAddress
}

func (i *interfaceImpl) Gateway() gopacket.Endpoint {
	return i.gateway
}

func (i *interfaceImpl) Network() *net.IPNet {
	return i.network
}

func (i *interfaceImpl) BroadcastIPAddress() gopacket.Endpoint {
	return i.broadcast
}

func (i *interfaceImpl) Card() link.EthernetPort {
	return i.card
}
