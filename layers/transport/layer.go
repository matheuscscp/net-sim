package transport

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/matheuscscp/net-sim/layers/network"
	pkgio "github.com/matheuscscp/net-sim/pkg/io"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

type (
	// Layer represents the transport layer of a device,
	// where the TCP and UDP protocols are implemented.
	// Its responsibility is to route application data
	// to/from the IP network interfaces through these
	// two protocols.
	Layer interface {
		// Listen binds the given IPv4:port address on the
		// given network ("tcp" or "udp") and returns a
		// handler for accepting connections into that port.
		// The port is said to be on the "listen" mode, which
		// allows it to receive incoming connections. The
		// IPv4 part of the address is optional, and if present
		// should match the IP address of one of the underlying
		// network interfaces. This means that the port will
		// only accept incoming connections targeted to this
		// IP address. If the port zero is used, a random
		// port will be chosen.
		Listen(ctx context.Context, network, address string) (net.Listener, error)

		// Dial starts a connection with the given IPv4:port address
		// on the given network ("tcp" or "udp") and returns a handler
		// for reading and writing data from/into this connection.
		// The local port is chosen at random and will not accept
		// any incoming connections. For UDP, creating a connection
		// means just binding the dst IPv4:port address to the
		// connection handler (there's no handshake). DNS resolution
		// is not support yet.
		Dial(ctx context.Context, network, address string) (net.Conn, error)

		// Close makes the transport layer stop listening to
		// IP datagrams received and provided by the network
		// layer. It does not close the network layer, it only
		// causes the underlying call to network.Layer.Listen()
		// to return, releasing the Recv() channels of the
		// underlying network interfaces to be taken over by
		// something else, like a new instance of the transport
		// layer.
		Close() error
	}

	layer struct {
		networkLayer network.Layer
		tcp          *listenerSet
		udp          *listenerSet
		cancelCtx    context.CancelFunc
		wg           sync.WaitGroup
		giantBuf     chan *gplayers.IPv4
	}
)

// NewLayer creates the concrete implementation of Layer and
// calls networkLayer.Listen() on a thread. Use Close() to
// stop and wait for this thread.
func NewLayer(networkLayer network.Layer) Layer {
	ctx, cancel := context.WithCancel(context.Background())

	l := &layer{
		networkLayer: networkLayer,
		cancelCtx:    cancel,
		giantBuf:     make(chan *gplayers.IPv4, demuxThreads*channelSize),
	}
	l.tcp = newListenerSet(l, tcp{})
	l.udp = newListenerSet(l, udp{})

	// create thread for listening to IP datagrams and push them
	// into the giant buffer
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		networkLayer.Listen(ctx, func(datagram *gplayers.IPv4) {
			select {
			case l.giantBuf <- datagram:
			default:
			}
		})
	}()

	// create threads for draining the giant datagram buffer and demux
	// each datagram into the right place (some port/conn buffer, or
	// drop the datagram)
	ctxDone := ctx.Done()
	for i := 0; i < demuxThreads; i++ {
		l.wg.Add(1)
		go func() {
			defer l.wg.Done()
			for {
				select {
				case <-ctxDone:
					return
				case datagram := <-l.giantBuf:
					switch datagram.Protocol {
					case gplayers.IPProtocolTCP:
						l.tcp.decapAndDemux(datagram)
					case gplayers.IPProtocolUDP:
						l.udp.decapAndDemux(datagram)
					}
				}
			}
		}()
	}

	return l
}

func (l *layer) Listen(ctx context.Context, network, address string) (net.Listener, error) {
	if network == TCP {
		return l.tcp.listen(ctx, address)
	}
	if network == UDP {
		return l.udp.listen(ctx, address)
	}
	return nil, ErrInvalidNetwork
}

func (l *layer) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	if network == TCP {
		return l.tcp.dial(ctx, address)
	}
	if network == UDP {
		return l.udp.dial(ctx, address)
	}
	return nil, ErrInvalidNetwork
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

	// close channels
	close(l.giantBuf)

	return pkgio.Close(l.tcp, l.udp)
}

func (l *layer) send(
	ctx context.Context,
	datagramHeader *gplayers.IPv4,
	segment gopacket.TransportLayer,
) error {
	intf, err := l.networkLayer.FindInterfaceForHeader(datagramHeader)
	if err != nil {
		return fmt.Errorf("error finding interface for datagram header: %w", err)
	}
	if err := intf.SendTransportSegment(ctx, datagramHeader, segment); err != nil {
		return fmt.Errorf("error sending transport segment: %w", err)
	}
	return nil
}
