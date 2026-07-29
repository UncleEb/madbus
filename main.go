// Command madbus reads normalized telemetry from RS-485 / Modbus RTU devices
// and serves it over the v1 REST API (see docs/api.md).
//
// The poll loop reads each configured device on an interval, logs the raw +
// decoded feed to the terminal (when debug is on), and keeps the latest reading
// in memory for the API. Readings are not persisted (there is no database);
// the only state that survives a restart is each device's last-online time,
// written to last_seen.json. A device whose serial port is "mock" produces
// synthetic readings so the whole pipeline can run without hardware.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"madbus/internal/api"
	"madbus/internal/config"
	"madbus/internal/profile"
	"madbus/internal/rtu"
	"madbus/internal/state"
	"madbus/internal/telemetry"
)

// webFS holds the embedded web UI (served at /).
//
//go:embed web
var webFS embed.FS

// stateFlushInterval bounds how often last_seen.json is rewritten while device
// timestamps are advancing — a modest cadence to limit flash wear on a Pi. A
// final flush also runs on clean shutdown.
const stateFlushInterval = 60 * time.Second

// lastSeenFile is the state file name, kept alongside the config file.
const lastSeenFile = "last_seen.json"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// device pairs a configured device with its resolved profile and, for real
// hardware, the shared serial bus it lives on.
type device struct {
	cfg  config.Device
	prof *profile.Profile
	bus  *rtu.Bus // nil for mock devices
	mock bool
}

func run() error {
	configPath := flag.String("config", "config.json", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	setLogLevel(cfg.Debug)

	profiles, err := profile.Load(cfg.ProfilesDir)
	if err != nil {
		return fmt.Errorf("load profiles: %w", err)
	}
	slog.Info("profiles loaded", "count", len(profiles), "dir", cfg.ProfilesDir)

	store := telemetry.NewStore()
	devices, buses, err := buildDevices(cfg, profiles, store)
	if err != nil {
		return err
	}

	// Restore per-device last-online timestamps so a just-restarted Madbus can
	// report when each device was last reachable, before the first poll.
	statePath := filepath.Join(filepath.Dir(*configPath), lastSeenFile)
	if seen, err := state.Load(statePath); err != nil {
		slog.Warn("could not load last-seen state", "path", statePath, "err", err)
	} else if len(seen) > 0 {
		store.SeedLastOnline(seen)
		slog.Info("last-seen state restored", "devices", len(seen), "path", statePath)
	}

	// flush persists last-online timestamps only when they've changed since the
	// previous write, so a steady poll loop writes at most once per tick.
	lastWritten := store.LastOnline()
	flush := func(reason string) {
		current := store.LastOnline()
		if sameTimes(current, lastWritten) {
			return
		}
		if err := state.Save(statePath, current); err != nil {
			slog.Warn("could not write last-seen state", "path", statePath, "err", err)
			return
		}
		lastWritten = current
		slog.Debug("last-seen state persisted", "reason", reason, "devices", len(current))
	}

	srv := &http.Server{Addr: cfg.HTTPAddr, Handler: api.NewServer(store, webFS, *configPath).Handler()}
	go func() {
		slog.Info("http listening", "addr", cfg.HTTPAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("http server", "err", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	interval := time.Duration(cfg.PollIntervalSeconds) * time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	stateTicker := time.NewTicker(stateFlushInterval)
	defer stateTicker.Stop()

	slog.Info("madbus started", "devices", len(devices), "poll_interval", interval.String())

	pollAll(devices, store)
	online := 0
	for _, d := range store.Snapshot() {
		if d.Online {
			online++
		}
	}
	slog.Info("initial poll complete", "online", online, "total", len(devices))
	for {
		select {
		case <-ctx.Done():
			slog.Info("shutting down")
			flush("shutdown")
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = srv.Shutdown(shutCtx)
			cancel()
			for _, b := range buses {
				b.Close()
			}
			return nil
		case <-ticker.C:
			// Live config reload (Sola-style): config.json is the source of
			// truth. Settings apply live here; device reconcile lands with the
			// device-management slice.
			if reloaded, err := config.Load(*configPath); err != nil {
				slog.Warn("config reload failed, keeping current settings", "err", err)
			} else {
				applySettings(cfg, reloaded, ticker)
				cfg = reloaded
			}
			pollAll(devices, store)
		case <-stateTicker.C:
			flush("interval")
		}
	}
}

// setLogLevel installs a slog text handler at the level implied by debug.
func setLogLevel(debug bool) {
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))
}

// applySettings applies the settings-level differences between the previously
// loaded config and a freshly reloaded one, live. Listen-address changes are not
// applied here (the socket is already bound) and take effect on the next start.
func applySettings(old, cur *config.Config, ticker *time.Ticker) {
	if cur.PollIntervalSeconds != old.PollIntervalSeconds && cur.PollIntervalSeconds > 0 {
		ticker.Reset(time.Duration(cur.PollIntervalSeconds) * time.Second)
		slog.Info("poll interval changed", "seconds", cur.PollIntervalSeconds)
	}
	if cur.Debug != old.Debug {
		setLogLevel(cur.Debug)
		slog.Info("debug logging changed", "debug", cur.Debug)
	}
}

// sameTimes reports whether two last-online maps are identical.
func sameTimes(a, b map[string]time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for id, t := range a {
		if other, ok := b[id]; !ok || !other.Equal(t) {
			return false
		}
	}
	return true
}

// buildDevices resolves profiles, registers devices in the store, and creates
// one shared Bus per serial port.
func buildDevices(cfg *config.Config, profiles map[string]*profile.Profile, store *telemetry.Store) ([]device, map[string]*rtu.Bus, error) {
	buses := make(map[string]*rtu.Bus)
	var devices []device

	for _, dc := range cfg.Devices {
		prof, ok := profiles[dc.Profile]
		if !ok {
			return nil, nil, fmt.Errorf("device %q references unknown profile %q", dc.ID, dc.Profile)
		}
		store.Register(dc.ID, dc.Name, dc.Profile, string(prof.Category))

		d := device{cfg: dc, prof: prof}
		if strings.EqualFold(dc.Serial.Port, "mock") {
			d.mock = true
			slog.Info("device configured (mock)", "device", dc.ID, "profile", dc.Profile)
		} else {
			bus, ok := buses[dc.Serial.Port]
			if !ok {
				parity, err := rtu.ParseParity(dc.Serial.Parity)
				if err != nil {
					return nil, nil, fmt.Errorf("device %q: %w", dc.ID, err)
				}
				bus = rtu.NewBus(rtu.SerialParams{
					Port:     dc.Serial.Port,
					Baud:     dc.Serial.Baud,
					DataBits: dc.Serial.DataBits,
					Parity:   parity,
					StopBits: dc.Serial.StopBits,
					Timeout:  time.Second,
				})
				buses[dc.Serial.Port] = bus
			}
			d.bus = bus
			slog.Info("device configured", "device", dc.ID, "profile", dc.Profile,
				"port", dc.Serial.Port, "unit_id", dc.UnitID)
		}
		devices = append(devices, d)
	}
	return devices, buses, nil
}

func pollAll(devices []device, store *telemetry.Store) {
	for _, d := range devices {
		prev, _ := store.Get(d.cfg.ID)
		wasOnline := prev.Online

		var (
			samples []rtu.Sample
			err     error
		)
		if d.mock {
			samples = mockRead(d.prof)
		} else {
			samples, err = d.bus.Read(d.cfg.UnitID, d.prof)
		}
		if err != nil {
			reason := err.Error()
			store.RecordFailure(d.cfg.ID, reason)
			// Announce the offline edge — coming from online, the first failure,
			// or a changed reason — with the cause, then retry quietly. This is
			// what makes "device unplugged / not connected yet" visible instead
			// of a silent offline device.
			if wasOnline || prev.LastError != reason {
				slog.Warn("device offline", "device", d.cfg.ID, "err", reason)
			}
			continue
		}

		metrics := make(map[string]telemetry.Measurement, len(samples))
		for _, sample := range samples {
			metrics[sample.Metric] = telemetry.Measurement{Value: sample.Value, Unit: sample.Unit}
			// Raw + decoded per-metric feed, emitted only when debug is enabled.
			slog.Debug("reading",
				"device", d.cfg.ID,
				"metric", sample.Metric,
				"raw", hexWords(sample.Raw),
				"value", sample.Value,
				"unit", sample.Unit,
			)
		}
		store.RecordSuccess(d.cfg.ID, time.Now().UTC(), metrics)
		if !wasOnline {
			slog.Info("device online", "device", d.cfg.ID, "metrics", len(metrics))
		}
	}
}

func hexWords(raw []uint16) string {
	if len(raw) == 0 {
		return "mock"
	}
	parts := make([]string, len(raw))
	for i, w := range raw {
		parts[i] = fmt.Sprintf("0x%04X", w)
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// mockRead synthesizes plausible readings from a profile so the pipeline and
// API can be exercised before hardware arrives.
func mockRead(prof *profile.Profile) []rtu.Sample {
	phase := float64(time.Now().Unix()%60) / 60.0
	out := make([]rtu.Sample, 0, len(prof.Registers))
	for _, r := range prof.Registers {
		out = append(out, rtu.Sample{Metric: r.Metric, Unit: r.Unit, Value: mockValue(r.Metric, phase)})
	}
	return out
}

func mockValue(metric string, phase float64) float64 {
	wobble := math.Sin(phase * 2 * math.Pi)
	switch {
	case strings.Contains(metric, "voltage"):
		return 230 + 3*wobble
	case strings.Contains(metric, "current"):
		return 5 + 0.5*wobble
	case strings.Contains(metric, "power"):
		return 1150 + 120*wobble
	case strings.Contains(metric, "frequency"):
		return 60 + 0.05*wobble
	case strings.Contains(metric, "soc"):
		return 85 + 5*wobble
	case strings.Contains(metric, "energy"):
		return 10432.5 + phase
	default:
		return 100 + 10*wobble
	}
}
