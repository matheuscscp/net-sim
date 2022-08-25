package link

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"net"
	"sync"

	"github.com/matheuscscp/net-sim/layers/common"
	"github.com/matheuscscp/net-sim/layers/physical"
	pkgcontext "github.com/matheuscscp/net-sim/pkg/context"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/sirupsen/logrus"
)

type (
	// EthernetPort represents a hypothetical ethernet network interface
	// card, composed by a physical port and a MAC address.
	//
	// Inbound frames with dst MAC address not matching the port's MAC
	// address will be discarded, unless if running on "forwarding mode".
	EthernetPort interface {
		Send(ctx context.Context, frame *gplayers.Ethernet) error
		Recv() <-chan *gplayers.Ethernet
		Close() error
		ForwardingMode() bool
		MACAddress() gopacket.Endpoint
	}

	// EthernetPortConfig contains the configs for the
	// concrete implementation of EthernetPort.
	EthernetPortConfig struct {
		// ForwardingMode keeps inbound frames with wrong dst address.
		ForwardingMode bool   `yaml:"forwardingMode"`
		MACAddress     string `yaml:"macAddress"`

		Medium *physical.FullDuplexUnreliablePortConfig `yaml:"fullDuplexUnreliablePort"`
	}

	ethernetPort struct {
		conf       *EthernetPortConfig
		l          logrus.FieldLogger
		macAddress gopacket.Endpoint
		medium     physical.FullDuplexUnreliablePort
		out        chan *outFrame
		in         chan *gplayers.Ethernet
		cancelCtx  func()
		wg         sync.WaitGroup
	}

	outFrame struct {
		ctx           context.Context
		buf           []byte
		dstMACAddress net.HardwareAddr
	}
)

// NewEthernetPort creates an EthernetPort from config.
// If conf.Medium is nil, then a medium must be passed.
func NewEthernetPort(
	ctx context.Context,
	conf EthernetPortConfig,
	medium ...physical.FullDuplexUnreliablePort,
) (EthernetPort, error) {
	if len(medium) > 0 && conf.Medium != nil {
		return nil, errors.New("specify one of medium or conf.Medium, not both")
	}
	if len(medium) == 0 && conf.Medium == nil {
		return nil, errors.New("specify one of medium or conf.Medium")
	}
	if len(medium) > 1 {
		return nil, errors.New("can only handle one medium")
	}
	if len(medium) == 1 && medium[0] == nil {
		return nil, errors.New("nil medium")
	}
	macAddress, err := net.ParseMAC(conf.MACAddress)
	if err != nil {
		return nil, fmt.Errorf("error parsing mac address: %w", err)
	}
	nic := &ethernetPort{
		conf:       &conf,
		l:          logrus.WithField("port_mac_address", conf.MACAddress),
		macAddress: gplayers.NewMACEndpoint(macAddress),
		out:        make(chan *outFrame, MaxQueueSize),
		in:         make(chan *gplayers.Ethernet, MaxQueueSize),
	}
	if len(medium) == 1 {
		nic.medium = medium[0]
	} else if nic.medium, err = physical.NewFullDuplexUnreliablePort(ctx, *conf.Medium); err != nil {
		return nil, fmt.Errorf("error creating medium: %w", err)
	}
	nic.startThreads()
	return nic, nil
}

func (e *ethernetPort) startThreads() {
	var ctx context.Context
	ctx, e.cancelCtx = context.WithCancel(context.Background())
	ctxDone := ctx.Done()

	// send
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		for {
			select {
			case <-ctxDone:
				return
			case frame := <-e.out:
				func() {
					// here we need new a context that must be cancelled if either ctx
					// or frame.ctx are done
					ctx, cancel := pkgcontext.WithCancelOnAnotherContext(frame.ctx, ctx)
					defer cancel()
					l := e.l.
						WithField("dst_mac_address", frame.dstMACAddress.String()).
						WithField("frame_buf", frame.buf)

					// send
					got, err := e.medium.Send(ctx, frame.buf)
					if err != nil {
						l.
							WithError(err).
							Error("error sending ethernet frame")
					} else if want := len(frame.buf); got < want {
						l.
							WithField("want", want).
							WithField("got", got).
							Error("wrong number of bytes sent for ethernet frame")
					}
				}()
			}
		}
	}()

	// recv
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		for {
			buf := make([]byte, 2*MTU)
			n, err := e.medium.Recv(ctx, buf)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				e.l.
					WithError(err).
					Error("error receiving ethernet frame")
				continue
			}
			e.decap(buf[:n])
		}
	}()
}

func (e *ethernetPort) Send(ctx context.Context, frame *gplayers.Ethernet) error {
	// validate payload size
	if len(frame.Payload) == 0 {
		return common.ErrCannotSendEmpty
	}
	if len(frame.Payload) > MTU {
		return fmt.Errorf("payload is larger than link layer MTU (%d)", MTU)
	}

	// serialize frame
	if !e.ForwardingMode() {
		frame.SrcMAC = e.macAddress.Raw()
	}
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{}
	payload := gopacket.Payload(frame.Payload)
	if err := gopacket.SerializeLayers(buf, opts, frame, payload); err != nil {
		return fmt.Errorf("error serializing ethernet layer: %w", err)
	}

	// serialize crc32 checksum
	crc := crc32.Checksum(buf.Bytes(), crc32.MakeTable(crc32.Castagnoli))
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, crc)
	finalBuf := append(buf.Bytes(), b...)

	// send
	e.out <- &outFrame{ctx, finalBuf, frame.DstMAC}

	return nil
}

func (e *ethernetPort) Recv() <-chan *gplayers.Ethernet {
	return e.in
}

func (e *ethernetPort) decap(frameBuf []byte) {
	l := e.l.WithField("frame_buf", frameBuf)

	var frame *gplayers.Ethernet
	err := func() error {
		// split frame data and crc
		if len(frameBuf) < 4 {
			return errors.New("frame has less than 4 bytes, cannot be valid")
		}
		siz := len(frameBuf) - 4
		frameData, crcBuf := frameBuf[:siz], frameBuf[siz:]

		// validate crc
		crc := crc32.Checksum(frameData, crc32.MakeTable(crc32.Castagnoli))
		expectedCrc := binary.LittleEndian.Uint32(crcBuf)
		if crc != expectedCrc {
			return fmt.Errorf("crc32.Castagnoli integrity check failed, want %x, got %x", expectedCrc, crc)
		}

		// decap
		pkt := gopacket.NewPacket(frameData, gplayers.LayerTypeEthernet, gopacket.Lazy)
		frame = pkt.LinkLayer().(*gplayers.Ethernet)
		if frame == nil || len(frame.Payload) == 0 {
			return pkt.ErrorLayer().Error()
		}
		dstMACAddress := gplayers.NewMACEndpoint(frame.DstMAC)
		if dstMACAddress != BroadcastMACEndpoint &&
			!e.ForwardingMode() &&
			e.macAddress != dstMACAddress {
			l.
				WithField("frame", frame).
				Info("discarding ethernet frame due to unmatched dst mac address")
			frame = nil
		}

		return nil
	}()

	if err != nil {
		l.
			WithError(err).
			Error("error decapsulating link layer")
		return
	}

	if frame != nil {
		e.in <- frame
	}
}

func (e *ethernetPort) Close() error {
	if e.cancelCtx == nil {
		return nil
	}

	// close threads
	e.cancelCtx()
	e.cancelCtx = nil
	e.wg.Wait()

	// close channels
	close(e.out)
	for range e.out {
	}
	close(e.in)

	return e.medium.Close()
}

func (e *ethernetPort) ForwardingMode() bool {
	return e.conf.ForwardingMode
}

func (e *ethernetPort) MACAddress() gopacket.Endpoint {
	return e.macAddress
}
