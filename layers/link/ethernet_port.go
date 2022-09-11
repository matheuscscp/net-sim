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

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/sirupsen/logrus"
)

type (
	// EthernetPort represents a hypothetical Ethernet network interface
	// card, composed by a physical wire and a MAC address.
	//
	// When sending a frame out it goes with the MAC address of the
	// port as its src MAC address, unless if running on "forwarding
	// mode".
	//
	// Inbound frames with dst MAC address not matching the port's MAC
	// address will be discarded, unless if running on "forwarding mode"
	// or if the dst MAC address is the broadcast MAC address.
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
		// ForwardingMode keeps inbound frames with wrong dst MAC address.
		ForwardingMode bool   `yaml:"forwardingMode"`
		MACAddress     string `yaml:"macAddress"`

		Medium physical.FullDuplexUnreliableWireConfig `yaml:"fullDuplexUnreliableWire"`
	}

	ethernetPort struct {
		ctx        context.Context
		cancelCtx  context.CancelFunc
		conf       *EthernetPortConfig
		l          logrus.FieldLogger
		macAddress gopacket.Endpoint
		medium     physical.FullDuplexUnreliableWire
		out        chan *outFrame
		in         chan *gplayers.Ethernet
		wg         sync.WaitGroup
	}

	outFrame struct {
		buf           []byte
		dstMACAddress net.HardwareAddr
	}
)

// NewEthernetPort creates an EthernetPort from config.
func NewEthernetPort(ctx context.Context, conf EthernetPortConfig) (EthernetPort, error) {
	macAddress, err := net.ParseMAC(conf.MACAddress)
	if err != nil {
		return nil, fmt.Errorf("error parsing mac address: %w", err)
	}
	medium, err := physical.NewFullDuplexUnreliableWire(ctx, conf.Medium)
	if err != nil {
		return nil, fmt.Errorf("error creating medium: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	nic := &ethernetPort{
		ctx:        ctx,
		cancelCtx:  cancel,
		conf:       &conf,
		l:          logrus.WithField("port_mac_address", macAddress.String()),
		macAddress: gplayers.NewMACEndpoint(macAddress),
		medium:     medium,
		out:        make(chan *outFrame, channelSize),
		in:         make(chan *gplayers.Ethernet, channelSize),
	}
	nic.startThreads()
	return nic, nil
}

func (e *ethernetPort) startThreads() {
	// send
	ctxDone := e.ctx.Done()
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		for {
			select {
			case <-ctxDone:
				return
			case frame := <-e.out:
				l := e.l.
					WithField("frame", frame)
				got, err := e.medium.Send(e.ctx, frame.buf)
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
			}
		}
	}()

	// recv
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		for {
			buf := make([]byte, 2*MTU)
			n, err := e.medium.Recv(e.ctx, buf)
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
	crc := crc32.Checksum(buf.Bytes(), crc32.MakeTable(crc32.IEEE))
	b := make([]byte, ChecksumLength)
	binary.LittleEndian.PutUint32(b, crc)
	finalBuf := append(buf.Bytes(), b...)

	// send
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-e.ctx.Done():
		return e.ctx.Err()
	case e.out <- &outFrame{finalBuf, frame.DstMAC}:
	}

	return nil
}

func (e *ethernetPort) Recv() <-chan *gplayers.Ethernet {
	return e.in
}

func (e *ethernetPort) decap(frameBuf []byte) {
	var frame *gplayers.Ethernet
	err := func() error {
		// split frame data and crc
		if len(frameBuf) < ChecksumLength {
			return fmt.Errorf("frame has less than %d bytes, cannot be valid", ChecksumLength)
		}
		siz := len(frameBuf) - ChecksumLength
		frameData, crcBuf := frameBuf[:siz], frameBuf[siz:]

		// validate crc
		crc := crc32.Checksum(frameData, crc32.MakeTable(crc32.IEEE))
		expectedCrc := binary.LittleEndian.Uint32(crcBuf)
		if crc != expectedCrc {
			return fmt.Errorf("crc32.Castagnoli integrity check failed, want %x, got %x", expectedCrc, crc)
		}

		// deserialize frame
		pkt := gopacket.NewPacket(frameData, gplayers.LayerTypeEthernet, gopacket.Lazy)
		frame = pkt.LinkLayer().(*gplayers.Ethernet)
		if frame == nil || len(frame.Payload) == 0 {
			return fmt.Errorf("error deserializing link layer: %w", pkt.ErrorLayer().Error())
		}

		// check discard
		dstMACAddress := gplayers.NewMACEndpoint(frame.DstMAC)
		if dstMACAddress != BroadcastMACEndpoint() &&
			!e.ForwardingMode() &&
			e.macAddress != dstMACAddress {
			frame = nil
		}

		return nil
	}()

	if err != nil {
		e.l.
			WithError(err).
			WithField("frame_buf", frameBuf).
			Error("error decapsulating link layer")
		return
	}

	if frame != nil {
		select {
		case <-e.ctx.Done():
			e.l.
				WithError(e.ctx.Err()).
				WithField("frame", frame).
				Error("port context done while receiving frame")
		case e.in <- frame:
		}
	}
}

func (e *ethernetPort) Close() error {
	// cancel ctx and wait threads
	var cancel context.CancelFunc
	cancel, e.cancelCtx = e.cancelCtx, nil
	if cancel == nil {
		return nil
	}
	cancel()
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
