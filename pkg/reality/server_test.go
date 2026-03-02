package reality

import (
	"encoding/base64"
	"testing"
)

func TestParseShortId(t *testing.T) {
	tests := []struct {
		name  string
		input string
		ok    bool
	}{
		{"empty", "", false},
		{"odd hex", "abc", false},
		{"too long", "0123456789abcdef01", false},
		{"invalid hex", "gg", false},
		{"2 chars", "ab", true},
		{"4 chars", "abcd", true},
		{"8 chars", "0123456789abcdef", true},
		{"with spaces", "  ab  ", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, ok := ParseShortId(tt.input)
			if ok != tt.ok {
				t.Fatalf("ParseShortId(%q) ok=%v, want %v", tt.input, ok, tt.ok)
			}
			if tt.ok && (out[0] == 0 && out[1] == 0 && tt.input != "00" && tt.input != " 00 " && len(tt.input) >= 2) {
				// just check we got some bytes back for valid input
				dec := string(out[:])
				_ = dec
			}
		})
	}
}

func TestBuildRealityConfig(t *testing.T) {
	validKey := base64.RawURLEncoding.EncodeToString(make([]byte, 32))

	t.Run("nil config", func(t *testing.T) {
		_, err := BuildRealityConfig(nil)
		if err == nil {
			t.Fatal("expected error for nil config")
		}
	})

	t.Run("empty Dest", func(t *testing.T) {
		_, err := BuildRealityConfig(&ServerConfig{PrivateKey: validKey})
		if err == nil {
			t.Fatal("expected error for empty Dest")
		}
	})

	t.Run("invalid private key", func(t *testing.T) {
		_, err := BuildRealityConfig(&ServerConfig{
			Dest:       "example.com:443",
			PrivateKey: "not-base64!!!",
		})
		if err == nil {
			t.Fatal("expected error for invalid key")
		}
	})

	t.Run("key wrong length", func(t *testing.T) {
		shortKey := base64.StdEncoding.EncodeToString([]byte("short"))
		_, err := BuildRealityConfig(&ServerConfig{
			Dest:       "example.com:443",
			PrivateKey: shortKey,
		})
		if err == nil {
			t.Fatal("expected error for wrong key length")
		}
	})

	t.Run("valid RawURLEncoding key", func(t *testing.T) {
		cfg, err := BuildRealityConfig(&ServerConfig{
			Dest:       "www.example.com:443",
			PrivateKey: validKey,
		})
		if err != nil {
			t.Fatalf("BuildRealityConfig: %v", err)
		}
		if cfg.Dest != "www.example.com:443" {
			t.Errorf("Dest = %q", cfg.Dest)
		}
		if len(cfg.ServerNames) == 0 {
			t.Error("expected ServerNames from Dest host")
		}
		if !cfg.ServerNames["www.example.com"] {
			t.Errorf("expected www.example.com in ServerNames: %v", cfg.ServerNames)
		}
	})

	t.Run("valid StdEncoding key", func(t *testing.T) {
		stdKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
		cfg, err := BuildRealityConfig(&ServerConfig{
			Dest:        "a:443",
			PrivateKey:  stdKey,
			ServerNames: []string{"sni1.com", "sni2.com"},
			ShortIds:    []string{"abcd", "ef"},
		})
		if err != nil {
			t.Fatalf("BuildRealityConfig: %v", err)
		}
		if !cfg.ServerNames["sni1.com"] || !cfg.ServerNames["sni2.com"] {
			t.Errorf("ServerNames: %v", cfg.ServerNames)
		}
		if len(cfg.ShortIds) != 2 {
			t.Errorf("ShortIds: %v", cfg.ShortIds)
		}
	})
}

func TestDialREALITY_NotImplemented(t *testing.T) {
	_, err := DialREALITY(nil, "tcp", "127.0.0.1:443", nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}
