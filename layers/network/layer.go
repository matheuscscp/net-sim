package network

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/matheuscscp/net-sim/layers/link"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/hashicorp/go-multierror"
	"github.com/sirupsen/logrus"
)

type (
	// Layer represents the network layer of a device. It provides
	// the means to send and receive IP datagrams to and from the
	// IP network, composed by a forwarding table and a set of
	// network interfaces. When sending a datagram out, the layer
	// indexes the forwarding table to choose the interface.
	// A consumer of IP datagrams may consume by accessing the
	// Recv() channel of the interfaces.
	//
	// Inbound datagrams with dst IP address not matching the interface's
	// IP address will be discarded, unless if running on "forwarding mode".
	Layer interface {
		Send(ctx context.Context, datagram *gplayers.IPv4) error
		ForwardingMode() bool
		ForwardingTable() *ForwardingTable
		Interfaces() []Interface
		Interface(name string) Interface
		// Listen blocks listening for datagrams in all interfaces
		// and forwards them to the given listener function.
		//
		// The listener will be invoked from multiple threads, so
		// the underlying code must be thread-safe (this is a
		// performance optimization decision).
		//
		// The Recv() channels of the interfaces should not be drained
		// elsewhere while Listen()ing, otherwise the listener function
		// will miss those datagrams.
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
		conf            *LayerConfig
		intfs           []Interface
		intfMap         map[string]Interface
		forwardingTable *ForwardingTable
	}

	loopbackIntf struct {
		buf    chan *gplayers.IPv4
		closed bool
	}
)

// NewLayer creates Layer from config.
func NewLayer(ctx context.Context, conf LayerConfig) (Layer, error) {
	// prepare for creating interfaces
	intfs := make([]Interface, 0, len(conf.Interfaces))
	intfMap := make(map[string]Interface)
	addIntf := func(intf Interface) {
		intfs = append(intfs, intf)
		intfMap[intf.Name()] = intf
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
	lo := &loopbackIntf{buf: make(chan *gplayers.IPv4, MaxQueueSize)}
	addIntf(lo)

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

	return &layer{
		conf:            &conf,
		intfs:           intfs,
		intfMap:         intfMap,
		forwardingTable: forwardingTable,
	}, nil
}

func (l *layer) Send(ctx context.Context, datagram *gplayers.IPv4) error {
	intfName, ok := l.forwardingTable.FindRoute(datagram.DstIP)
	if !ok {
		return nil // no route, discarding
	}
	intf, ok := l.intfMap[intfName]
	if !ok {
		return fmt.Errorf("target interface %s does not exist", intfName)
	}
	return intf.Send(ctx, datagram)
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
					if err := l.Send(ctx, datagram); err != nil {
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
	var err error
	for _, intf := range l.intfs {
		if cErr := intf.Close(); cErr != nil {
			err = multierror.Append(err, fmt.Errorf("error closing interface %s: %w", intf.Name(), cErr))
		}
	}
	return err
}

func (l *loopbackIntf) Send(ctx context.Context, datagram *gplayers.IPv4) error {
	// validate and set fields
	if err := ValidateDatagramAndSetDefaultFields(datagram); err != nil {
		return err
	}
	loIPAddr := LoopbackIPAddress()
	datagram.SrcIP = loIPAddr
	datagram.DstIP = loIPAddr

	// serialize and deserialize datagram
	buf, err := SerializeDatagram(datagram)
	if err != nil {
		return err
	}
	datagram, err = DeserializeDatagram(buf)
	if err != nil {
		return err
	}

	l.buf <- datagram
	return nil
}

func (l *loopbackIntf) Recv() <-chan *gplayers.IPv4 {
	return l.buf
}

func (l *loopbackIntf) Close() error {
	if !l.closed {
		close(l.buf)
		l.closed = true
	}
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
