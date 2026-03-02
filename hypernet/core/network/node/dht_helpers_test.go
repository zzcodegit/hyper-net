package node

import (
	"testing"
)

func TestStringKeyToCID_EmptyReturnsError(t *testing.T) {
	_, err := stringKeyToCID("")
	if err == nil {
		t.Fatal("expected error for empty key")
	}
	if err.Error() != "empty key" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestStringKeyToCID_Deterministic(t *testing.T) {
	key := "my-file"
	c1, err := stringKeyToCID(key)
	if err != nil {
		t.Fatalf("stringKeyToCID: %v", err)
	}
	c2, err := stringKeyToCID(key)
	if err != nil {
		t.Fatalf("stringKeyToCID (second): %v", err)
	}
	if c1 != c2 {
		t.Errorf("same key must produce same CID: %s != %s", c1, c2)
	}
	if !c1.Defined() {
		t.Error("CID should be defined")
	}
}

func TestStringKeyToCID_DifferentKeysDifferentCIDs(t *testing.T) {
	c1, _ := stringKeyToCID("key-a")
	c2, _ := stringKeyToCID("key-b")
	if c1 == c2 {
		t.Error("different keys must produce different CIDs")
	}
}

func TestProtocolKey(t *testing.T) {
	got := protocolKey("vless")
	want := "hypernet:proto:vless"
	if got != want {
		t.Errorf("protocolKey(%q) = %q, want %q", "vless", got, want)
	}
	got2 := protocolKey("trojan")
	want2 := "hypernet:proto:trojan"
	if got2 != want2 {
		t.Errorf("protocolKey(%q) = %q, want %q", "trojan", got2, want2)
	}
}

func TestProtocolRegionKey(t *testing.T) {
	got := protocolRegionKey("vless", "eu")
	want := "hypernet:proto:vless:region:eu"
	if got != want {
		t.Errorf("protocolRegionKey(%q, %q) = %q, want %q", "vless", "eu", got, want)
	}
	got2 := protocolRegionKey("trojan", "us-east-1")
	want2 := "hypernet:proto:trojan:region:us-east-1"
	if got2 != want2 {
		t.Errorf("protocolRegionKey(%q, %q) = %q, want %q", "trojan", "us-east-1", got2, want2)
	}
}

