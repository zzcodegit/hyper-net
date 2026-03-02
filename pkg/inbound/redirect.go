package inbound

import (
	"context"
	"net"
	"strconv"
	"strings"
)

const RedirectOutboundName = "redirect"

// Redirect is an outbound that always connects to a fixed target (for Dokodemo-door).
type Redirect struct {
	TargetHost string
	TargetPort int
	Dialer     *net.Dialer
}

// NewRedirect creates an outbound that always connects to target (host:port).
func NewRedirect(target string) (*Redirect, error) {
	host, port, err := splitHostPort(target)
	if err != nil {
		return nil, err
	}
	if port == 0 {
		port = 80
	}
	return &Redirect{TargetHost: host, TargetPort: port, Dialer: &net.Dialer{}}, nil
}

func splitHostPort(s string) (host string, port int, err error) {
	s = strings.TrimSpace(s)
	idx := strings.LastIndex(s, ":")
	if idx < 0 {
		return s, 0, nil
	}
	host = s[:idx]
	p, err := strconv.Atoi(s[idx+1:])
	if err != nil || p <= 0 || p > 65535 {
		return "", 0, err
	}
	return host, p, nil
}

// Dial connects to TargetHost:TargetPort (ignores host/port args).
func (r *Redirect) Dial(ctx context.Context, network, host string, port int) (net.Conn, error) {
	addr := net.JoinHostPort(r.TargetHost, strconv.Itoa(r.TargetPort))
	if r.Dialer != nil {
		return r.Dialer.DialContext(ctx, network, addr)
	}
	return net.Dial(network, addr)
}

func (r *Redirect) Name() string { return RedirectOutboundName }
