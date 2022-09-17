package cmd

import (
	"errors"
	"fmt"
	"strings"
)

const (
	host    = "host"
	overlay = "overlay"
)

type (
	addr             string
	hostPort         string
	tcpProxyEntries  []string
	httpProxyEntries []string

	tcpProxyEntry struct {
		src addr
		dst addr
	}

	httpProxyEntry struct {
		src  addr
		host string
		dst  addr
	}
)

func (a addr) network() string {
	s := string(a)
	if strings.HasPrefix(s, host+":") {
		return host
	}
	if strings.HasPrefix(s, overlay+":") {
		return overlay
	}
	return ""
}

func (a addr) networkOrDefault() string {
	network := a.network()
	if network == "" {
		return overlay
	}
	return network
}

func (a addr) hostPort() hostPort {
	s := string(a)
	if network := a.network(); network != "" {
		s = strings.TrimPrefix(s, network+":")
	}
	return hostPort(s)
}

func (h hostPort) orLoopback() hostPort {
	s := string(h)
	portIdx := strings.Index(s, ":")
	if portIdx == 0 {
		s = fmt.Sprintf("127.0.0.1%s", s)
	}
	return hostPort(s)
}

func (h hostPort) orPort(port uint16) hostPort {
	s := string(h)
	portIdx := strings.Index(s, ":")
	if portIdx < 0 {
		s = fmt.Sprintf("%s:%d", s, port)
	}
	return hostPort(s)
}

func (h hostPort) String() string {
	return string(h)
}

func (t tcpProxyEntries) entries() ([]tcpProxyEntry, error) {
	if len(t)%2 != 0 {
		return nil, errors.New("number of specified addresses is not even")
	}
	p := make([]tcpProxyEntry, len(t)/2)
	for i := 0; i < len(t); i += 2 {
		src, dst := addr(t[i]), addr(t[i+1])
		p[i/2] = tcpProxyEntry{src, dst}
	}
	return p, nil
}

func (h httpProxyEntries) entries() ([]httpProxyEntry, error) {
	if len(h)%3 != 0 {
		return nil, errors.New("number of specified arguments is not multiple of three")
	}
	p := make([]httpProxyEntry, len(h)/3)
	for i := 0; i < len(h); i += 3 {
		src, host, dst := addr(h[i]), h[i+1], addr(h[i+2])
		p[i/3] = httpProxyEntry{src, host, dst}
	}
	return p, nil
}
