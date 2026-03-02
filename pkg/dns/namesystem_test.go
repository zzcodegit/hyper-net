//go:build integration

package dns

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	dht "github.com/libp2p/go-libp2p-kad-dht"
)

// setupTwoHostsWithDHT создаёт два libp2p-хоста с DHT и валидатором для /hypernet/name/, соединяет их.
func setupTwoHostsWithDHT(t *testing.T, ctx context.Context) (h1, h2 host.Host, dht1, dht2 *dht.IpfsDHT) {
	t.Helper()
	// Используем свой протокольный префикс, чтобы не требовать /pk и /ipns; ключи вида /hypernet/name/<name> → namespace "hypernet".
	dhtOpts := []dht.Option{
		dht.ProtocolPrefix("/hypernet"),
		dht.Mode(dht.ModeServer),
		dht.NamespacedValidator("hypernet", DHTRecordValidator()),
	}
	h1, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("libp2p.New(h1): %v", err)
	}
	dht1, err = dht.New(ctx, h1, dhtOpts...)
	if err != nil {
		h1.Close()
		t.Fatalf("dht.New(h1): %v", err)
	}
	h2, err = libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		dht1.Close()
		h1.Close()
		t.Fatalf("libp2p.New(h2): %v", err)
	}
	bootstrapInfo := peer.AddrInfo{ID: h1.ID(), Addrs: h1.Addrs()}
	dht2, err = dht.New(ctx, h2, dht.ProtocolPrefix("/hypernet"), dht.Mode(dht.ModeClient), dht.BootstrapPeers(bootstrapInfo), dht.NamespacedValidator("hypernet", DHTRecordValidator()))
	if err != nil {
		h2.Close()
		dht1.Close()
		h1.Close()
		t.Fatalf("dht.New(h2): %v", err)
	}
	if err := h2.Connect(ctx, bootstrapInfo); err != nil {
		dht2.Close()
		h2.Close()
		dht1.Close()
		h1.Close()
		t.Fatalf("Connect h2->h1: %v", err)
	}
	// Оба направления: h1 тоже подключается к h2, чтобы routing table заполнилась с обеих сторон.
	info2 := peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()}
	if err := h1.Connect(ctx, info2); err != nil {
		dht2.Close()
		h2.Close()
		dht1.Close()
		h1.Close()
		t.Fatalf("Connect h1->h2: %v", err)
	}
	_ = dht1.Bootstrap(ctx)
	_ = dht2.Bootstrap(ctx)
	// Даём время на асинхронное заполнение routing table после Bootstrap.
	time.Sleep(2 * time.Second)
	return h1, h2, dht1, dht2
}

// TestNameSystem_RegisterAndResolve проверяет работу с in-memory DHT: регистрация на одной ноде, разрешение на другой.
func TestNameSystem_RegisterAndResolve(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	h1, h2, dht1, dht2 := setupTwoHostsWithDHT(t, ctx)
	defer dht1.Close()
	defer dht2.Close()
	defer h1.Close()
	defer h2.Close()

	nsServer := NewNameSystem(dht1, h1)
	nsClient := NewNameSystem(dht2, h2)

	name := "exit-1"
	addrs := h1.Addrs()
	if len(addrs) == 0 {
		t.Fatal("server has no addrs")
	}
	var withP2P []multiaddr.Multiaddr
	for _, a := range addrs {
		full := a.String() + "/p2p/" + h1.ID().String()
		m, err := multiaddr.NewMultiaddr(full)
		if err != nil {
			continue
		}
		withP2P = append(withP2P, m)
	}
	if len(withP2P) == 0 {
		t.Fatal("no valid multiaddrs with p2p")
	}

	if err := nsServer.Register(ctx, name, withP2P, 3600*time.Second); err != nil {
		t.Fatalf("Register: %v", err)
	}

	resolveCtx := context.WithValue(ctx, CacheBypassKey, true)
	got, err := nsClient.Resolve(resolveCtx, name)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("Resolve returned no addrs")
	}
	found := false
	for _, a := range got {
		if a.String() == withP2P[0].String() {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Resolve addrs %v do not contain expected %s", got, withP2P[0].String())
	}
}

// TestNameSystem_ResolveSelectsLatestByTimestamp проверяет выбор записи с максимальным timestamp при нескольких значениях в DHT.
func TestNameSystem_ResolveSelectsLatestByTimestamp(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	h1, h2, dht1, dht2 := setupTwoHostsWithDHT(t, ctx)
	defer dht1.Close()
	defer dht2.Close()
	defer h1.Close()
	defer h2.Close()

	name := "sel"
	key := DHTKey(name)
	addrOld := "/ip4/192.168.1.1/tcp/4001"
	addrNew := "/ip4/10.0.0.2/tcp/4002"
	tsOld := int64(1000)
	tsNew := int64(2000)

	recOld := &Record{
		Name:      name,
		Owner:     h1.ID().String(),
		Addrs:     []string{addrOld},
		TTL:       3600,
		Timestamp: tsOld,
	}
	recNew := &Record{
		Name:      name,
		Owner:     h2.ID().String(),
		Addrs:     []string{addrNew},
		TTL:       3600,
		Timestamp: tsNew,
	}
	priv1 := h1.Peerstore().PrivKey(h1.ID())
	priv2 := h2.Peerstore().PrivKey(h2.ID())
	if priv1 == nil || priv2 == nil {
		t.Fatal("missing private keys")
	}
	if err := recOld.Sign(priv1); err != nil {
		t.Fatalf("sign old: %v", err)
	}
	if err := recNew.Sign(priv2); err != nil {
		t.Fatalf("sign new: %v", err)
	}
	dataOld, _ := json.Marshal(recOld)
	dataNew, _ := json.Marshal(recNew)
	if err := dht1.PutValue(ctx, key, dataOld); err != nil {
		t.Fatalf("PutValue old: %v", err)
	}
	if err := dht2.PutValue(ctx, key, dataNew); err != nil {
		t.Fatalf("PutValue new: %v", err)
	}

	nsResolver := NewNameSystem(dht2, h2)
	resolveCtx := context.WithValue(ctx, CacheBypassKey, true)
	addrs, err := nsResolver.Resolve(resolveCtx, name)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(addrs) == 0 {
		t.Fatal("Resolve returned no addrs")
	}
	if got := addrs[0].String(); got != addrNew {
		t.Errorf("Resolve selected by timestamp: got %q, want %q (newer record)", got, addrNew)
	}
}

// TestNameSystem_ResolveExpiredIgnored проверяет, что просроченная запись не возвращается.
func TestNameSystem_ResolveExpiredIgnored(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	h1, h2, dht1, dht2 := setupTwoHostsWithDHT(t, ctx)
	defer dht1.Close()
	defer dht2.Close()
	defer h1.Close()
	defer h2.Close()

	name := "expired"
	key := DHTKey(name)
	rec := &Record{
		Name:      name,
		Owner:     h1.ID().String(),
		Addrs:     []string{"/ip4/127.0.0.1/tcp/4001"},
		TTL:       1,
		Timestamp: time.Now().Unix() - 10,
	}
	priv := h1.Peerstore().PrivKey(h1.ID())
	if priv == nil {
		t.Fatal("no private key")
	}
	if err := rec.Sign(priv); err != nil {
		t.Fatalf("Sign: %v", err)
	}
	data, _ := json.Marshal(rec)
	if err := dht1.PutValue(ctx, key, data); err != nil {
		t.Fatalf("PutValue: %v", err)
	}

	nsClient := NewNameSystem(dht2, h2)
	resolveCtx := context.WithValue(ctx, CacheBypassKey, true)
	_, err := nsClient.Resolve(resolveCtx, name)
	if err == nil {
		t.Fatal("Resolve expected to fail for expired record")
	}
}
