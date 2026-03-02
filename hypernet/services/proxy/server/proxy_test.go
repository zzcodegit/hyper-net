package proxy

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestParseRequestOK(t *testing.T) {
	req, err := ParseRequest("TCP example.com 80\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Proto != "TCP" || req.Host != "example.com" || req.Port != 80 {
		t.Fatalf("unexpected request: %+v", req)
	}
}

func TestParseRequestUDP(t *testing.T) {
	req, err := ParseRequest("UDP 8.8.8.8 53\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Proto != "UDP" || req.Host != "8.8.8.8" || req.Port != 53 {
		t.Fatalf("unexpected UDP request: %+v", req)
	}
}

func TestParseRequestDIRECT(t *testing.T) {
	req, err := ParseRequest("DIRECT example.com 443\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.Proto != "DIRECT" || req.Host != "example.com" || req.Port != 443 {
		t.Fatalf("unexpected DIRECT request: %+v", req)
	}
}

func TestFormatParseObfsLine(t *testing.T) {
	p := ObfsParams{PaddingMin: 0, PaddingMax: 100, WriteDelayMin: 0, WriteDelayMax: 10 * time.Millisecond}
	line := FormatObfsLine(p)
	if !strings.HasPrefix(line, "OBFS ") || !strings.HasSuffix(line, "\n") {
		t.Fatalf("unexpected format: %q", line)
	}
	parsed, ok := ParseObfsLine(line)
	if !ok {
		t.Fatalf("ParseObfsLine failed for %q", line)
	}
	if parsed.PaddingMin != p.PaddingMin || parsed.PaddingMax != p.PaddingMax ||
		parsed.WriteDelayMin != p.WriteDelayMin || parsed.WriteDelayMax != p.WriteDelayMax {
		t.Fatalf("roundtrip: got %+v", parsed)
	}
	// Невалидные строки
	for _, bad := range []string{"", "OBFS", "OBFS 1 2", "OBFS 1 0 0 0", "OBFS -1 0 0 0"} {
		if _, ok := ParseObfsLine(bad); ok {
			t.Fatalf("expected ParseObfsLine to fail for %q", bad)
		}
	}
}

func TestAgreeObfs(t *testing.T) {
	srv := NewServer()
	srv.SetObfuscation(0, 80, 0, 5*time.Millisecond)
	client := ObfsParams{PaddingMin: 0, PaddingMax: 200, WriteDelayMin: 0, WriteDelayMax: 20 * time.Millisecond}
	agreed := srv.AgreeObfs(client)
	if agreed.PaddingMax != 80 || agreed.WriteDelayMax != 5*time.Millisecond {
		t.Fatalf("expected server to clamp: got %+v", agreed)
	}
	srv2 := NewServer()
	agreed2 := srv2.AgreeObfs(client)
	if agreed2.PaddingMax != 0 || agreed2.WriteDelayMax != 0 {
		t.Fatalf("expected no obfs when server has none: got %+v", agreed2)
	}
}

// Граничный случай: клиент без обфускации (все нули) — сервер возвращает нули.
func TestAgreeObfs_ClientZero_ReturnsZero(t *testing.T) {
	srv := NewServer()
	srv.SetObfuscation(10, 100, time.Millisecond, 10*time.Millisecond)
	client := ObfsParams{PaddingMin: 0, PaddingMax: 0, WriteDelayMin: 0, WriteDelayMax: 0}
	agreed := srv.AgreeObfs(client)
	if agreed.PaddingMax != 0 || agreed.WriteDelayMax != 0 {
		t.Errorf("client zero obfs: server should return zero; got %+v", agreed)
	}
}

func TestParseRequestErrors(t *testing.T) {
	cases := []string{
		"",
		"TCP only-two-fields",
		"XYZ host 80",
		"TCP  0",
		"TCP host 70000",
	}
	for _, c := range cases {
		if _, err := ParseRequest(c); err == nil {
			t.Fatalf("expected error for %q", c)
		}
	}
}

// TestParseRequest_ErrorWrapsInvalidRequest проверяет, что все ошибки ParseRequest обёрнуты в errInvalidRequest.
func TestParseRequest_ErrorWrapsInvalidRequest(t *testing.T) {
	cases := []string{"", "TCP a", "XYZ host 80", "TCP host 0", "TCP host 99999"}
	for _, c := range cases {
		_, err := ParseRequest(c)
		if err == nil {
			continue
		}
		if !errors.Is(err, errInvalidRequest) {
			t.Errorf("ParseRequest(%q): err should wrap errInvalidRequest: %v", c, err)
		}
	}
}

func TestProtocolID_NonEmpty(t *testing.T) {
	if ProtocolID == "" {
		t.Error("ProtocolID must be non-empty")
	}
	if !strings.HasPrefix(ProtocolID, "/") {
		t.Errorf("ProtocolID should look like a protocol path: %q", ProtocolID)
	}
}

func TestFormatObfsLine_ZeroValuesRoundtrip(t *testing.T) {
	p := ObfsParams{PaddingMin: 0, PaddingMax: 0, WriteDelayMin: 0, WriteDelayMax: 0}
	line := FormatObfsLine(p)
	parsed, ok := ParseObfsLine(line)
	if !ok {
		t.Fatalf("ParseObfsLine(%q) failed", line)
	}
	if parsed.PaddingMax != 0 || parsed.WriteDelayMax != 0 {
		t.Errorf("roundtrip zero: got %+v", parsed)
	}
}

// Граничные случаи и ошибки ParseRequest: порт 0, порт 65536, только пробелы, лишние пробелы.
func TestParseRequest_EdgeCasesAndErrors(t *testing.T) {
	// Порт 0 невалиден
	_, err := ParseRequest("TCP host 0")
	if err == nil {
		t.Fatal("port 0 should fail")
	}
	if !errors.Is(err, errInvalidRequest) {
		t.Errorf("port 0: %v", err)
	}
	// Порт 65536 невалиден
	_, err = ParseRequest("TCP host 65536")
	if err == nil {
		t.Fatal("port 65536 should fail")
	}
	// Только пробелы — пустая строка после TrimSpace
	_, err = ParseRequest("   ")
	if err == nil {
		t.Fatal("spaces only should fail")
	}
	// Лишние пробелы между полями — всё равно 3 поля
	req, err := ParseRequest("  TCP   example.com   80  ")
	if err != nil {
		t.Fatalf("leading/trailing spaces: %v", err)
	}
	if req.Proto != "TCP" || req.Host != "example.com" || req.Port != 80 {
		t.Errorf("got %+v", req)
	}
	// Один токен — недостаточно полей
	_, err = ParseRequest("TCP")
	if err == nil {
		t.Fatal("one field should fail")
	}
	// Четыре поля
	_, err = ParseRequest("TCP host 80 extra")
	if err == nil {
		t.Fatal("four fields should fail")
	}
}

// Проверка формата ошибки для клиента: "ERR "+err.Error()+"\n" — без паники и в разумной длине.
func TestParseRequest_ErrMessageForWire(t *testing.T) {
	inputs := []string{"", "X", "TCP a", "TCP host 0", "TCP host 99999"}
	for _, in := range inputs {
		_, err := ParseRequest(in)
		if err == nil {
			continue
		}
		wire := "ERR " + err.Error() + "\n"
		if len(wire) > 300 {
			t.Errorf("wire message too long for %q: %d bytes", in, len(wire))
		}
		if wire[:4] != "ERR " {
			t.Errorf("wire should start with ERR : %q", wire)
		}
	}
}

func TestPipeBidirectional(t *testing.T) {
	// Поведение pipeBidirectional детально проверяется интеграционными тестами
	// в pkg/client (ProxyTCPIntegration, ProxyTCPFileSizes, ProxyParallelConnections).
	// Здесь оставляем заглушку, чтобы не плодить нестабильных гонок в unit-тесте.
	t.Skip("pipeBidirectional covered by higher-level proxy integration tests")
}

// fakeStream оборачивает net.Conn, чтобы удовлетворить интерфейсу network.Stream,
// минимально необходимому для pipeBidirectional (нужны Close и Write/Read).
type fakeStream struct {
	net.Conn
}

func TestDialTCPAndUDP(t *testing.T) {
	// TCP: успешное подключение к локальному серверу и ошибка для несуществующего порта.
	tcpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp listen: %v", err)
	}
	defer tcpLn.Close()

	tcpDone := make(chan struct{})
	go func() {
		defer close(tcpDone)
		conn, err := tcpLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	host, portStr, _ := net.SplitHostPort(tcpLn.Addr().String())
	port, _ := strconv.Atoi(portStr)
	if _, err := dialTCP(ctx, host, port); err != nil {
		t.Fatalf("dialTCP to local listener: %v", err)
	}
	<-tcpDone

	// Ошибка при подключении к заведомо несуществующему порту.
	if _, err := dialTCP(ctx, "127.0.0.1", 65000); err == nil {
		t.Fatalf("expected error dialing unused port")
	}

	// UDP: успешный ping к локальному echo-серверу.
	udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("resolve udp addr: %v", err)
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer udpConn.Close()

	udpDone := make(chan struct{})
	go func() {
		defer close(udpDone)
		buf := make([]byte, 64)
		n, addr, err := udpConn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		_, _ = udpConn.WriteToUDP(buf[:n], addr)
	}()

	hostU, portUStr, _ := net.SplitHostPort(udpConn.LocalAddr().String())
	portU, _ := strconv.Atoi(portUStr)
	uconn, err := dialUDP(ctx, hostU, portU)
	if err != nil {
		t.Fatalf("dialUDP: %v", err)
	}
	defer uconn.Close()

	if _, err := uconn.Write([]byte("hi")); err != nil {
		t.Fatalf("udp write: %v", err)
	}
	buf := make([]byte, 64)
	if _, err := uconn.Read(buf); err != nil {
		t.Fatalf("udp read: %v", err)
	}
	<-udpDone
}

