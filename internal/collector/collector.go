package collector

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"etapo/internal/collector/mock"
	"etapo/internal/collector/tapo"
	"etapo/internal/config"
	"etapo/internal/models"
	"etapo/internal/storage"
)

// Broadcaster is an interface for pushing real-time events to connected API clients.
type Broadcaster interface {
	Broadcast(event string, data interface{})
}

type managedPlug struct {
	config config.PlugConfig
	driver DeviceDriver
}

// Collector manages polling loops for all configured plugs.
type Collector struct {
	cfg         *config.Config
	storage     *storage.Storage
	broadcaster Broadcaster
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
		plugs:       make(map[string]*managedPlug),
		stopChan:    make(chan struct{}),
		doneChan:    make(chan struct{}),
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
			driver = tapo.NewDriver(pc.IP, username, password)

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
		}

		// Pre-register plug in storage
		p := models.Plug{
			ID:         pc.ID,
			Name:       pc.Name,
			Model:      pc.Model,
			DriverType: pc.Type,
			IP:         pc.IP,
			IsActive:   true,
			IsOn:       true,
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
		log.Printf("[Collector] Failed to get info for %s (%s): %v", mp.config.ID, mp.config.IP, err)
		return
	}

	_ = c.storage.UpdatePlugState(ctx, mp.config.ID, info.IsOn)

	// 2. Get Energy Telemetry
	usage, err := mp.driver.GetEnergyUsage(reqCtx)
	if err != nil {
		log.Printf("[Collector] Failed to get energy for %s: %v", mp.config.ID, err)
		return
	}

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
