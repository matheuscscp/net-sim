package link

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"net"

	"github.com/matheuscscp/net-sim/internal/common"
	"github.com/matheuscscp/net-sim/internal/layers/physical"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
)

type (
	// EthernetCard represents a hypothetical ethernet network interface
	// card, composed by a physical port and a MAC address.
	EthernetCard interface {
		Send(ctx context.Context, frame *gplayers.Ethernet) error
		Recv() (<-chan *gplayers.Ethernet, <-chan error)
		Close() error
	}

	// EthernetCardConfig contains the configs for the
	// concrete implementation of EthernetCard.
	EthernetCardConfig struct {
		// ForwardingMode keeps inbound frames with wrong dst address.
		ForwardingMode bool   `json:"forwardingMode"`
		MACAddress     string `json:"macAddress"`

		Medium *physical.FullDuplexUnreliablePortConfig `json:"fullDuplexUnreliablePortConfig"`
	}

	ethernetCard struct {
		conf          *EthernetCardConfig
		macAddress    gopacket.Endpoint
		medium        physical.FullDuplexUnreliablePort
		ch            chan *gplayers.Ethernet
		err           chan error
		cancelRecvCtx func()
		recvClosedCh  chan struct{}
	}
)

// NewEthernetCard creates a EthernetCard from config.
// If conf.Medium is nil, then a medium must be passed.
func NewEthernetCard(
	ctx context.Context,
	conf EthernetCardConfig,
	medium ...physical.FullDuplexUnreliablePort,
) (EthernetCard, error) {
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
	nic := &ethernetCard{
		conf:       &conf,
		macAddress: gplayers.NewMACEndpoint(macAddress),
	}
	if len(medium) == 1 {
		nic.medium = medium[0]
	} else if nic.medium, err = physical.NewFullDuplexUnreliablePort(ctx, *conf.Medium); err != nil {
		return nil, fmt.Errorf("error creating medium: %w", err)
	}
	nic.recv() // init internal state
	return nic, nil
}

func (e *ethernetCard) Send(ctx context.Context, frame *gplayers.Ethernet) error {
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
	got, err := e.medium.Send(ctx, finalBuf)
	if err != nil {
		return fmt.Errorf("error sending ethernet frame: %w", err)
	}
	if want := len(finalBuf); got < want {
		return fmt.Errorf("wrong number of bytes sent. want %d, got %d", want, got)
	}

	return nil
}

func (e *ethernetCard) Recv() (<-chan *gplayers.Ethernet, <-chan error) {
	return e.ch, e.err
}

func (e *ethernetCard) recv() {
	e.ch = make(chan *gplayers.Ethernet, MaxQueueSize)
	e.err = make(chan error, MaxQueueSize)
	var ctx context.Context
	ctx, e.cancelRecvCtx = context.WithCancel(context.Background())
	e.recvClosedCh = make(chan struct{})

	go func() {
		defer func() {
			close(e.ch)
			close(e.err)
			close(e.recvClosedCh)
		}()
		for {
			buf := make([]byte, 2*MTU)
			n, err := e.medium.Recv(ctx, buf)
			if err != nil {
				if errors.Is(err, context.Canceled) {
					return
				}
				e.err <- fmt.Errorf("error recv()ing frame: %w", err)
				continue
			}
			e.decap(buf[:n])
		}
	}()
}

func (e *ethernetCard) decap(frame []byte) {
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
		}

		return nil
	}()
	if err != nil {
		e.err <- err
	} else if eth != nil {
		e.ch <- eth
	}
}

func (e *ethernetCard) Close() error {
	if e.cancelRecvCtx == nil {
		return nil
	}
	e.cancelRecvCtx()
	e.cancelRecvCtx = nil
	<-e.recvClosedCh
	return e.medium.Close()
}
