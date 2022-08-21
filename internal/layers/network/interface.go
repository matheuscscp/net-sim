package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/matheuscscp/net-sim/internal/layers/common"
	"github.com/matheuscscp/net-sim/internal/layers/link"
	pkgnet "github.com/matheuscscp/net-sim/pkg/net"
	pkgtime "github.com/matheuscscp/net-sim/pkg/time"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/sirupsen/logrus"
)

type (
	// Interface represents a hypothetical network interface, composed
	// by an ethernet card, an IP address, a gateway IP address and a
	// network CIDR block.
	//
	// The interface uses ARP queries to resolve dst IP addresses inside
	// the network. When the IP address is outside the network, an ARP
	// query is sent to resolve the MAC address of the gateway instead.
	//
	// Inbound datagrams with dst IP address not matching the interface's
	// IP address will be discarded, unless if running on "forwarding mode".
	Interface interface {
		Send(ctx context.Context, datagram *gplayers.IPv4) error
		Recv() <-chan *gplayers.IPv4
		Close() error
		ForwardingMode() bool
		IPAddress() gopacket.Endpoint
	}

	// InterfaceConfig contains the configs for the
	// concrete implementation of Interface.
	InterfaceConfig struct {
		// ForwardingMode keeps inbound datagrams with wrong dst address.
		ForwardingMode bool   `yaml:"forwardingMode"`
		IPAddress      string `yaml:"ipAddress"`
		Gateway        string `yaml:"gateway"`
		NetworkCIDR    string `yaml:"networkCIDR"`

		Card *link.EthernetPortConfig `yaml:"ethernetPort"`
	}

	interfaceImpl struct {
		conf      *InterfaceConfig
		l         logrus.FieldLogger
		ipAddress gopacket.Endpoint
		gateway   gopacket.Endpoint
		network   *net.IPNet
		broadcast gopacket.Endpoint
		out       chan *outDatagram // delayed datagrams waiting for ARP
		in        chan *gplayers.IPv4
		arpTable  ARPTable
		card      link.EthernetPort
		cancelCtx func()
		wg        sync.WaitGroup
	}

	outDatagram struct {
		ctx             context.Context
		buf             []byte
		dstIPAddress    gopacket.Endpoint
		arpDstIPAddress gopacket.Endpoint // dstIPAddress or gateway
	}
)

var (
	errNoL2Route = errors.New("no L2 route")
)

// NewInterface creates an Interface from config.
// If conf.Card is nil, then a card must be passed.
func NewInterface(
	ctx context.Context,
	conf InterfaceConfig,
	card ...link.EthernetPort,
) (Interface, error) {
	if len(card) > 0 && conf.Card != nil {
		return nil, errors.New("specify one of card or conf.Card, not both")
	}
	if len(card) == 0 && conf.Card == nil {
		return nil, errors.New("specify one of card or conf.Card")
	}
	if len(card) > 1 {
		return nil, errors.New("can only handle one card")
	}
	if len(card) == 1 && card[0] == nil {
		return nil, errors.New("nil card")
	}
	ipAddress := net.ParseIP(conf.IPAddress)
	if ipAddress == nil { // net.ParseID() does not return an error
		return nil, errors.New("unknown error parsing ip address")
	}
	gateway := net.ParseIP(conf.Gateway)
	if gateway == nil {
		return nil, errors.New("unknown error parsing gateway")
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
	intf := &interfaceImpl{
		conf:      &conf,
		l:         logrus.WithField("interface_ip_address", conf.IPAddress),
		ipAddress: gplayers.NewIPEndpoint(ipAddress),
		gateway:   gplayers.NewIPEndpoint(gateway),
		network:   network,
		broadcast: gplayers.NewIPEndpoint(pkgnet.Broadcast(network)),
		out:       make(chan *outDatagram, MaxQueueSize),
		in:        make(chan *gplayers.IPv4, MaxQueueSize),
		arpTable:  NewARPTable(),
	}
	if len(card) == 1 {
		intf.card = card[0]
	} else if intf.card, err = link.NewEthernetPort(ctx, *conf.Card); err != nil {
		return nil, fmt.Errorf("error creating card: %w", err)
	}
	intf.startThreads()
	return intf, nil
}

func (i *interfaceImpl) startThreads() {
	var ctx context.Context
	ctx, i.cancelCtx = context.WithCancel(context.Background())
	ctxDone := ctx.Done()

	// send delayed datagrams waiting for arp
	i.wg.Add(1)
	go func() {
		defer i.wg.Done()
		i.sendDelayedDatagramsWaitingforARP(ctx)
	}()

	// recv
	i.wg.Add(1)
	go func() {
		defer i.wg.Done()
		for {
			select {
			case <-ctxDone:
				return
			case frame := <-i.card.Recv():
				i.decap(ctx, frame)
			}
		}
	}()
}

func (i *interfaceImpl) Send(ctx context.Context, datagram *gplayers.IPv4) error {
	// validate payload size
	if len(datagram.Payload) == 0 {
		return common.ErrCannotSendEmpty
	}
	if len(datagram.Payload) > MTU {
		return fmt.Errorf("payload is larger than network layer MTU (%d)", MTU)
	}

	// serialize datagram
	datagram.Version = 4
	datagram.IHL = 5                                     // header length in 4-byte words
	datagram.Length = uint16(len(datagram.Payload)) + 20 // header length in bytes
	if !i.ForwardingMode() {
		datagram.SrcIP = i.ipAddress.Raw()
	}
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{}
	payload := gopacket.Payload(datagram.Payload)
	if err := gopacket.SerializeLayers(buf, opts, datagram, payload); err != nil {
		return fmt.Errorf("error serializing network layer: %w", err)
	}

	// calculate L2 endpoint
	dstIPAddress := gplayers.NewIPEndpoint(datagram.DstIP)
	arpDstIPAddress := dstIPAddress
	if !i.network.Contains(dstIPAddress.Raw()) {
		arpDstIPAddress = i.gateway
	}

	// send
	outDatagram := &outDatagram{ctx, buf.Bytes(), dstIPAddress, arpDstIPAddress}
	err := i.send(outDatagram)
	if err == nil {
		return nil
	}
	if !errors.Is(err, errNoL2Route) {
		return err
	}

	// no dstMACAddress cached for dstIPAddress, send arp request
	err = i.sendARP(ctx, &gplayers.ARP{
		Operation:      gplayers.ARPRequest,
		DstProtAddress: arpDstIPAddress.Raw(),
		DstHwAddress:   link.BroadcastMACAddress,
	})
	if err != nil {
		return fmt.Errorf("error sending arp request: %w", err)
	}
	i.out <- outDatagram // enqueue delayed transmission

	return nil
}

func (i *interfaceImpl) send(o *outDatagram) error {
	dstMACAddress := link.BroadcastMACEndpoint
	if o.arpDstIPAddress != i.broadcast {
		var hasL2Route bool
		dstMACAddress, hasL2Route = i.arpTable.Load(o.arpDstIPAddress)
		if !hasL2Route {
			return errNoL2Route
		}
	}

	err := i.card.Send(o.ctx, &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: o.buf,
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

func (i *interfaceImpl) sendDelayedDatagramsWaitingforARP(ctx context.Context) {
	// the queue stores an outDatagram and a deadline, which is set
	// for a small amount of time in the future when the outDatagram
	// arrives in the thread's channel
	type queueElem struct {
		datagram *outDatagram
		deadline time.Time
	}
	var queue []*queueElem
	push := func(datagram *outDatagram) {
		queue = append(queue, &queueElem{
			datagram: datagram,
			deadline: time.Now().Add(100 * time.Millisecond),
		})
	}
	pop := func() (*outDatagram, bool) {
		if len(queue) == 0 {
			return nil, false
		}
		datagram := queue[0].datagram
		queue[0] = nil
		queue = queue[1:]
		if len(queue) == 0 {
			queue = nil
		}
		return datagram, true
	}

	// the timer is used to wake up the thread whenever is time to send
	// the next outDatagram (the head of the queue)
	timer, stopTimer := pkgtime.NewTimer(time.Hour)
	defer stopTimer()
	resetTimer := func() {
		stopTimer()
		d := time.Hour
		if len(queue) > 0 {
			d = time.Until(queue[0].deadline)
		}
		timer.Reset(d)
	}

	ctxDone := ctx.Done()
	for {
		select {
		case <-ctxDone:
			return
		case datagram := <-i.out:
			push(datagram)
			resetTimer()
		case <-timer.C:
			if datagram, ok := pop(); ok {
				// send first datagram in the queue. here we dont need
				// to merge the cancellation of the thread's ctx into
				// datagram.ctx because the cancellation of this thread's
				// ctx is tied with the cancellation of the underlying
				// port's thread ctx via Close(), and the latter is
				// already merged with the ctx arg passed to Send()
				// upon transmission
				if err := i.send(datagram); err != nil {
					i.l.
						WithError(err).
						WithField("dst_ip_address", datagram.dstIPAddress.String()).
						WithField("arp_dst_ip_address", datagram.arpDstIPAddress.String()).
						WithField("datagram_buf", datagram.buf).
						Error("error sending arp-delayed ip datagram")
				}
			}
			resetTimer()
		}
	}
}

func (i *interfaceImpl) Recv() <-chan *gplayers.IPv4 {
	return i.in
}

func (i *interfaceImpl) decap(ctx context.Context, frame *gplayers.Ethernet) {
	l := i.l.
		WithField("src_mac_address", frame.SrcMAC.String()).
		WithField("datagram_buf", frame.Payload)

	var datagram *gplayers.IPv4
	err := func() error {
		switch frame.EthernetType {
		case gplayers.EthernetTypeARP:
			return i.decapARP(ctx, frame)
		case gplayers.EthernetTypeIPv4:
			pkt := gopacket.NewPacket(frame.Payload, gplayers.LayerTypeIPv4, gopacket.Lazy)
			datagram = pkt.NetworkLayer().(*gplayers.IPv4)
			if datagram == nil || len(datagram.Payload) == 0 {
				return pkt.ErrorLayer().Error()
			}
			dstIPAddress := gplayers.NewIPEndpoint(datagram.DstIP)
			if dstIPAddress != i.broadcast &&
				!i.ForwardingMode() &&
				i.ipAddress != dstIPAddress {
				l.
					WithField("datagram", datagram).
					Info("discarding ip datagram due to unmatched dst ip address")
				datagram = nil
			}
			return nil
		default:
			l.Info("ethertype not implemented. dropping")
			return nil
		}
	}()

	if err != nil {
		l.
			WithError(err).
			Error("error decapsulating network layer")
		return
	}

	if datagram != nil {
		i.in <- datagram
	}
}

func (i *interfaceImpl) decapARP(ctx context.Context, frame *gplayers.Ethernet) error {
	pkt := gopacket.NewPacket(frame.Payload, gplayers.LayerTypeARP, gopacket.Lazy)
	arp := pkt.Layer(gplayers.LayerTypeARP).(*gplayers.ARP)
	if arp == nil || len(arp.Payload) == 0 {
		return pkt.ErrorLayer().Error()
	}

	// cache mapping
	i.arpTable.Store(arp.SourceProtAddress, arp.SourceHwAddress)

	// reply arp request
	arpDstIPAddress := gplayers.NewIPEndpoint(arp.DstProtAddress)
	if arp.Operation == gplayers.ARPRequest && i.ipAddress == arpDstIPAddress {
		err := i.sendARP(ctx, &gplayers.ARP{
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

func (i *interfaceImpl) Close() error {
	if i.cancelCtx == nil {
		return nil
	}

	// close threads
	i.cancelCtx()
	i.cancelCtx = nil
	i.wg.Wait()

	// close channels
	close(i.out)
	for range i.out {
	}
	close(i.in)

	return i.card.Close()
}

func (i *interfaceImpl) ForwardingMode() bool {
	return i.conf.ForwardingMode
}

func (i *interfaceImpl) IPAddress() gopacket.Endpoint {
	return i.ipAddress
}
