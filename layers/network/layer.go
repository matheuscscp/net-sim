package network

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/matheuscscp/net-sim/layers/common"
	"github.com/matheuscscp/net-sim/layers/link"
	pkgcontext "github.com/matheuscscp/net-sim/pkg/context"
	pkgio "github.com/matheuscscp/net-sim/pkg/io"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
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
	// consume by registering protocol handlers (see the IPProtocol
	// interface).
	Layer interface {
		Send(ctx context.Context, datagram *gplayers.IPv4) error
		FindInterfaceForHeader(datagramHeader *gplayers.IPv4) (Interface, error)
		ForwardingMode() bool
		ForwardingTable() *ForwardingTable
		Interfaces() []Interface
		Interface(name string) Interface
		RegisterProtocol(protocol IPProtocol)
		DeregisterProtocol(protocolID gplayers.IPProtocol) bool
		GetRegisteredProtocol(protocolID gplayers.IPProtocol) (IPProtocol, bool)
		Close() error
		StackName() string
	}

	// LayerConfig contains the configs for the
	// concrete implementation of Layer.
	LayerConfig struct {
		// ForwardingMode keeps inbound datagrams with wrong dst IP address.
		ForwardingMode bool `yaml:"forwardingMode"`
		MetricLabels   struct {
			StackName string `yaml:"stackName"`
		} `yaml:"metricLabels"`

		Interfaces            []InterfaceConfig `yaml:"interfaces"`
		DefaultRouteInterface string            `yaml:"defaultRouteInterface"`
	}

	// IPProtocol represents a protocol that uses the Internet Protocol for
	// its transport, e.g. TCP and UDP.
	IPProtocol interface {
		GetID() gplayers.IPProtocol
		// Recv is the method for a registered IPProtocol to consume datagrams
		// that were received by the network layer. For performance reasons,
		// it will be invoked from multiple threads, hence the implementation
		// must be thread-safe.
		Recv(datagram *gplayers.IPv4)
	}

	layer struct {
		cancelCtx       context.CancelFunc
		wg              sync.WaitGroup
		conf            *LayerConfig
		intfs           []Interface
		intfMap         map[string]Interface
		intfIPMap       map[gopacket.Endpoint]Interface
		forwardingTable *ForwardingTable
		protocols       map[gplayers.IPProtocol]IPProtocol
		protocolsMu     sync.RWMutex
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
	intfIPMap := make(map[gopacket.Endpoint]Interface)
	addIntf := func(intf Interface) {
		intfs = append(intfs, intf)
		intfMap[intf.Name()] = intf
		intfIPMap[intf.IPAddress()] = intf
	}
	checkIntfOverlapping := func(intf Interface) error {
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
		if intfConf.MetricLabels.StackName == "" {
			intfConf.MetricLabels.StackName = conf.MetricLabels.StackName
		}
		intf, err := NewInterface(ctx, intfConf)
		if err != nil || checkIntfOverlapping(intf) != nil {
			for j := i - 1; 0 <= j; j-- {
				intfs[j].Close()
			}
			if err != nil {
				return nil, fmt.Errorf("error creating network interface %d: %w", i, err)
			}
			return nil, checkIntfOverlapping(intf)
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
	l := &layer{
		cancelCtx:       cancel,
		conf:            &conf,
		intfs:           intfs,
		intfMap:         intfMap,
		intfIPMap:       intfIPMap,
		forwardingTable: forwardingTable,
		protocols:       map[gplayers.IPProtocol]IPProtocol{},
	}
	l.listen(ctx)
	return l, nil
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
	intf, ok := l.intfIPMap[srcIPAddress]
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
		return nil, fmt.Errorf("the network interface %s, chosen by indexing the forwarding table with the dst IP address %s, does not exist", intfName, dstIPAddress)
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

// listen starts one thread for each interface to consume the intf.Recv() channel
// and either forward the datagrams in case the layer is running in the forwarding
// mode, or invoke registered IPProtocol handlers matching the datagram when the
// dst IP address matches the IP address of the receiving interface (or the subnet
// broadcast IP address).
func (l *layer) listen(ctx context.Context) {
	ctxDone := ctx.Done()
	for _, intf := range l.intfs {
		// make local copy so the i-th thread captures
		// a reference only to its own intf
		intf := intf

		// start intf thread
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			for {
				select {
				case <-ctxDone:
					return
				case datagram := <-intf.Recv():
					// deliver to protocol
					dstIPAddress := gplayers.NewIPEndpoint(datagram.DstIP)
					if dstIPAddress == intf.IPAddress() || dstIPAddress == intf.BroadcastIPAddress() {
						if protocol, ok := l.GetRegisteredProtocol(datagram.Protocol); ok {
							protocol.Recv(datagram)
						}
						continue
					}

					// if the dst IP address does not match the IP address or the broadcast
					// IP address of any interface, then the interfaces are necessarily
					// running in forwarding mode. forward the datagram
					dstIntf, err := l.findInterfaceForDstIPAddress(datagram.DstIP)
					if err == nil {
						if sendErr := dstIntf.Send(ctx, datagram); sendErr != nil {
							if pkgcontext.IsContextError(ctx, sendErr) {
								// network layer was closed, error can be ignored safely
								continue
							}
							err = fmt.Errorf("error sending datagram through dst interface: %w", sendErr)
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

func (l *layer) RegisterProtocol(protocol IPProtocol) {
	l.protocolsMu.Lock()
	l.protocols[protocol.GetID()] = protocol
	l.protocolsMu.Unlock()
}

func (l *layer) DeregisterProtocol(protocolID gplayers.IPProtocol) bool {
	l.protocolsMu.Lock()
	_, ok := l.protocols[protocolID]
	delete(l.protocols, protocolID)
	l.protocolsMu.Unlock()
	return ok
}

func (l *layer) GetRegisteredProtocol(protocolID gplayers.IPProtocol) (IPProtocol, bool) {
	l.protocolsMu.RLock()
	protocol, ok := l.protocols[protocolID]
	l.protocolsMu.RUnlock()
	return protocol, ok
}

func (l *layer) Close() error {
	// cancel ctx and wait threads
	var cancel context.CancelFunc
	cancel, l.cancelCtx = l.cancelCtx, nil
	if cancel == nil {
		return nil
	}
	cancel()
	l.wg.Wait()

	// close interfaces
	closers := make([]io.Closer, len(l.intfs))
	for i, intf := range l.intfs {
		closers[i] = intf
	}
	return pkgio.Close(closers...)
}

func (l *layer) StackName() string {
	stackName := l.conf.MetricLabels.StackName
	if stackName == "" {
		stackName = "default"
	}
	return stackName
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
		return fmt.Errorf("(*loopbackIntf).send(ctx) done while pushing ip datagram to input buffer: %w", ctx.Err())
	case <-l.ctx.Done():
		return fmt.Errorf("(*loopbackIntf).ctx done while pushing ip datagram to input buffer: %w", l.ctx.Err())
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
