package obfuscate

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestNewDelayConn_ZeroDelay_Passthrough(t *testing.T) {
	buf := &bytes.Buffer{}
	wrap := NewDelayConn(&nopCloser{buf}, 0, 0)
	defer wrap.Close()

	before := time.Now()
	_, err := wrap.Write([]byte("x"))
	elapsed := time.Since(before)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if elapsed > 10*time.Millisecond {
		t.Errorf("zero delay should not sleep; elapsed %v", elapsed)
	}
	if buf.String() != "x" {
		t.Errorf("buffer = %q, want x", buf.String())
	}
}

func TestNewDelayConn_MinMaxClamped(t *testing.T) {
	// When delayMin > delayMax, implementation sets delayMin = delayMax
	wrap := NewDelayConn(&nopCloser{&bytes.Buffer{}}, 50*time.Millisecond, 10*time.Millisecond)
	defer wrap.Close()
	if wrap.DelayMin != wrap.DelayMax {
		t.Errorf("expected DelayMin clamped to DelayMax; got Min=%v Max=%v", wrap.DelayMin, wrap.DelayMax)
	}
}

func TestDelayConn_Write_DelaysWhenMaxPositive(t *testing.T) {
	buf := &bytes.Buffer{}
	minDelay := 15 * time.Millisecond
	maxDelay := 25 * time.Millisecond
	wrap := NewDelayConn(&nopCloser{buf}, minDelay, maxDelay)
	defer wrap.Close()

	before := time.Now()
	_, err := wrap.Write([]byte("hello"))
	elapsed := time.Since(before)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if elapsed < minDelay {
		t.Errorf("Write with delay [%v,%v] should take at least %v; elapsed %v", minDelay, maxDelay, minDelay, elapsed)
	}
	if buf.String() != "hello" {
		t.Errorf("buffer = %q, want hello", buf.String())
	}
}

func TestDelayConn_Read_Passthrough(t *testing.T) {
	// DelayConn only wraps Write with delay; Read goes to embedded reader
	content := []byte("read-me")
	buf := bytes.NewBuffer(content)
	r := &nopCloser{buf}
	wrap := NewDelayConn(r, 10*time.Millisecond, 20*time.Millisecond)
	defer wrap.Close()

	out := make([]byte, 32)
	n, err := wrap.Read(out)
	if err != nil && err != io.EOF {
		t.Fatalf("Read: %v", err)
	}
	if string(out[:n]) != string(content) {
		t.Errorf("Read = %q, want %q", out[:n], content)
	}
}

type nopCloser struct{ io.ReadWriter }
func (nopCloser) Close() error { return nil }

