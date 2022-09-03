package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/matheuscscp/net-sim/layers/network"

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
		Listen(network, address string) (net.Listener, error)

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
		tcp       *tcp
		udp       *udp
		cancelCtx context.CancelFunc
		wg        sync.WaitGroup
		giantBuf  chan *gplayers.IPv4
	}
)

const (
	useOfClosedConn = "use of closed network connection"
)

var (
	ErrInvalidNetwork       = errors.New("invalid network")
	ErrPortAlreadyInUse     = errors.New("port already in use")
	ErrAllPortsAlreadyInUse = errors.New("all ports already in use")
	ErrListenerClosed       = fmt.Errorf("listener closed (os error msg: %s)", useOfClosedConn)
	ErrConnClosed           = fmt.Errorf("connection closed (os error msg: %s)", useOfClosedConn)
	ErrTimeout              = errors.New("timeout")
)

// NewLayer creates the concrete implementation of Layer and
// calls networkLayer.Listen() on a thread. Use Close() to
// stop and wait for this thread.
func NewLayer(networkLayer network.Layer) Layer {
	ctx, cancel := context.WithCancel(context.Background())

	l := &layer{
		tcp:       newTCP(ctx, networkLayer),
		udp:       newUDP(ctx, networkLayer),
		cancelCtx: cancel,
		giantBuf:  make(chan *gplayers.IPv4, demuxThreads*channelSize),
	}

	// create thread for listening to IP datagrams and push them
	// into the giant buffer
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		networkLayer.Listen(ctx, func(datagram *gplayers.IPv4) {
			l.giantBuf <- datagram
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

func (l *layer) Listen(network, address string) (net.Listener, error) {
	if network == TCP {
		return l.tcp.listen(address)
	}
	if network == UDP {
		return l.udp.listen(address)
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
	if l.cancelCtx == nil {
		return nil
	}

	// close threads
	l.cancelCtx()
	l.cancelCtx = nil
	l.wg.Wait()

	// close channels
	close(l.giantBuf)
	for range l.giantBuf {
	}

	return nil
}

// IsUseOfClosedConn tells whether the error is due to the port/connection
// being closed.
func IsUseOfClosedConn(err error) bool {
	return strings.Contains(err.Error(), useOfClosedConn)
}

func parseHostPort(address string, needIP bool) (int, *gopacket.Endpoint, error) {
	host, p, err := net.SplitHostPort(address)
	if err != nil {
		return 0, nil, fmt.Errorf("error splitting in host:port: %w", err)
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return 0, nil, fmt.Errorf("error parsing port number: %w", err)
	}
	if port < 0 || 65535 < port {
		return 0, nil, errors.New("port number must be in range [0, 65535]")
	}
	var ipAddress *gopacket.Endpoint
	if len(host) > 0 {
		ip := net.ParseIP(host)
		if ip == nil {
			return 0, nil, fmt.Errorf("unknown error parsing host '%s' as IP address", host)
		}
		ep := gplayers.NewIPEndpoint(ip)
		ipAddress = &ep
	}
	if needIP && ipAddress == nil {
		return 0, nil, errors.New("host cannot be empty")
	}
	return port, ipAddress, nil
}
