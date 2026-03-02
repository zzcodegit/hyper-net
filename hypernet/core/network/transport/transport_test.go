package transport

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/multiformats/go-multiaddr"
)

func TestTCPTransport_Name(t *testing.T) {
	tr := TCP()
	if tr.Name() != TCPTransportName {
		t.Errorf("Name() = %q, want %q", tr.Name(), TCPTransportName)
	}
}

func TestTCPTransport_DialListen(t *testing.T) {
	tr := TCP()
	ma, err := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/0")
	if err != nil {
		t.Fatalf("NewMultiaddr: %v", err)
	}
	// Listen on random port: we need a multiaddr with port 0, then get actual addr from listener
	ln, err := tr.Listen(ma)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	defer ln.Close()

	addr := ln.Addr().String()
	t.Logf("listening on %s", addr)
	host, port, _ := net.SplitHostPort(addr)
	listenMA, err := multiaddr.NewMultiaddr("/ip4/" + host + "/tcp/" + port)
	if err != nil {
		t.Fatalf("listen multiaddr: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		conn, err := tr.Dial(ctx, listenMA)
		if err != nil {
			t.Errorf("Dial: %v", err)
			close(done)
			return
		}
		conn.Close()
		close(done)
	}()

	accepted, err := ln.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	accepted.Close()

	<-done
}

func TestRegistry_GetAndByMultiaddr(t *testing.T) {
	r := NewRegistry()
	r.Register(TCP())
	if tr, ok := r.Get("tcp"); !ok || tr == nil {
		t.Fatalf("Get(tcp) = %v, %v", tr, ok)
	}
	ma, _ := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/4001")
	tr, ok := r.ByMultiaddr(ma)
	if !ok || tr == nil || tr.Name() != "tcp" {
		t.Errorf("ByMultiaddr = %v, %v", tr, ok)
	}
}

