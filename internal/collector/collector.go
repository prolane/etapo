package collector

import (
	"context"
	"fmt"
	"log"
	"net/netip"
	"sync"
	"time"

	"etapo/internal/collector/mock"
	"etapo/internal/collector/tapo"
	"etapo/internal/config"
	"etapo/internal/models"
	"etapo/internal/storage"
)

const (
	// unreachableAfter is how many polls in a row must fail before a plug is searched for on the network.
	unreachableAfter = 3
	// locateInterval is the shortest time between two network searches.
	locateInterval = time.Minute
	// locateWait is how long a network search collects replies.
	locateWait = 2 * time.Second
)

// Broadcaster is an interface for pushing real-time events to connected API clients.
type Broadcaster interface {
	Broadcast(event string, data interface{})
}

// addressed is implemented by drivers that reach a device at a network address, so the device can be found again after the address changes.
type addressed interface {
	IP() string
	Relocate(ip string)
}

// scanFunc finds devices by MAC address among the given hosts. It is tapo.Scan, replaceable in tests.
type scanFunc func(ctx context.Context, hosts []netip.Addr, port int, wait time.Duration) (map[string]netip.Addr, error)

type managedPlug struct {
	config config.PlugConfig
	driver DeviceDriver

	mu       sync.Mutex // guards failures and mac
	failures int        // Polls in a row that failed
	mac      string     // MAC address reported by the plug; empty until the first successful poll
}

// address returns the network address the plug is reached at now, which can differ from the configured one.
func (mp *managedPlug) address() string {
	if a, ok := mp.driver.(addressed); ok {
		return a.IP()
	}
	return mp.config.IP
}

func (mp *managedPlug) noteFailure() {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	mp.failures++
}

func (mp *managedPlug) clearFailures() {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	mp.failures = 0
}

func (mp *managedPlug) unreachable() bool {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	return mp.failures >= unreachableAfter
}

func (mp *managedPlug) knownMAC() string {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	return mp.mac
}

// setMAC records mac and reports whether it differs from the one known before.
func (mp *managedPlug) setMAC(mac string) bool {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if mac == mp.mac {
		return false
	}
	mp.mac = mac
	return true
}

// Collector manages polling loops for all configured plugs.
type Collector struct {
	cfg         *config.Config
	storage     *storage.Storage
	broadcaster Broadcaster
	scan        scanFunc
	lastScan    time.Time
	plugsMu     sync.RWMutex
	plugs       map[string]*managedPlug
	stopChan    chan struct{}
	doneChan    chan struct{}
}

// New creates a new Collector instance and initializes drivers.
func New(cfg *config.Config, store *storage.Storage, b Broadcaster) (*Collector, error) {
	c := &Collector{
		cfg:         cfg,
		storage:     store,
		broadcaster: b,
		scan:        tapo.Scan,
		plugs:       make(map[string]*managedPlug),
		stopChan:    make(chan struct{}),
		doneChan:    make(chan struct{}),
	}

	macs, err := store.PlugMACs(context.Background())
	if err != nil {
		return nil, fmt.Errorf("failed to load plug MAC addresses: %w", err)
	}

	for _, pc := range cfg.Plugs {
		var driver DeviceDriver

		switch pc.Type {
		case "tapo":
			username := pc.Username
			if username == "" {
				username = cfg.TapoAuth.Username
			}
			password := pc.Password
			if password == "" {
				password = cfg.TapoAuth.Password
			}
			driver = tapo.NewDriver(pc.IP, macs[pc.ID], username, password)

		case "mock", "":
			profile := mock.Profile(pc.MockProfile)
			if profile == "" {
				profile = mock.ProfileDefault
			}
			driver = mock.NewDriver(pc.ID, pc.Name, pc.Model, profile)

		default:
			log.Printf("[Collector] Unknown driver type %q for plug %s, falling back to mock", pc.Type, pc.ID)
			driver = mock.NewDriver(pc.ID, pc.Name, pc.Model, mock.ProfileDefault)
		}

		c.plugs[pc.ID] = &managedPlug{
			config: pc,
			driver: driver,
			mac:    macs[pc.ID],
		}

		// Pre-register plug in storage
		p := models.Plug{
			ID:         pc.ID,
			Name:       pc.Name,
			Model:      pc.Model,
			Category:   pc.Category,
			DriverType: pc.Type,
			IP:         pc.IP,
			IsActive:   true,
			IsOn:       true,
		}
		if p.Category == "" {
			p.Category = "General"
		}
		if p.DriverType == "" {
			p.DriverType = "mock"
		}
		if p.Model == "" {
			p.Model = "P115"
		}
		_ = store.UpsertPlug(context.Background(), p)
	}

	return c, nil
}

// Start begins polling all plugs on the configured interval.
func (c *Collector) Start(ctx context.Context) {
	pollInterval := c.cfg.PollDuration()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	defer close(c.doneChan)

	log.Printf("[Collector] Started energy poller for %d sockets (interval: %v)", len(c.plugs), pollInterval)

	// Run initial collection immediately
	c.pollAll(ctx)
	c.locateUnreachable(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Println("[Collector] Shutting down poller...")
			return
		case <-c.stopChan:
			log.Println("[Collector] Stopped poller...")
			return
		case <-ticker.C:
			c.pollAll(ctx)
			c.locateUnreachable(ctx)
		}
	}
}

// Stop gracefully stops the collector.
func (c *Collector) Stop() {
	close(c.stopChan)
	<-c.doneChan
}

// SetPower toggles the relay state of a specific socket.
func (c *Collector) SetPower(ctx context.Context, plugID string, on bool) error {
	c.plugsMu.RLock()
	mp, exists := c.plugs[plugID]
	c.plugsMu.RUnlock()

	if !exists {
		return fmt.Errorf("plug %q not found", plugID)
	}

	if err := mp.driver.SetPower(ctx, on); err != nil {
		return fmt.Errorf("failed to toggle plug: %w", err)
	}

	_ = c.storage.UpdatePlugState(ctx, plugID, on)

	// Poll immediately to update state
	go c.pollDevice(context.Background(), mp)

	return nil
}

func (c *Collector) pollAll(ctx context.Context) {
	c.plugsMu.RLock()
	defer c.plugsMu.RUnlock()

	var wg sync.WaitGroup
	for _, mp := range c.plugs {
		wg.Add(1)
		go func(p *managedPlug) {
			defer wg.Done()
			c.pollDevice(ctx, p)
		}(mp)
	}
	wg.Wait()
}

func (c *Collector) pollDevice(ctx context.Context, mp *managedPlug) {
	reqCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()

	// 1. Get Device Info (relay state, signal)
	info, err := mp.driver.GetInfo(reqCtx)
	if err != nil {
		mp.noteFailure()
		log.Printf("[Collector] Failed to get info for %s (%s): %v", mp.config.ID, mp.address(), err)
		return
	}

	_ = c.storage.UpdatePlugState(ctx, mp.config.ID, info.IsOn)
	c.rememberMAC(ctx, mp, info.MAC)

	// 2. Get Energy Telemetry
	usage, err := mp.driver.GetEnergyUsage(reqCtx)
	if err != nil {
		mp.noteFailure()
		log.Printf("[Collector] Failed to get energy for %s: %v", mp.config.ID, err)
		return
	}
	mp.clearFailures()

	reading := models.EnergyReading{
		PlugID:         mp.config.ID,
		Timestamp:      time.Now().UTC(),
		PowerWatts:     usage.PowerWatts,
		VoltageV:       usage.VoltageV,
		CurrentMA:      usage.CurrentMA,
		EnergyTodayKWh: usage.EnergyTodayKWh,
		EnergyMonthKWh: usage.EnergyMonthKWh,
	}

	// Record to storage write-buffer (flushed periodically to minimize SD card wear)
	c.storage.RecordReading(reading)

	// Push real-time event to SSE clients
	if c.broadcaster != nil {
		c.broadcaster.Broadcast("reading", reading)
	}
}

// rememberMAC stores the MAC address a plug reports, so the plug can be found again after its address changes.
func (c *Collector) rememberMAC(ctx context.Context, mp *managedPlug, mac string) {
	if mac == "" || !mp.setMAC(mac) {
		return
	}
	if err := c.storage.SetPlugMAC(ctx, mp.config.ID, mac); err != nil {
		log.Printf("[Collector] Failed to store MAC address for %s: %v", mp.config.ID, err)
	}
}

// locateUnreachable searches for plugs that have failed several polls in a row. The search covers the /24 network
// of each plug's last known address, and a plug that answers from a different address is moved there.
func (c *Collector) locateUnreachable(ctx context.Context) {
	missing := c.unreachablePlugs()
	if len(missing) == 0 || time.Since(c.lastScan) < locateInterval {
		return
	}
	c.lastScan = time.Now()

	var hosts []netip.Addr
	seen := make(map[netip.Addr]bool)
	var searchable []*managedPlug
	for _, mp := range missing {
		if mp.knownMAC() == "" {
			log.Printf("[Collector] Cannot search for %s: its MAC address is not known yet", mp.config.ID)
			continue
		}
		ip, err := netip.ParseAddr(mp.address())
		if err != nil || !ip.Is4() {
			continue
		}
		searchable = append(searchable, mp)
		for _, host := range subnetHosts(ip) {
			if !seen[host] {
				seen[host] = true
				hosts = append(hosts, host)
			}
		}
	}
	if len(searchable) == 0 {
		return
	}

	found, err := c.scan(ctx, hosts, tapo.DiscoveryPort, locateWait)
	if err != nil {
		log.Printf("[Collector] Network search failed: %v", err)
		return
	}
	for _, mp := range searchable {
		newIP, ok := found[mp.knownMAC()]
		if !ok || newIP.String() == mp.address() {
			continue
		}
		c.moveTo(ctx, mp, newIP.String())
	}
}

// unreachablePlugs returns the plugs that have failed enough polls in a row to be searched for.
func (c *Collector) unreachablePlugs() []*managedPlug {
	c.plugsMu.RLock()
	defer c.plugsMu.RUnlock()

	var out []*managedPlug
	for _, mp := range c.plugs {
		if _, ok := mp.driver.(addressed); ok && mp.unreachable() {
			out = append(out, mp)
		}
	}
	return out
}

// moveTo points a plug at the address it was found at and records the change.
func (c *Collector) moveTo(ctx context.Context, mp *managedPlug, ip string) {
	addr, ok := mp.driver.(addressed)
	if !ok {
		return
	}
	old := addr.IP()
	addr.Relocate(ip)
	mp.clearFailures()

	if err := c.storage.UpdatePlugIP(ctx, mp.config.ID, ip); err != nil {
		log.Printf("[Collector] Failed to store new address for %s: %v", mp.config.ID, err)
	}
	log.Printf("[Collector] Plug %s moved from %s to %s", mp.config.ID, old, ip)

	if c.broadcaster != nil {
		c.broadcaster.Broadcast("plug_moved", map[string]string{"plug_id": mp.config.ID, "ip": ip})
	}
}

// subnetHosts returns the host addresses of the /24 network that contains ip.
func subnetHosts(ip netip.Addr) []netip.Addr {
	base := ip.As4()
	hosts := make([]netip.Addr, 0, 254)
	for last := 1; last < 255; last++ {
		hosts = append(hosts, netip.AddrFrom4([4]byte{base[0], base[1], base[2], byte(last)}))
	}
	return hosts
}
