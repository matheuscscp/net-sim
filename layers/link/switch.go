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
		Ports []*EthernetPortConfig `yaml:"ethernetPorts"`
	}

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
// If len(conf.Ports) is zero, then len(ports) must be
// greater than or equal to three.
//
// The returned function blocks until the switch has stopped
// running, which happens upon the given ctx being cancelled.
func RunSwitch(
	ctx context.Context,
	conf SwitchConfig,
	ports ...EthernetPort,
) (func(), error) {
	if len(ports) > 0 && len(conf.Ports) > 0 {
		return nil, errors.New("specify one of ports or conf.Ports, not both")
	}
	if len(ports) == 0 && len(conf.Ports) == 0 {
		return nil, errors.New("specify one of ports or conf.Ports")
	}
	if (len(conf.Ports) > 0 && len(conf.Ports) < 3) ||
		(len(ports) > 0 && len(ports) < 3) {
		return nil, errors.New("switch will only run with a least three ports")
	}
	for i, port := range ports {
		if port == nil {
			return nil, fmt.Errorf("port number %d is nil", i)
		}
	}
	s := &switchImpl{
		conf: &conf,
	}
	if len(ports) > 0 {
		for i, port := range ports {
			if port == nil {
				return nil, fmt.Errorf("port number %d is nil", i)
			}
			if !port.ForwardingMode() {
				return nil, fmt.Errorf("port number %d is not in forwarding mode", i)
			}
		}
		s.ports = ports
	} else {
		s.ports = make([]EthernetPort, 0, len(conf.Ports))
		for i, portConf := range conf.Ports {
			portConf.ForwardingMode = true
			port, err := NewEthernetPort(ctx, *portConf)
			if err != nil {
				for j := i - 1; 0 <= j; j-- {
					s.ports[j].Close()
				}
				return nil, fmt.Errorf("error creating ethernet port number %d: %w", i, err)
			}
			s.ports = append(s.ports, port)
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.run(ctx)
	}()
	return wg.Wait, nil
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

	portAddressToNumber := make(map[gopacket.Endpoint]int)
	for i, port := range s.ports {
		portAddressToNumber[port.MACAddress()] = i
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
				case eth := <-fromPort.Recv():
					l := logrus.
						WithField("from_port", i).
						WithField("frame", eth)

					// update forwarding table
					srcMAC := gplayers.NewMACEndpoint(eth.SrcMAC)
					storeRoute(srcMAC, i)

					// check if dst mac address matches the address of one of the ports
					dstMAC := gplayers.NewMACEndpoint(eth.DstMAC)
					if j, ok := portAddressToNumber[dstMAC]; ok {
						l.
							WithField("matched_port_number", j).
							Info("frame discarded because dst mac address matches the address of a port")
						continue
					}

					// fetch route and forward
					dstPort, hasRoute := forwardingTable.Load(dstMAC)
					if hasRoute {
						j := dstPort.(int)
						if err := s.ports[j].Send(ctx, eth); err != nil {
							l.
								WithError(err).
								WithField("to_port", j).
								Error("error forwarding frame")
						}
					} else { // no route, forward to all other ports
						for j, toPort := range s.ports {
							if j != i {
								if err := toPort.Send(ctx, eth); err != nil {
									l.
										WithError(err).
										WithField("to_port", j).
										Error("error forwarding frame")
								}
							}
						}
					}
				}
			}
		}()
	}
}
