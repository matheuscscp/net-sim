package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/matheuscscp/net-sim/layers/common"
	"github.com/matheuscscp/net-sim/layers/link"
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
		conf      *InterfaceConfig
		l         logrus.FieldLogger
		ipAddress gopacket.Endpoint
		gateway   gopacket.Endpoint
		network   *net.IPNet
		broadcast gopacket.Endpoint
		card      link.EthernetPort
		out       chan *outDatagram // delayed datagrams waiting for ARP
		in        chan *gplayers.IPv4
		arpEvents chan *gopacket.Endpoint
		arpTable  ARPTable
		cancelCtx context.CancelFunc
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
	errNoL2Route       = errors.New("no L2 route")
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
	intf := &interfaceImpl{
		conf:      &conf,
		l:         logrus.WithField("interface_ip_address", conf.IPAddress),
		ipAddress: gplayers.NewIPEndpoint(ipAddress),
		gateway:   gplayers.NewIPEndpoint(gateway),
		network:   network,
		broadcast: gplayers.NewIPEndpoint(pkgnet.BroadcastIPAddress(network)),
		card:      card,
		out:       make(chan *outDatagram, channelSize),
		in:        make(chan *gplayers.IPv4, channelSize),
		arpEvents: make(chan *gopacket.Endpoint, channelSize),
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
	if len(datagram.Payload) == 0 {
		return common.ErrCannotSendEmpty
	}
	i.setDatagramHeaderFields(datagram)
	buf, err := SerializeDatagram(datagram)
	if err != nil {
		return err
	}
	return i.sendOrDelay(ctx, datagram, buf)
}

func (i *interfaceImpl) SendTransportSegment(
	ctx context.Context,
	datagramHeader *gplayers.IPv4,
	segment gopacket.TransportLayer,
) error {
	i.setDatagramHeaderFields(datagramHeader)
	buf, err := SerializeDatagramWithTransportSegment(datagramHeader, segment)
	if err != nil {
		return err
	}
	return i.sendOrDelay(ctx, datagramHeader, buf)
}

func (i *interfaceImpl) setDatagramHeaderFields(datagramHeader *gplayers.IPv4) {
	if !i.ForwardingMode() {
		datagramHeader.SrcIP = i.ipAddress.Raw()
	}
}

func (i *interfaceImpl) sendOrDelay(
	ctx context.Context,
	datagramHeader *gplayers.IPv4,
	buf []byte,
) error {
	if len(buf)-HeaderLength > MTU {
		return errPayloadTooLarge
	}

	// calculate L2 endpoint
	dstIPAddress := gplayers.NewIPEndpoint(datagramHeader.DstIP)
	arpDstIPAddress := dstIPAddress
	if !i.network.Contains(dstIPAddress.Raw()) {
		arpDstIPAddress = i.gateway
	}

	// send
	outDatagram := &outDatagram{ctx, buf, dstIPAddress, arpDstIPAddress}
	err := i.send(outDatagram)
	if err == nil {
		return nil
	}
	if !errors.Is(err, errNoL2Route) {
		return err
	}

	// no dstMACAddress cached for dstIPAddress, send arp request
	i.out <- outDatagram // enqueue delayed transmission
	err = i.sendARP(ctx, &gplayers.ARP{
		Operation:      gplayers.ARPRequest,
		DstProtAddress: arpDstIPAddress.Raw(),
		DstHwAddress:   link.BroadcastMACAddress(),
	})
	if err != nil {
		return fmt.Errorf("error sending arp request: %w", err)
	}

	return nil
}

func (i *interfaceImpl) send(datagram *outDatagram) error {
	// find L2 route
	dstMACAddress := link.BroadcastMACEndpoint()
	if datagram.arpDstIPAddress != i.broadcast {
		var hasL2Route bool
		dstMACAddress, hasL2Route = i.arpTable.FindRoute(datagram.arpDstIPAddress)
		if !hasL2Route {
			return errNoL2Route
		}
	}

	// send
	err := i.card.Send(datagram.ctx, &gplayers.Ethernet{
		BaseLayer: gplayers.BaseLayer{
			Payload: datagram.buf,
		},
		DstMAC:       dstMACAddress.Raw(),
		EthernetType: gplayers.EthernetTypeIPv4,
	})
	if err != nil {
		return fmt.Errorf("error sending ip datagram: %w", err)
	}

	return nil
}

func (i *interfaceImpl) sendOrLogError(datagram *outDatagram) {
	if err := i.send(datagram); err != nil {
		i.l.
			WithError(err).
			WithField("dst_ip_address", datagram.dstIPAddress.String()).
			WithField("arp_dst_ip_address", datagram.arpDstIPAddress.String()).
			WithField("datagram_buf", datagram.buf).
			Error("error sending arp-delayed ip datagram")
	}
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
	// we index the ARP dst IP address to store the *outDatagram
	// in a list. this is so we can transmit batches of waiting
	// datagrams right away when the relevant ARP reply arrives
	// and updates the ARP table with the required ARP dst MAC
	// address
	type arpDstIPAddressDescriptor struct {
		pendingDatagrams []*outDatagram
		deadline         time.Time
	}
	pendingDatagramsByARPDstIPAddress := make(map[gopacket.Endpoint]*arpDstIPAddressDescriptor)
	index := func(datagram *outDatagram) {
		a, ok := pendingDatagramsByARPDstIPAddress[datagram.arpDstIPAddress]
		if !ok {
			a = &arpDstIPAddressDescriptor{}
		}
		a.pendingDatagrams = append(a.pendingDatagrams, datagram)
		a.deadline = time.Now().Add(100 * time.Millisecond)
		pendingDatagramsByARPDstIPAddress[datagram.arpDstIPAddress] = a
	}

	// the queue stores an ARP dst IP address whenever a datagram
	// is delayed because the required ARP entry is missing
	type queueElem struct {
		arpDstIPAddress *gopacket.Endpoint
		next            *queueElem
	}
	var queueFront, queueBack *queueElem
	push := func(arpDstIPAddress *gopacket.Endpoint) {
		elem := &queueElem{arpDstIPAddress: arpDstIPAddress}
		if queueBack == nil {
			queueFront = elem
			queueBack = elem
		} else {
			queueBack.next = elem
			queueBack = elem
		}
	}
	pop := func() *gopacket.Endpoint {
		popped := queueFront
		if popped == nil {
			return nil
		}

		// pop front
		queueFront = queueFront.next
		if queueFront == nil {
			queueBack = nil
		}
		popped.next = nil

		return popped.arpDstIPAddress
	}

	// the timer is used to wake up the thread whenever there are pending
	// datagram batches
	timer, stopTimer := pkgtime.NewTimer(0)
	defer stopTimer()
	resetTimer := func() {
		stopTimer()

		for ; queueFront != nil; pop() {
			arpDstIPAddress := queueFront.arpDstIPAddress
			a, ok := pendingDatagramsByARPDstIPAddress[*arpDstIPAddress]
			if ok {
				timer.Reset(time.Until(a.deadline))
				return
			}
		}
	}

	flush := func(arpDstIPAddress *gopacket.Endpoint) {
		a, ok := pendingDatagramsByARPDstIPAddress[*arpDstIPAddress]
		if ok {
			for _, datagram := range a.pendingDatagrams {
				i.sendOrLogError(datagram)
			}
		}
		delete(pendingDatagramsByARPDstIPAddress, *arpDstIPAddress)
	}

	ctxDone := ctx.Done()
	for {
		select {
		case <-ctxDone:
			return
		case datagram := <-i.out:
			index(datagram)
			push(&datagram.arpDstIPAddress)
			resetTimer()
		case arpDstIPAddress := <-i.arpEvents:
			flush(arpDstIPAddress)
			resetTimer()
		case <-timer.C:
			for arpDstIPAddress := pop(); arpDstIPAddress != nil; arpDstIPAddress = pop() {
				a, ok := pendingDatagramsByARPDstIPAddress[*arpDstIPAddress]
				if ok && a.deadline.Before(time.Now()) {
					flush(arpDstIPAddress)
					break
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
		WithField("frame", frame)

	var datagram *gplayers.IPv4
	err := func() error {
		switch frame.EthernetType {
		case gplayers.EthernetTypeARP:
			return i.decapARP(ctx, frame)
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
		i.in <- datagram
	}
}

func (i *interfaceImpl) decapARP(ctx context.Context, frame *gplayers.Ethernet) error {
	// deserialize arp
	pkt := gopacket.NewPacket(frame.Payload, gplayers.LayerTypeARP, gopacket.Lazy)
	arp := pkt.Layer(gplayers.LayerTypeARP).(*gplayers.ARP)
	if arp == nil || len(arp.Payload) == 0 {
		return fmt.Errorf("error deserializing arp packet: %w", pkt.ErrorLayer().Error())
	}

	// cache mapping and notify
	i.arpTable.StoreRoute(arp.SourceProtAddress, arp.SourceHwAddress)
	arpEvent := gplayers.NewIPEndpoint(arp.SourceProtAddress)
	i.arpEvents <- &arpEvent

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
	close(i.arpEvents)
	for range i.arpEvents {
	}

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
