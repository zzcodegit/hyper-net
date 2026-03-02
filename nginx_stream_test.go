package main

import (
	"os"
	"strings"
	"testing"
)

// TestNginxStreamExample_ContainsExpectedUpstream verifies that the example
// nginx stream configuration for Cloudflare Spectrum keeps the expected
// upstream/port wiring towards hypernet-node.
func TestNginxStreamExample_ContainsExpectedUpstream(t *testing.T) {
	data, err := os.ReadFile("nginx-stream.conf.example")
	if err != nil {
		t.Fatalf("failed to read nginx-stream.conf.example: %v", err)
	}
	cfg := string(data)

	wantSnippets := []string{
		"stream {",
		"upstream hypernet_node_tcp {",
		"server 127.0.0.1:4001;",
		"server {",
		"listen 443;",
		"proxy_pass hypernet_node_tcp;",
	}

	for _, s := range wantSnippets {
		if !strings.Contains(cfg, s) {
			t.Fatalf("nginx-stream.conf.example: expected to contain %q", s)
		}
	}
}

