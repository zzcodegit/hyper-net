package main

import "testing"

func TestParseDomains(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"example.com", []string{"example.com"}},
		{"example.com, www.example.com", []string{"example.com", "www.example.com"}},
		{"  a.com , , b.com  ", []string{"a.com", "b.com"}},
		{"", nil},
	}

	for _, tc := range cases {
		got := parseDomains(tc.in)
		if len(got) != len(tc.want) {
			t.Fatalf("parseDomains(%q): expected %d items, got %d", tc.in, len(tc.want), len(got))
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("parseDomains(%q)[%d]: expected %q, got %q", tc.in, i, tc.want[i], got[i])
			}
		}
	}
}

