package link

import (
	"context"
	"errors"
	"fmt"
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
// If dst MAC address matches the MAC address of the receiving
// port, the frame is discarded.
//
// If len(conf.Ports) is zero, then len(ports) must be
// greater than or equal to three.
func RunSwitch(
	ctx context.Context,
	conf SwitchConfig,
	ports ...EthernetPort,
) error {
	if len(ports) > 0 && len(conf.Ports) > 0 {
		return errors.New("specify one of ports or conf.Ports, not both")
	}
	if len(ports) == 0 && len(conf.Ports) == 0 {
		return errors.New("specify one of ports or conf.Ports")
	}
	if len(conf.Ports) > 0 && len(conf.Ports) < 3 {
		return errors.New("switch will only run with a least three ports")
	}
	if len(ports) > 0 && len(ports) < 3 {
		return errors.New("switch will only run with a least three ports")
	}
	for i, port := range ports {
		if port == nil {
			return fmt.Errorf("port number %d is nil", i)
		}
	}
	s := &switchImpl{
		conf: &conf,
	}
	if len(ports) > 0 {
		for i, port := range ports {
			if !port.ForwardingMode() {
				return fmt.Errorf("port number %d is not in forwarding mode", i)
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
				return fmt.Errorf("error creating ethernet port number %d: %w", i, err)
			}
			s.ports = append(s.ports, port)
		}
	}
	return s.run(ctx)
}

func (s *switchImpl) run(ctx context.Context) error {
	defer func() {
		for _, port := range s.ports {
			port.Close()
		}
	}()
	return nil // TODO
}
