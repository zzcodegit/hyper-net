package dns

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
)

func TestNormalizeName(t *testing.T) {
	tests := []struct {
		in   string
		want string
		err  bool
	}{
		{"Exit-Node-1", "exit-node-1", false},
		{"a", "a", false},
		{"ab", "ab", false},
		{"my-node", "my-node", false},
		{"  trim  ", "trim", false},
		{"", "", true},
		{"bad name", "", true},
		{"UPPER", "upper", false},
		{"-leading", "", true},
		{"trailing-", "", true},
	}
	for _, tt := range tests {
		got, err := NormalizeName(tt.in)
		if (err != nil) != tt.err {
			t.Errorf("NormalizeName(%q) err=%v want err=%v", tt.in, err, tt.err)
			continue
		}
		if !tt.err && got != tt.want {
			t.Errorf("NormalizeName(%q)=%q want %q", tt.in, got, tt.want)
		}
	}
}

func TestRecord_SignAndVerify(t *testing.T) {
	privKey, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pubKey := privKey.GetPublic()
	rec := &Record{
		Name:      "test-node",
		Owner:     "12D3KooWTest",
		Addrs:     []string{"/ip4/127.0.0.1/tcp/4001"},
		TTL:       3600,
		Timestamp: time.Now().Unix(),
	}
	if err := rec.Sign(privKey); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	if rec.Signature == "" {
		t.Fatal("Signature empty after Sign")
	}
	if err := rec.Verify(pubKey); err != nil {
		t.Fatalf("Verify: %v", err)
	}
	// Wrong key must fail
	priv2, _, _ := crypto.GenerateEd25519Key(nil)
	if err := rec.Verify(priv2.GetPublic()); err == nil {
		t.Fatal("Verify with wrong key should fail")
	}
}

func TestRecord_Expired(t *testing.T) {
	now := time.Now()
	rec := &Record{TTL: 10, Timestamp: now.Unix() - 20}
	if !rec.Expired(now) {
		t.Error("expected expired")
	}
	rec.Timestamp = now.Unix()
	if rec.Expired(now) {
		t.Error("expected not expired")
	}
	rec.TTL = 0
	if !rec.Expired(now) {
		t.Error("TTL=0 should be expired")
	}
}

func TestDHTKey(t *testing.T) {
	if got := DHTKey("my-node"); got != "/hypernet/name/my-node" {
		t.Errorf("DHTKey(my-node)=%q", got)
	}
}

// TestRecord_SerializationRoundtrip проверяет сериализацию/десериализацию: marshal -> unmarshal -> Verify.
func TestRecord_SerializationRoundtrip(t *testing.T) {
	privKey, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	rec := &Record{
		Name:      "test-node",
		Owner:     "12D3KooWSerial",
		Addrs:     []string{"/ip4/192.168.1.1/tcp/4001", "/ip6/::1/tcp/4001"},
		TTL:       7200,
		Timestamp: 1234567890,
	}
	if err := rec.Sign(privKey); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Record
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if decoded.Signature != rec.Signature {
		t.Error("Signature changed after roundtrip")
	}
	if err := decoded.Verify(privKey.GetPublic()); err != nil {
		t.Fatalf("Verify after roundtrip: %v", err)
	}
	if decoded.Name != rec.Name || decoded.Timestamp != rec.Timestamp {
		t.Errorf("decoded name=%q ts=%d want name=%q ts=%d", decoded.Name, decoded.Timestamp, rec.Name, rec.Timestamp)
	}
}

// TestRecord_VerifyFailsWhenTampered проверяет, что после изменения поля верификация не проходит.
func TestRecord_VerifyFailsWhenTampered(t *testing.T) {
	privKey, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	rec := &Record{
		Name:      "test",
		Owner:     "12D3KooWTest",
		Addrs:     []string{"/ip4/127.0.0.1/tcp/4001"},
		TTL:       3600,
		Timestamp: time.Now().Unix(),
	}
	if err := rec.Sign(privKey); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	rec.Addrs = []string{"/ip4/10.0.0.1/tcp/9999"}
	if err := rec.Verify(privKey.GetPublic()); err == nil {
		t.Fatal("Verify should fail after tampering with Addrs")
	}
}

// TestRecord_TTL проверяет границы TTL: запись не истекла до timestamp+ttl, истекла после.
func TestRecord_TTL(t *testing.T) {
	base := time.Unix(1000, 0)
	rec := &Record{TTL: 10, Timestamp: 1000}
	if rec.Expired(base) {
		t.Error("at exactly timestamp, should not be expired")
	}
	if rec.Expired(time.Unix(1009, 0)) {
		t.Error("at timestamp+9 should not be expired")
	}
	if !rec.Expired(time.Unix(1011, 0)) {
		t.Error("at timestamp+11 should be expired")
	}
	rec.TTL = 0
	if !rec.Expired(base) {
		t.Error("TTL=0 should be expired")
	}
}
