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
	poller := newPoller(store, profiles, *configPath, cfg)

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

	// A 1s base tick drives the scheduler: config is reloaded on change, then
	// every device whose period has elapsed is read. Per-device periods give 1s
	// resolution; devices sharing a serial bus serialize on the bus mutex.
	baseTicker := time.NewTicker(time.Second)
	defer baseTicker.Stop()

	stateTicker := time.NewTicker(stateFlushInterval)
	defer stateTicker.Stop()

	slog.Info("madbus started", "devices", poller.deviceCount(),
		"default_poll_interval", (time.Duration(cfg.PollIntervalSeconds) * time.Second).String())

	poller.tick(time.Now())
	online := 0
	for _, d := range store.Snapshot() {
		if d.Online {
			online++
		}
	}
	slog.Info("initial poll complete", "online", online, "total", poller.deviceCount())

	for {
		select {
		case <-ctx.Done():
			slog.Info("shutting down")
			flush("shutdown")
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = srv.Shutdown(shutCtx)
			cancel()
			poller.close()
			return nil
		case now := <-baseTicker.C:
			poller.maybeReload()
			poller.tick(now)
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
