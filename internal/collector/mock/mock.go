package mock

import (
	"context"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"sync"
	"time"

	"etapo/internal/models"
)

// Profile represents simulated device consumption characteristics.
type Profile string

const (
	ProfileFridge      Profile = "fridge"
	ProfileTV          Profile = "tv"
	ProfileWorkstation Profile = "workstation"
	ProfileKettle      Profile = "kettle"
	ProfileDefault     Profile = "default"
)

// Driver simulates a Tapo P115 socket with realistic power fluctuations.
type Driver struct {
	mu          sync.Mutex
	id          string
	name        string
	model       string
	profile     Profile
	isOn        bool
	lastPoll    time.Time
	energyToday float64 // kWh
	energyMonth float64 // kWh
	rng         *rand.Rand
	cycleTick   int64
}

// NewDriver creates a mock driver for testing without physical sockets.
func NewDriver(id, name, model string, profile Profile) *Driver {
	r := rand.New(rand.NewSource(time.Now().UnixNano() + int64(len(id))))

	// Initialize with realistic baseline daily energy consumption
	baseToday := 0.2 + r.Float64()*0.8
	baseMonth := baseToday * (1.0 + float64(time.Now().Day()))

	return &Driver{
		id:          id,
		name:        name,
		model:       model,
		profile:     profile,
		isOn:        true,
		lastPoll:    time.Now(),
		energyToday: baseToday,
		energyMonth: baseMonth,
		rng:         r,
	}
}

// mac derives a stable MAC address from the plug ID, in the device's upper-case dash-separated format.
func (d *Driver) mac() string {
	h := fnv.New32a()
	h.Write([]byte(d.id))
	sum := h.Sum32()
	return fmt.Sprintf("60-15-6F-%02X-%02X-%02X", byte(sum>>16), byte(sum>>8), byte(sum))
}

func (d *Driver) GetInfo(ctx context.Context) (*models.DeviceInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	return &models.DeviceInfo{
		ID:       d.id,
		Nickname: d.name,
		Model:    d.model,
		MAC:      d.mac(),
		IsOn:     d.isOn,
		RSSI:     -55 - d.rng.Intn(15),
	}, nil
}

func (d *Driver) GetEnergyUsage(ctx context.Context) (*models.EnergyUsage, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	elapsedSeconds := now.Sub(d.lastPoll).Seconds()
	if elapsedSeconds <= 0 {
		elapsedSeconds = 1
	}
	// Like the device, the daily and monthly counters restart at local midnight and month start
	if now.YearDay() != d.lastPoll.YearDay() || now.Year() != d.lastPoll.Year() {
		d.energyToday = 0
	}
	if now.Month() != d.lastPoll.Month() || now.Year() != d.lastPoll.Year() {
		d.energyMonth = 0
	}
	d.lastPoll = now
	d.cycleTick++

	var watts float64
	// The P115 does not report voltage, so the driver reports the 230 V mains estimate
	voltage := 230.0

	if !d.isOn {
		// Standby / vampire power draw when turned off
		switch d.profile {
		case ProfileTV:
			watts = 1.4 + d.rng.Float64()*0.3 // Standby LED, smart TV Wi-Fi chip
		case ProfileWorkstation:
			watts = 4.2 + d.rng.Float64()*0.8 // Docking station & sleeping monitors
		case ProfileFridge:
			watts = 0.5 + d.rng.Float64()*0.2
		case ProfileKettle:
			watts = 0.3 + d.rng.Float64()*0.1
		default:
			watts = 1.0 + d.rng.Float64()*0.4
		}
	} else {
		switch d.profile {
		case ProfileFridge:
			// Compressor cycles every ~20 ticks
			if (d.cycleTick % 25) < 10 {
				watts = 85.0 + d.rng.Float64()*25.0 // Compressor running
			} else {
				watts = 4.5 + d.rng.Float64()*2.0 // Idle
			}

		case ProfileTV:
			// ~80 - 120W with video scene brightness fluctuations
			watts = 95.0 + math.Sin(float64(d.cycleTick)/5.0)*15.0 + d.rng.Float64()*10.0

		case ProfileWorkstation:
			// ~70W idle, bursts to 180W
			watts = 75.0 + math.Abs(math.Sin(float64(d.cycleTick)/8.0))*60.0 + d.rng.Float64()*20.0

		case ProfileKettle:
			// Occasional boil spike
			if (d.cycleTick % 40) >= 36 {
				watts = 2100.0 + d.rng.Float64()*100.0
			} else {
				watts = 0.0
			}

		default:
			watts = 40.0 + d.rng.Float64()*15.0
		}
	}

	// Calculate current in mA: P = V * I -> I = (P / V) * 1000
	currentMA := (watts / voltage) * 1000.0

	// Accumulate kWh: kWh += (Watts * hours) / 1000
	kwhAdded := (watts * (elapsedSeconds / 3600.0)) / 1000.0
	d.energyToday += kwhAdded
	d.energyMonth += kwhAdded

	return &models.EnergyUsage{
		PowerWatts:     math.Round(watts*10) / 10,
		VoltageV:       math.Round(voltage*10) / 10,
		CurrentMA:      math.Round(currentMA*10) / 10,
		EnergyTodayKWh: math.Round(d.energyToday*1000) / 1000,
		EnergyMonthKWh: math.Round(d.energyMonth*100) / 100,
	}, nil
}

func (d *Driver) SetPower(ctx context.Context, on bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.isOn = on
	return nil
}
