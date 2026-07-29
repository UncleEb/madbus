package main

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"madbus/internal/config"
	"madbus/internal/profile"
	"madbus/internal/rtu"
	"madbus/internal/telemetry"
)

// schedDevice is a device the poller reads on its own period.
type schedDevice struct {
	cfg     config.Device
	prof    *profile.Profile
	bus     *rtu.Bus // nil for mock devices
	mock    bool
	period  time.Duration
	nextDue time.Time // zero => due immediately
}

// busEntry tracks an open serial bus and the params it was opened with, so a
// changed serial config triggers a rebuild rather than a stale connection.
type busEntry struct {
	bus    *rtu.Bus
	params rtu.SerialParams
}

// Poller owns the live device set. config.json is the source of truth:
// reconcile diffs the running devices/buses against a config and adds, drops, or
// rebuilds to match; tick reads whichever devices are due. It is driven from a
// single loop and is not safe for concurrent use.
type Poller struct {
	store       *telemetry.Store
	profiles    map[string]*profile.Profile
	configPath  string
	buses       map[string]*busEntry    // by serial port
	devices     map[string]*schedDevice // by device id
	cfg         *config.Config
	lastModTime time.Time
}

func newPoller(store *telemetry.Store, profiles map[string]*profile.Profile, configPath string, cfg *config.Config) *Poller {
	p := &Poller{
		store:      store,
		profiles:   profiles,
		configPath: configPath,
		buses:      make(map[string]*busEntry),
		devices:    make(map[string]*schedDevice),
	}
	if fi, err := os.Stat(configPath); err == nil {
		p.lastModTime = fi.ModTime()
	}
	p.reconcile(cfg)
	return p
}

func (p *Poller) deviceCount() int { return len(p.devices) }

// reconcile makes the running device/bus set match cfg.
func (p *Poller) reconcile(cfg *config.Config) {
	p.cfg = cfg
	defaultPeriod := time.Duration(cfg.PollIntervalSeconds) * time.Second
	if defaultPeriod <= 0 {
		defaultPeriod = 5 * time.Second
	}

	// Drop scheduled devices no longer in config.
	desired := make(map[string]bool, len(cfg.Devices))
	for _, d := range cfg.Devices {
		desired[d.ID] = true
	}
	for id := range p.devices {
		if !desired[id] {
			delete(p.devices, id)
			p.store.Remove(id)
			slog.Info("device removed", "device", id)
		}
	}

	// Desired serial buses: one per port (mock excluded). Devices sharing a port
	// share a bus; the first device's serial params define it.
	desiredBus := make(map[string]rtu.SerialParams)
	for _, d := range cfg.Devices {
		if strings.EqualFold(d.Serial.Port, "mock") {
			continue
		}
		if _, seen := desiredBus[d.Serial.Port]; seen {
			continue
		}
		parity, err := rtu.ParseParity(d.Serial.Parity)
		if err != nil {
			continue // surfaced per-device below
		}
		desiredBus[d.Serial.Port] = rtu.SerialParams{
			Port:     d.Serial.Port,
			Baud:     d.Serial.Baud,
			DataBits: d.Serial.DataBits,
			Parity:   parity,
			StopBits: d.Serial.StopBits,
			Timeout:  time.Second,
		}
	}
	// Close buses that are gone or whose params changed; open new ones.
	for port, entry := range p.buses {
		if want, ok := desiredBus[port]; !ok || want != entry.params {
			entry.bus.Close()
			delete(p.buses, port)
		}
	}
	for port, params := range desiredBus {
		if _, ok := p.buses[port]; !ok {
			p.buses[port] = &busEntry{bus: rtu.NewBus(params), params: params}
		}
	}

	// Add or update each device.
	for _, dc := range cfg.Devices {
		prof, ok := p.profiles[dc.Profile]
		if !ok {
			// Not pollable, but keep it visible as offline with a clear reason.
			delete(p.devices, dc.ID)
			p.store.Register(dc.ID, dc.Name, dc.Profile, "")
			p.store.RecordFailure(dc.ID, "unknown profile: "+dc.Profile)
			slog.Warn("device references unknown profile; not polling", "device", dc.ID, "profile", dc.Profile)
			continue
		}

		period := defaultPeriod
		if dc.PollIntervalSeconds > 0 {
			period = time.Duration(dc.PollIntervalSeconds) * time.Second
		}
		p.store.Register(dc.ID, dc.Name, dc.Profile, string(prof.Category))

		mock := strings.EqualFold(dc.Serial.Port, "mock")
		var bus *rtu.Bus
		if !mock {
			if e := p.buses[dc.Serial.Port]; e != nil {
				bus = e.bus
			}
		}

		if sd := p.devices[dc.ID]; sd != nil {
			sd.cfg, sd.prof, sd.bus, sd.mock, sd.period = dc, prof, bus, mock, period
		} else {
			p.devices[dc.ID] = &schedDevice{cfg: dc, prof: prof, bus: bus, mock: mock, period: period}
			slog.Info("device configured", "device", dc.ID, "profile", dc.Profile,
				"port", dc.Serial.Port, "unit_id", dc.UnitID, "period", period.String())
		}
	}
}

// maybeReload reloads config.json and reconciles when the file has changed.
func (p *Poller) maybeReload() {
	fi, err := os.Stat(p.configPath)
	if err != nil {
		slog.Debug("config stat failed", "path", p.configPath, "err", err)
		return
	}
	if fi.ModTime().Equal(p.lastModTime) {
		return
	}
	p.lastModTime = fi.ModTime()
	slog.Info("config change detected, reloading", "path", p.configPath)

	reloaded, err := config.Load(p.configPath)
	if err != nil {
		slog.Warn("config reload failed, keeping current", "err", err)
		return
	}
	if reloaded.Debug != p.cfg.Debug {
		setLogLevel(reloaded.Debug)
		slog.Info("debug logging changed", "debug", reloaded.Debug)
	}
	p.reconcile(reloaded)
}

// tick reads every device whose period has elapsed.
func (p *Poller) tick(now time.Time) {
	for _, sd := range p.devices {
		if now.Before(sd.nextDue) {
			continue
		}
		sd.nextDue = now.Add(sd.period)
		p.read(sd, now)
	}
}

func (p *Poller) read(sd *schedDevice, now time.Time) {
	prev, _ := p.store.Get(sd.cfg.ID)
	wasOnline := prev.Online

	var (
		samples []rtu.Sample
		err     error
	)
	switch {
	case sd.mock:
		samples = mockRead(sd.prof)
	case sd.bus != nil:
		samples, err = sd.bus.Read(sd.cfg.UnitID, sd.prof)
	default:
		err = fmt.Errorf("no serial bus for port %s", sd.cfg.Serial.Port)
	}
	if err != nil {
		reason := err.Error()
		p.store.RecordFailure(sd.cfg.ID, reason)
		if wasOnline || prev.LastError != reason {
			slog.Warn("device offline", "device", sd.cfg.ID, "err", reason)
		}
		return
	}

	metrics := make(map[string]telemetry.Measurement, len(samples))
	for _, sample := range samples {
		metrics[sample.Metric] = telemetry.Measurement{Value: sample.Value, Unit: sample.Unit}
		slog.Debug("reading", "device", sd.cfg.ID, "metric", sample.Metric,
			"raw", hexWords(sample.Raw), "value", sample.Value, "unit", sample.Unit)
	}
	p.store.RecordSuccess(sd.cfg.ID, now.UTC(), metrics)
	if !wasOnline {
		slog.Info("device online", "device", sd.cfg.ID, "metrics", len(metrics))
	}
}

// close releases all serial buses.
func (p *Poller) close() {
	for _, entry := range p.buses {
		entry.bus.Close()
	}
}
