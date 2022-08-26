package link

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/google/gopacket"
	gplayers "github.com/google/gopacket/layers"
	"github.com/sirupsen/logrus"
)

type (
	// SwitchConfig contains the configs for RunSwitch().
	SwitchConfig struct {
		Ports []EthernetPortConfig `yaml:"ethernetPorts"`
	}

	SwitchWaitCloseFunc func(func(frame <-chan *gplayers.Ethernet))

	switchImpl struct {
		conf  *SwitchConfig
		ports []EthernetPort
	}
)

// RunSwitch runs a hypothetical L2 switch, which decaps and
// forwards ethernet frames based on a forwarding table
// constructed by learning L2 routes on the fly: frame comes
// in, a mapping from src MAC address to port number is cached.
// No mapping present on the table: frame is forwarded to all
// other ports.
// If dst MAC address matches the MAC address of one of the
// switch's ports, the frame is discarded.
//
// The returned function blocks until the switch has stopped
// running, which happens upon the given ctx being cancelled.
// The function argument is another function, passed for
// consuming any potential frames that remained in the ports.
func RunSwitch(ctx context.Context, conf SwitchConfig) (SwitchWaitCloseFunc, error) {
	// create switch
	if len(conf.Ports) < 3 {
		return nil, errors.New("switch will only run with a least three ports")
	}
	ports := make([]EthernetPort, 0, len(conf.Ports))
	for i, portConf := range conf.Ports {
		portConf.ForwardingMode = true
		port, err := NewEthernetPort(ctx, portConf)
		if err != nil {
			for j := i - 1; 0 <= j; j-- {
				ports[j].Close()
			}
			return nil, fmt.Errorf("error creating ethernet port number %d: %w", i, err)
		}
		ports = append(ports, port)
	}
	s := &switchImpl{
		conf:  &conf,
		ports: ports,
	}

	// start switching thread
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.run(ctx)
	}()
	return func(f func(frame <-chan *gplayers.Ethernet)) {
		wg.Wait()
		if f == nil {
			return
		}
		for _, port := range s.ports {
			f(port.Recv())
		}
	}, nil
}

func (s *switchImpl) run(ctx context.Context) {
	var wg sync.WaitGroup

	defer func() {
		wg.Wait() // wait all port threads first
		for _, port := range s.ports {
			port.Close()
		}
	}()

	var forwardingTable sync.Map
	storeRoute := func(macAddress gopacket.Endpoint, portNumber int) {
		oldPortNumber, hasOldRoute := forwardingTable.Load(macAddress)
		if !hasOldRoute || oldPortNumber.(int) != portNumber {
			forwardingTable.Store(macAddress, portNumber)
		}
	}

	portMACAddresses := make(map[gopacket.Endpoint]struct{})
	for _, port := range s.ports {
		portMACAddresses[port.MACAddress()] = struct{}{}
	}

	ctxDone := ctx.Done()
	for i, fromPort := range s.ports {
		// make local copies so the i-th thread captures
		// references only to its own (i, fromPort) pair
		i := i
		fromPort := fromPort

		// start port thread
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctxDone:
					return
				case frame := <-fromPort.Recv():
					l := logrus.
						WithField("from_port", i).
						WithField("frame", frame)

					// update forwarding table
					srcMAC := gplayers.NewMACEndpoint(frame.SrcMAC)
					storeRoute(srcMAC, i)

					// check if dst mac address matches the mac address of one of the ports
					dstMAC := gplayers.NewMACEndpoint(frame.DstMAC)
					if _, isPortMACAddress := portMACAddresses[dstMAC]; isPortMACAddress {
						continue
					}

					// fetch route and forward
					dstPort, hasRoute := forwardingTable.Load(dstMAC)
					if hasRoute {
						j := dstPort.(int)
						if err := s.ports[j].Send(ctx, frame); err != nil {
							l.
								WithError(err).
								WithField("to_port", j).
								Error("error forwarding frame")
						}
						continue
					}

					// no route, forward to all other ports
					for j, toPort := range s.ports {
						if j != i {
							if err := toPort.Send(ctx, frame); err != nil {
								l.
									WithError(err).
									WithField("to_port", j).
									Error("error forwarding frame")
							}
						}
					}
				}
			}
		}()
	}
}
