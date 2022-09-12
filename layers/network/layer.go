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

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/hashicorp/go-multierror"
	"github.com/sirupsen/logrus"
)

type (
	// Layer represents the network layer of a device, composed by a
	// forwarding table and a set of network interfaces. It provides
	// the means to send and receive IP datagrams to and from the IP
	// networks attached to the underlying interfaces.
	//
	// When sending a datagram out, the network layer indexes the
	// forwarding table with the dst IP address to choose the
	// interface. The datagram goes out with the IP address of the
	// interface as its src IP address, unless if running on
	// "forwarding mode" and the datagram came in from one of the
	// other interfaces. The src IP address of the datagram may be
	// specified in order to choose the network interface to go out
	// from. When doing so, the IP address must match the IP address
	// of one of the interfaces, otherwise an error will be returned.
	//
	// A consumer of IP datagrams (like the transport layer) may
	// consume by accessing the Recv() channel of the interfaces
	// directly, or via the Listen() method (which spwans one
	// thread for each interface/channel and finishes them when
	// the given ctx is cancelled), but not both simultaneously.
	// The Listen() method may be sequentially called multiple
	// times for any given Layer instance.
	Layer interface {
		Send(ctx context.Context, datagram *gplayers.IPv4) error
		FindInterfaceForHeader(datagramHeader *gplayers.IPv4) (Interface, error)
		ForwardingMode() bool
		ForwardingTable() *ForwardingTable
		Interfaces() []Interface
		Interface(name string) Interface
		// Listen blocks listening for datagrams in all interfaces
		// and forwards them to the given listener function. It
		// returns when the given ctx is cancelled.
		//
		// The listener will be invoked from multiple threads, so
		// the underlying code must be thread-safe (this is a
		// performance optimization decision).
		//
		// The Recv() channels of the interfaces should not be consumed
		// elsewhere while Listen()ing, otherwise the listener function
		// will miss those datagrams. For the same reason, Listen is not
		// thread-safe, but it may safely be called multiple times
		// sequentially.
		Listen(ctx context.Context, listener func(datagram *gplayers.IPv4))
		Close() error
	}

	// LayerConfig contains the configs for the
	// concrete implementation of Layer.
	LayerConfig struct {
		// ForwardingMode keeps inbound datagrams with wrong dst IP address.
		ForwardingMode        bool              `yaml:"forwardingMode"`
		Interfaces            []InterfaceConfig `yaml:"interfaces"`
		DefaultRouteInterface string            `yaml:"defaultRouteInterface"`
	}

	layer struct {
		ctx             context.Context
		cancelCtx       context.CancelFunc
		conf            *LayerConfig
		intfs           []Interface
		intfMap         map[string]Interface
		ipMap           map[gopacket.Endpoint]Interface
		forwardingTable *ForwardingTable
	}

	loopbackIntf struct {
		ctx       context.Context
		cancelCtx context.CancelFunc
		buf       chan *gplayers.IPv4
	}
)

var (
	errNoL3Route = errors.New("no L3 route")
)

// NewLayer creates Layer from config.
func NewLayer(ctx context.Context, conf LayerConfig) (Layer, error) {
	// prepare for creating interfaces
	intfs := make([]Interface, 0, len(conf.Interfaces))
	intfMap := make(map[string]Interface)
	ipMap := make(map[gopacket.Endpoint]Interface)
	addIntf := func(intf Interface) {
		intfs = append(intfs, intf)
		intfMap[intf.Name()] = intf
		ipMap[intf.IPAddress()] = intf
	}
	overlapIntf := func(intf Interface) error {
		intfNet := intf.Network()
		for _, overlap := range intfs {
			overlapNet := overlap.Network()
			if intfNet.Contains(overlapNet.IP) || overlapNet.Contains(intfNet.IP) {
				return fmt.Errorf("interface %s is configured with a network that overlaps the network of interface %s", intf.Name(), overlap.Name())
			}
		}
		return nil
	}

	// create loopback interface
	addIntf(newLoopbackIntf())

	// create configured interfaces
	for i, intfConf := range conf.Interfaces {
		intfConf.ForwardingMode = conf.ForwardingMode
		intfConf.Card.ForwardingMode = false
		intf, err := NewInterface(ctx, intfConf)
		if err != nil || overlapIntf(intf) != nil {
			for j := i - 1; 0 <= j; j-- {
				intfs[j].Close()
			}
			if err != nil {
				return nil, fmt.Errorf("error creating network interface %d: %w", i, err)
			}
			return nil, overlapIntf(intf)
		}
		addIntf(intf)
	}

	// assemble forwarding table
	forwardingTable := &ForwardingTable{}
	for _, intf := range intfs {
		forwardingTable.StoreRoute(intf.Network(), intf.Name())
	}
	if intf := conf.DefaultRouteInterface; len(intf) > 0 {
		if _, hasDefaultRouteInterface := intfMap[intf]; !hasDefaultRouteInterface {
			return nil, fmt.Errorf("target interface for default route does not exist: %s", intf)
		}
		forwardingTable.StoreRoute(Internet(), intf)
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &layer{
		ctx:             ctx,
		cancelCtx:       cancel,
		conf:            &conf,
		intfs:           intfs,
		intfMap:         intfMap,
		ipMap:           ipMap,
		forwardingTable: forwardingTable,
	}, nil
}

func (l *layer) Send(ctx context.Context, datagram *gplayers.IPv4) error {
	intf, err := l.FindInterfaceForHeader(datagram)
	if err != nil {
		return err
	}
	return intf.Send(ctx, datagram)
}

func (l *layer) FindInterfaceForHeader(datagramHeader *gplayers.IPv4) (Interface, error) {
	if datagramHeader.SrcIP == nil {
		return l.findInterfaceForDstIPAddress(datagramHeader.DstIP)
	}

	// find interface matching datagramHeader.SrcIP
	srcIPAddress := gplayers.NewIPEndpoint(datagramHeader.SrcIP)
	intf, ok := l.ipMap[srcIPAddress]
	if !ok {
		return nil, fmt.Errorf("specified src IP address does not match any of the network interfaces: %s", datagramHeader.SrcIP)
	}
	return intf, nil
}

func (l *layer) findInterfaceForDstIPAddress(dstIPAddress net.IP) (Interface, error) {
	intfName, ok := l.forwardingTable.FindRoute(dstIPAddress)
	if !ok {
		return nil, errNoL3Route
	}
	intf, ok := l.intfMap[intfName]
	if !ok {
		return nil, fmt.Errorf("the interface %s chosen by indexing the forwarding table with the dst IP address %s does not exist", intfName, dstIPAddress)
	}
	return intf, nil
}

func (l *layer) ForwardingMode() bool {
	return l.conf.ForwardingMode
}

func (l *layer) ForwardingTable() *ForwardingTable {
	return l.forwardingTable
}

func (l *layer) Interfaces() []Interface {
	intfs := make([]Interface, len(l.intfs))
	copy(intfs, l.intfs)
	return intfs
}

func (l *layer) Interface(name string) Interface {
	return l.intfMap[name]
}

func (l *layer) Listen(ctx context.Context, listener func(datagram *gplayers.IPv4)) {
	ctx, cancel := pkgcontext.WithCancelOnAnotherContext(ctx, l.ctx)
	defer cancel()

	var wg sync.WaitGroup
	defer wg.Wait()

	ctxDone := ctx.Done()
	for _, intf := range l.intfs {
		// make local copy so the i-th thread captures
		// a reference only to its own intf
		intf := intf

		// start intf thread
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctxDone:
					return
				case datagram := <-intf.Recv():
					// deliver to transport layer
					dstIPAddress := gplayers.NewIPEndpoint(datagram.DstIP)
					if dstIPAddress == intf.IPAddress() || dstIPAddress == intf.BroadcastIPAddress() {
						listener(datagram)
						continue
					}

					// if the dst IP address does not match the IP address or the broadcast
					// IP address of any interface, then the interfaces are necessarily
					// running in forwarding mode. forward the datagram
					dstIntf, err := l.findInterfaceForDstIPAddress(datagram.DstIP)
					if err == nil {
						err = dstIntf.Send(ctx, datagram)
						if err == nil {
							continue
						}
					}
					if err != nil && !errors.Is(err, errNoL3Route) {
						logrus.
							WithError(err).
							WithField("from_interface", intf.Name()).
							WithField("datagram", datagram).
							Error("error forwarding datagram")
					}
				}
			}
		}()
	}
}

func (l *layer) Close() error {
	// cancel ctx
	var cancel context.CancelFunc
	cancel, l.cancelCtx = l.cancelCtx, nil
	if cancel == nil {
		return nil
	}
	cancel()

	// close interfaces
	var err error
	for _, intf := range l.intfs {
		if cErr := intf.Close(); cErr != nil {
			err = multierror.Append(err, fmt.Errorf("error closing interface %s: %w", intf.Name(), cErr))
		}
	}
	return err
}

func newLoopbackIntf() Interface {
	ctx, cancel := context.WithCancel(context.Background())
	return &loopbackIntf{ctx, cancel, make(chan *gplayers.IPv4, channelSize)}
}

func (l *loopbackIntf) Send(ctx context.Context, datagram *gplayers.IPv4) error {
	if len(datagram.Payload) == 0 {
		return common.ErrCannotSendEmpty
	}
	l.setDatagramHeaderFields(datagram)
	buf, err := SerializeDatagram(datagram)
	if err != nil {
		return err
	}
	return l.send(ctx, buf)
}

func (l *loopbackIntf) SendTransportSegment(
	ctx context.Context,
	datagramHeader *gplayers.IPv4,
	segment gopacket.TransportLayer,
) error {
	l.setDatagramHeaderFields(datagramHeader)
	buf, err := SerializeDatagramWithTransportSegment(datagramHeader, segment)
	if err != nil {
		return err
	}
	return l.send(ctx, buf)
}

func (l *loopbackIntf) setDatagramHeaderFields(datagramHeader *gplayers.IPv4) {
	loIPAddr := LoopbackIPAddress()
	datagramHeader.SrcIP = loIPAddr
	datagramHeader.DstIP = loIPAddr
}

func (l *loopbackIntf) send(ctx context.Context, datagramBuf []byte) error {
	if len(datagramBuf)-HeaderLength > MTU {
		return errPayloadTooLarge
	}

	datagram, err := DeserializeDatagram(datagramBuf)
	if err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.ctx.Done():
		return l.ctx.Err()
	case l.buf <- datagram:
	}
	return nil
}

func (l *loopbackIntf) Recv() <-chan *gplayers.IPv4 {
	return l.buf
}

func (l *loopbackIntf) Close() error {
	// cancel ctx
	var cancel context.CancelFunc
	cancel, l.cancelCtx = l.cancelCtx, nil
	if cancel == nil {
		return nil
	}
	cancel()

	// close channels
	close(l.buf)

	return nil
}

func (l *loopbackIntf) ForwardingMode() bool {
	return false
}

func (l *loopbackIntf) Name() string {
	return "lo"
}

func (l *loopbackIntf) IPAddress() gopacket.Endpoint {
	return LoopbackIPEndpoint()
}

func (l *loopbackIntf) Gateway() gopacket.Endpoint {
	return gopacket.Endpoint{}
}

func (l *loopbackIntf) Network() *net.IPNet {
	_, net, _ := net.ParseCIDR(LoopbackIPAddress().String() + "/32")
	return net
}

func (l *loopbackIntf) BroadcastIPAddress() gopacket.Endpoint {
	return LoopbackIPEndpoint()
}

func (l *loopbackIntf) Card() link.EthernetPort {
	return nil
}
