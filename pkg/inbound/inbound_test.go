package inbound

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestFreedom_Name(t *testing.T) {
	f := NewFreedom(0)
	if got := f.Name(); got != FreedomOutboundName {
		t.Errorf("Name() = %q, want %q", got, FreedomOutboundName)
	}
}

func TestFreedom_Dial(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	f := NewFreedom(time.Second)
	ctx := context.Background()
	conn, err := f.Dial(ctx, "tcp", host, port)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	conn.Close()
}

func TestBlackhole_Name(t *testing.T) {
	b := NewBlackhole()
	if got := b.Name(); got != BlackholeOutboundName {
		t.Errorf("Name() = %q, want %q", got, BlackholeOutboundName)
	}
}

func TestBlackhole_Dial(t *testing.T) {
	b := NewBlackhole()
	conn, err := b.Dial(context.Background(), "tcp", "example.com", 443)
	if err == nil {
		t.Fatal("expected error from Blackhole.Dial")
	}
	if conn != nil {
		t.Fatal("expected nil conn")
	}
}

func TestNewRedirect_invalid(t *testing.T) {
	_, err := NewRedirect("bad:port")
	if err == nil {
		t.Fatal("expected error for invalid port")
	}
}

func TestNewRedirect_valid(t *testing.T) {
	r, err := NewRedirect("example.com:443")
	if err != nil {
		t.Fatalf("NewRedirect: %v", err)
	}
	if r.TargetHost != "example.com" || r.TargetPort != 443 {
		t.Errorf("TargetHost=%q TargetPort=%d", r.TargetHost, r.TargetPort)
	}
	if got := r.Name(); got != RedirectOutboundName {
		t.Errorf("Name() = %q", got)
	}
}

func TestRedirect_Dial(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	r, err := NewRedirect("127.0.0.1:" + port)
	if err != nil {
		t.Fatal(err)
	}
	// Dial with different host/port - Redirect should connect to target
	conn, err := r.Dial(context.Background(), "tcp", "other.com", 80)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	conn.Close()
}
