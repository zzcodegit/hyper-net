package dns

import (
	"encoding/json"
	"testing"
)

// TestDHTRecordValidator_SelectByTimestamp проверяет выбор записи с максимальным timestamp из нескольких.
func TestDHTRecordValidator_SelectByTimestamp(t *testing.T) {
	v := DHTRecordValidator()
	key := "/hypernet/name/test"

	recOld := &Record{Name: "test", Owner: "peer1", Addrs: []string{"/ip4/1.2.3.4/tcp/1"}, TTL: 3600, Timestamp: 100, Signature: "x"}
	recNew := &Record{Name: "test", Owner: "peer2", Addrs: []string{"/ip4/5.6.7.8/tcp/2"}, TTL: 3600, Timestamp: 200, Signature: "y"}
	dataOld, _ := json.Marshal(recOld)
	dataNew, _ := json.Marshal(recNew)

	// Новее второй (index 1).
	idx, err := v.Select(key, [][]byte{dataOld, dataNew})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if idx != 1 {
		t.Errorf("Select: got index %d, want 1 (newer record)", idx)
	}

	// Порядок обратный — новее первый (index 0).
	idx, err = v.Select(key, [][]byte{dataNew, dataOld})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if idx != 0 {
		t.Errorf("Select: got index %d, want 0", idx)
	}

	// Один элемент.
	idx, err = v.Select(key, [][]byte{dataNew})
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if idx != 0 {
		t.Errorf("Select single: got index %d", idx)
	}
}

// TestDHTRecordValidator_Validate проверяет валидацию записи.
func TestDHTRecordValidator_Validate(t *testing.T) {
	v := DHTRecordValidator()
	key := "/hypernet/name/x"

	validRec := &Record{Name: "x", Owner: "peer", Addrs: []string{"/ip4/127.0.0.1/tcp/1"}, TTL: 60, Timestamp: 1, Signature: "abc"}
	data, _ := json.Marshal(validRec)
	if err := v.Validate(key, data); err != nil {
		t.Errorf("Validate valid record: %v", err)
	}
	if err := v.Validate(key, []byte{}); err == nil {
		t.Error("Validate empty: expected error")
	}
	if err := v.Validate(key, []byte("not json")); err == nil {
		t.Error("Validate invalid JSON: expected error")
	}
	noSig := &Record{Name: "x", Owner: "p", Addrs: nil, TTL: 1, Timestamp: 1, Signature: ""}
	dataNoSig, _ := json.Marshal(noSig)
	if err := v.Validate(key, dataNoSig); err == nil {
		t.Error("Validate missing signature: expected error")
	}
}
