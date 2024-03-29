package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/matheuscscp/net-sim/layers/network"
	pkgio "github.com/matheuscscp/net-sim/pkg/io"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/sirupsen/logrus"
)

type (
	// Layer represents the transport layer of a device,
	// where the TCP and UDP protocols are implemented.
	// Its responsibility is to route application data
	// to/from the IP network interfaces through these
	// two protocols.
	Layer interface {
		// Listen binds the given IPv4:port localAddr on the
		// given network ("tcp" or "udp") and returns a
		// handler for accepting connections into that port.
		// The port is said to be on the "listen" mode, which
		// allows it to receive incoming connections.
		//
		// The IPv4 part of the address is optional, and if present
		// should match the IP address of one of the underlying
		// network interfaces. This means that the port will
		// only accept incoming connections targeted to this
		// IP address.
		//
		// If the port "0" is used, a random
		// port will be chosen.
		Listen(ctx context.Context, network, localAddr string) (net.Listener, error)

		// Dial starts a connection with the given IPv4:port remoteAddr
		// on the given network ("tcp" or "udp") and returns a handler
		// for reading and writing data from/into this connection.
		// The local port is chosen at random and will not accept
		// any incoming connections.
		//
		// For UDP, creating a connection
		// means just binding the dst IPv4:port address to the
		// connection handler (there's no handshake).
		//
		// DNS resolution is not support yet.
		Dial(ctx context.Context, network, remoteAddr string) (net.Conn, error)
		Dialer() LayerDialer

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

	// LayerDialer allows for specifying the localAddr when
	// dialing. The network ("tcp" or "udp") where the dialing
	// will take place is fetched from the localAddr specified
	// via WithLocalAddr().
	LayerDialer interface {
		Dialer
		WithLocalAddr(localAddr net.Addr) LayerDialer
	}

	layer struct {
		networkLayer network.Layer
		tcp          *protocol
		udp          *protocol
		cancelCtx    context.CancelFunc
		wg           sync.WaitGroup
		giantBuf     chan *gplayers.IPv4
	}

	layerDialer struct {
		*layer
		localAddr net.Addr
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
	l.tcp = newProtocol(l, tcpFactory{})
	l.udp = newProtocol(l, udpFactory{})

	// register protocol handlers
	networkLayer.RegisterIPProtocol(l.tcp)
	networkLayer.RegisterIPProtocol(l.udp)

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
					var err error
					switch datagram.Protocol {
					case gplayers.IPProtocolTCP:
						if err = l.tcp.decapAndDemux(datagram); err != nil {
							// prepare helpers for replying unmatched segments
							replyUnmatchedSegment := func(unmatched, reply *gplayers.TCP) error {
								datagramHeader := &gplayers.IPv4{
									DstIP:    datagram.SrcIP,
									SrcIP:    datagram.DstIP,
									Protocol: gplayers.IPProtocolTCP,
								}
								reply.DstPort = unmatched.SrcPort
								reply.SrcPort = unmatched.DstPort
								return l.send(ctx, datagramHeader, reply)
							}
							replyAckForUnmatchedFinSegment := func(unmatchedFin *gplayers.TCP) error {
								err := replyUnmatchedSegment(unmatchedFin, &gplayers.TCP{
									ACK: true,
									Ack: unmatchedFin.Seq + 1,
								})
								if err != nil {
									return fmt.Errorf("error replying ack for unmatched tcp fin segment: %w", err)
								}
								return nil
							}
							replyAckrstForUnmatchedSegment := func(unmatchedFin *gplayers.TCP) error {
								err := replyUnmatchedSegment(unmatchedFin, &gplayers.TCP{
									ACK: true,
									RST: true,
								})
								if err != nil {
									return fmt.Errorf("error replying ackrst for unmatched tcp segment: %w", err)
								}
								return nil
							}

							// check listener not found
							var listenerNotFoundErr *listenerNotFoundError
							if errors.As(err, &listenerNotFoundErr) {
								switch unmatchedSegment := listenerNotFoundErr.segment.(*gplayers.TCP); {
								case unmatchedSegment.RST:
									// no-op
									continue
								case unmatchedSegment.FIN:
									if err = replyAckForUnmatchedFinSegment(unmatchedSegment); err == nil {
										continue
									}
								default:
									if err = replyAckrstForUnmatchedSegment(unmatchedSegment); err == nil {
										continue
									}
								}
							}

							// check conn not found
							var connNotFoundErr *connNotFoundError
							if errors.As(err, &connNotFoundErr) {
								switch unmatchedSegment := connNotFoundErr.segment.(*gplayers.TCP); {
								case unmatchedSegment.FIN:
									if err = replyAckForUnmatchedFinSegment(unmatchedSegment); err == nil {
										continue
									}
								// print a debug log if the conn was not found
								default:
									err = nil
									logrus.
										WithField("segment", unmatchedSegment).
										Debug("conn not found for tcp segment")
								}
							}
						}
					case gplayers.IPProtocolUDP:
						if err = l.udp.decapAndDemux(datagram); err != nil {
							// print a debug log if listener or conn were not found
							var unmatchedSegment *gplayers.UDP
							if listenerNotFoundErr := (*listenerNotFoundError)(nil); errors.As(err, &listenerNotFoundErr) {
								unmatchedSegment = listenerNotFoundErr.segment.(*gplayers.UDP)
							} else if connNotFoundErr := (*connNotFoundError)(nil); errors.As(err, &connNotFoundErr) {
								unmatchedSegment = connNotFoundErr.segment.(*gplayers.UDP)
							}
							if unmatchedSegment != nil {
								err = nil
								logrus.
									WithField("segment", unmatchedSegment).
									Debug("conn not found for udp segment")
							}
						}
					}
					if err != nil {
						logrus.
							WithError(err).
							WithField("datagram", datagram).
							Error("error in transport.(*protocol).decapAndDemux()")
					}
				}
			}
		}()
	}

	return l
}

func (l *layer) recv(datagram *gplayers.IPv4) {
	select {
	case l.giantBuf <- datagram:
	default:
	}
}

func (l *layer) Listen(ctx context.Context, network, localAddr string) (net.Listener, error) {
	if network == TCP {
		return l.tcp.listen(ctx, localAddr)
	}
	if network == UDP {
		return l.udp.listen(ctx, localAddr)
	}
	return nil, ErrInvalidNetwork
}

func (l *layer) Dial(ctx context.Context, network, remoteAddr string) (net.Conn, error) {
	const localAddrForRandomPort = ":0"
	return l.dial(ctx, network, localAddrForRandomPort, remoteAddr)
}

func (l *layer) dial(ctx context.Context, network, localAddr, remoteAddr string) (net.Conn, error) {
	if network == TCP {
		return l.tcp.dial(ctx, localAddr, remoteAddr)
	}
	if network == UDP {
		return l.udp.dial(ctx, localAddr, remoteAddr)
	}
	return nil, ErrInvalidNetwork
}

func (l *layer) Dialer() LayerDialer {
	return &layerDialer{layer: l}
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

	// close protocols
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

func (d *layerDialer) WithLocalAddr(localAddr net.Addr) LayerDialer {
	d.localAddr = localAddr
	return d
}

func (d *layerDialer) Dial(ctx context.Context, remoteAddr string) (net.Conn, error) {
	if d.localAddr == nil {
		return nil, fmt.Errorf("localAddr was not specified")
	}
	return d.layer.dial(ctx, d.localAddr.Network(), d.localAddr.String(), remoteAddr)
}
