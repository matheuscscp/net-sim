package link

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"net"

	"github.com/matheuscscp/net-sim/internal/layers/common"
	"github.com/matheuscscp/net-sim/internal/layers/physical"
	pkgcontext "github.com/matheuscscp/net-sim/pkg/context"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/sirupsen/logrus"
)

type (
	// EthernetPort represents a hypothetical ethernet network interface
	// card, composed by a physical port and a MAC address. Inbound frames
	// with dst MAC address not matching the port's MAC address will be
	// discarded, unless if running on "forwarding mode".
	EthernetPort interface {
		Send(ctx context.Context, frame *gplayers.Ethernet) error
		Recv() <-chan *gplayers.Ethernet
		Close() error
		ForwardingMode() bool
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
		conf                       *EthernetPortConfig
		macAddress                 gopacket.Endpoint
		medium                     physical.FullDuplexUnreliablePort
		out                        chan *outFrame
		in                         chan *gplayers.Ethernet
		cancelCtx                  func()
		sendClosedCh, recvClosedCh chan struct{}
	}

	outFrame struct {
		ctx context.Context
		buf []byte
	}
)

// NewEthernetPort creates a EthernetPort from config.
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
		return nil, fmt.Errorf("error parsing MAC address: %w", err)
	}
	nic := &ethernetPort{
		conf:       &conf,
		macAddress: gplayers.NewMACEndpoint(macAddress),
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
	ctxDoneCh := ctx.Done()

	// send
	e.out = make(chan *outFrame, MaxQueueSize)
	e.sendClosedCh = make(chan struct{})
	go func() {
		defer close(e.sendClosedCh)
		for {
			select {
			case <-ctxDoneCh:
				return
			case frame := <-e.out:
				func() {
					ctx, cancel := pkgcontext.WithCancelOnAnotherContext(ctx, frame.ctx)
					defer cancel()
					l := logrus.WithField("frame_buf", frame.buf)

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
	e.in = make(chan *gplayers.Ethernet, MaxQueueSize)
	e.recvClosedCh = make(chan struct{})
	go func() {
		defer close(e.recvClosedCh)
		for {
			buf := make([]byte, 2*MTU)
			n, err := e.medium.Recv(ctx, buf)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				logrus.
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
	frame.SrcMAC = e.macAddress.Raw()
	buf := gopacket.NewSerializeBuffer()
	opts := gopacket.SerializeOptions{}
	if err := gopacket.SerializeLayers(buf, opts, frame, gopacket.Payload(frame.Payload)); err != nil {
		return fmt.Errorf("error serializing ethernet layer")
	}

	// serialize crc32 checksum
	crc := crc32.Checksum(buf.Bytes(), crc32.MakeTable(crc32.Castagnoli))
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, crc)
	finalBuf := append(buf.Bytes(), b...)

	// send
	e.out <- &outFrame{ctx, finalBuf}

	return nil
}

func (e *ethernetPort) Recv() <-chan *gplayers.Ethernet {
	return e.in
}

func (e *ethernetPort) decap(frame []byte) {
	l := logrus.WithField("frame_buf", frame)

	var eth *gplayers.Ethernet
	err := func() error {
		// split frame data and crc
		if len(frame) < 4 {
			return errors.New("frame has less than 4 bytes, cannot be valid")
		}
		siz := len(frame) - 4
		frameData, crcBuf := frame[:siz], frame[siz:]

		// validate crc
		crc := crc32.Checksum(frameData, crc32.MakeTable(crc32.Castagnoli))
		expectedCrc := binary.LittleEndian.Uint32(crcBuf)
		if crc != expectedCrc {
			return fmt.Errorf("crc32.Castagnoli integrity check failed, want %x, got %x", expectedCrc, crc)
		}

		// decap
		var isEth bool
		eth, isEth = gopacket.
			NewPacket(frameData, gplayers.LayerTypeEthernet, gopacket.Default).
			LinkLayer().(*gplayers.Ethernet)
		if !isEth {
			return errors.New("link layer is not ethernet")
		}
		if !e.conf.ForwardingMode && e.macAddress != gplayers.NewMACEndpoint(eth.DstMAC) {
			eth = nil
			l.
				WithField("frame", eth).
				Warn("discarding ethernet frame due to unmatched dst MAC address")
		}

		return nil
	}()
	if err != nil {
		l.
			WithError(err).
			Error("error decapsulating ethernet frame")
	} else if eth != nil {
		e.in <- eth
	}
}

func (e *ethernetPort) Close() error {
	if e.cancelCtx == nil {
		return nil
	}

	// close threads
	e.cancelCtx()
	e.cancelCtx = nil
	<-e.sendClosedCh
	<-e.recvClosedCh

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
