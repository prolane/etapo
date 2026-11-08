package collector

import (
	"context"
	"errors"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"etapo/internal/collector/tapo"
	"etapo/internal/config"
	"etapo/internal/models"
	"etapo/internal/storage"
)

const kettleMAC = "60-15-6F-00-00-01"

// fakeTapo is a tapo-like driver whose address can change and which can stop answering.
type fakeTapo struct {
	mu   sync.Mutex
	ip   string
	mac  string
	down bool
}

func (f *fakeTapo) GetInfo(ctx context.Context) (*models.DeviceInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("no answer")
	}
	return &models.DeviceInfo{MAC: f.mac, IsOn: true}, nil
}

func (f *fakeTapo) GetEnergyUsage(ctx context.Context) (*models.EnergyUsage, error) {
	return &models.EnergyUsage{PowerWatts: 5}, nil
}

func (f *fakeTapo) SetPower(ctx context.Context, on bool) error {
	return nil
}

func (f *fakeTapo) IP() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ip
}

// Relocate models the plug answering again at its new address.
func (f *fakeTapo) Relocate(ip string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ip = ip
	f.down = false
}

func (f *fakeTapo) setDown(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

type eventRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *eventRecorder) Broadcast(event string, data interface{}) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *eventRecorder) count(event string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e == event {
			n++
		}
	}
	return n
}

type scanCall struct {
	hosts []netip.Addr
	port  int
}

// newTestCollector returns a collector with one plug whose driver is replaced by a fake.
func newTestCollector(t *testing.T) (*Collector, *storage.Storage, *fakeTapo, *eventRecorder) {
	t.Helper()
	store, err := storage.New(filepath.Join(t.TempDir(), "etapo.db"), time.Second)
	if err != nil {
		t.Fatalf("failed to init storage: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	cfg := config.DefaultConfig()
	cfg.Plugs = []config.PlugConfig{{ID: "kettle", Name: "Kettle", Type: "tapo", IP: "192.168.1.103"}}
	rec := &eventRecorder{}
	c, err := New(cfg, store, rec)
	if err != nil {
		t.Fatalf("failed to create collector: %v", err)
	}

	fake := &fakeTapo{ip: "192.168.1.103", mac: kettleMAC}
	c.plugs["kettle"].driver = fake
	return c, store, fake, rec
}

func TestUnreachablePlugIsFoundAtItsNewAddress(t *testing.T) {
	c, store, fake, rec := newTestCollector(t)
	ctx := context.Background()
	mp := c.plugs["kettle"]

	c.pollDevice(ctx, mp)
	if mp.knownMAC() != kettleMAC {
		t.Fatalf("expected the MAC to be learned from the first poll, got %q", mp.knownMAC())
	}
	macs, err := store.PlugMACs(ctx)
	if err != nil || macs["kettle"] != kettleMAC {
		t.Fatalf("expected the MAC to be stored, got %v (err %v)", macs, err)
	}

	// The plug moves: the old address stops answering and the search finds it at a new one
	fake.setDown(true)
	for i := 0; i < unreachableAfter; i++ {
		c.pollDevice(ctx, mp)
	}
	if !mp.unreachable() {
		t.Fatal("expected the plug to count as unreachable after failed polls")
	}

	var calls []scanCall
	c.scan = func(ctx context.Context, hosts []netip.Addr, port int, wait time.Duration) (map[string]netip.Addr, error) {
		calls = append(calls, scanCall{hosts: hosts, port: port})
		return map[string]netip.Addr{kettleMAC: netip.MustParseAddr("192.168.1.120")}, nil
	}
	c.locateUnreachable(ctx)

	if len(calls) != 1 {
		t.Fatalf("expected one network search, got %d", len(calls))
	}
	if calls[0].port != tapo.DiscoveryPort {
		t.Errorf("expected the search on port %d, got %d", tapo.DiscoveryPort, calls[0].port)
	}
	if len(calls[0].hosts) != 254 {
		t.Errorf("expected the 254 hosts of the /24 network, got %d", len(calls[0].hosts))
	}
	if !containsAddr(calls[0].hosts, "192.168.1.120") {
		t.Error("expected the search to cover the new address")
	}

	if fake.IP() != "192.168.1.120" {
		t.Errorf("expected the driver at 192.168.1.120, got %s", fake.IP())
	}
	if mp.unreachable() {
		t.Error("expected the failures to be cleared after the move")
	}
	ip, _, _ := store.PlugIP(ctx, "kettle")
	if ip != "192.168.1.120" {
		t.Errorf("expected the stored address to be updated, got %s", ip)
	}
	if rec.count("plug_moved") != 1 {
		t.Errorf("expected one plug_moved event, got %d", rec.count("plug_moved"))
	}
}

func TestNetworkSearchIsThrottled(t *testing.T) {
	c, _, fake, _ := newTestCollector(t)
	ctx := context.Background()
	mp := c.plugs["kettle"]
	c.pollDevice(ctx, mp)

	searches := 0
	c.scan = func(ctx context.Context, hosts []netip.Addr, port int, wait time.Duration) (map[string]netip.Addr, error) {
		searches++
		return map[string]netip.Addr{}, nil
	}

	fake.setDown(true)
	for i := 0; i < unreachableAfter; i++ {
		c.pollDevice(ctx, mp)
	}
	c.locateUnreachable(ctx)
	for i := 0; i < unreachableAfter; i++ {
		c.pollDevice(ctx, mp)
	}
	c.locateUnreachable(ctx)
	if searches != 1 {
		t.Errorf("expected the second search to wait for the interval, got %d searches", searches)
	}

	c.lastScan = time.Now().Add(-2 * locateInterval)
	c.locateUnreachable(ctx)
	if searches != 2 {
		t.Errorf("expected a search after the interval, got %d searches", searches)
	}
}

func TestPlugAnsweringAtItsAddressIsNotMoved(t *testing.T) {
	c, _, fake, rec := newTestCollector(t)
	ctx := context.Background()
	mp := c.plugs["kettle"]
	c.pollDevice(ctx, mp)

	fake.setDown(true)
	for i := 0; i < unreachableAfter; i++ {
		c.pollDevice(ctx, mp)
	}
	c.scan = func(ctx context.Context, hosts []netip.Addr, port int, wait time.Duration) (map[string]netip.Addr, error) {
		return map[string]netip.Addr{kettleMAC: netip.MustParseAddr("192.168.1.103")}, nil
	}
	c.locateUnreachable(ctx)

	if fake.IP() != "192.168.1.103" {
		t.Errorf("expected the address to stay 192.168.1.103, got %s", fake.IP())
	}
	if rec.count("plug_moved") != 0 {
		t.Error("expected no plug_moved event when the address did not change")
	}
}

func TestPlugWithoutKnownMACIsNotSearched(t *testing.T) {
	c, _, fake, _ := newTestCollector(t)
	ctx := context.Background()
	mp := c.plugs["kettle"]

	fake.setDown(true)
	for i := 0; i < unreachableAfter; i++ {
		c.pollDevice(ctx, mp)
	}

	searches := 0
	c.scan = func(ctx context.Context, hosts []netip.Addr, port int, wait time.Duration) (map[string]netip.Addr, error) {
		searches++
		return nil, nil
	}
	c.locateUnreachable(ctx)
	if searches != 0 {
		t.Errorf("expected no search without a known MAC address, got %d", searches)
	}
}

func containsAddr(addrs []netip.Addr, want string) bool {
	target := netip.MustParseAddr(want)
	for _, a := range addrs {
		if a == target {
			return true
		}
	}
	return false
}
