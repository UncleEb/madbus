// Package telemetry holds the latest normalized reading for every device in
// memory. It is the source the HTTP API serves from. Persistence (SQLite) can
// be layered on later without changing this shape.
package telemetry

import (
	"sync"
	"time"
)

// Measurement is one normalized value. Value is nil when the metric has never
// been read successfully. Stale is true when the value is a last-known reading
// that was not refreshed on the most recent poll.
type Measurement struct {
	Value *float64
	Unit  string
	Stale bool
}

// DeviceState is the current known state of one device.
type DeviceState struct {
	ID       string
	Name     string
	Profile  string
	Online   bool
	LastRead time.Time // zero if never read successfully
	Metrics  map[string]Measurement
}

// Store is a concurrency-safe map of device state.
type Store struct {
	mu      sync.RWMutex
	order   []string // preserves registration order for stable output
	devices map[string]*DeviceState
}

func NewStore() *Store {
	return &Store{devices: make(map[string]*DeviceState)}
}

// Register adds a device in the offline state. It is idempotent.
func (s *Store) Register(id, name, profileID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.devices[id]; ok {
		return
	}
	s.devices[id] = &DeviceState{
		ID:      id,
		Name:    name,
		Profile: profileID,
		Metrics: make(map[string]Measurement),
	}
	s.order = append(s.order, id)
}

// RecordSuccess replaces a device's metrics with fresh readings.
func (s *Store) RecordSuccess(id string, readAt time.Time, metrics map[string]Measurement) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		return
	}
	d.Online = true
	d.LastRead = readAt
	d.Metrics = metrics
}

// RecordFailure marks a device offline and flags its last-known values stale.
// Values are retained so a dropped link doesn't blank the device.
func (s *Store) RecordFailure(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.devices[id]
	if !ok {
		return
	}
	d.Online = false
	for k, m := range d.Metrics {
		m.Stale = true
		d.Metrics[k] = m
	}
}

// LastOnline returns each device's last successful-read time. Devices never
// read successfully (zero time) are omitted. This is the only state Madbus
// persists across restarts.
func (s *Store) LastOnline() map[string]time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]time.Time)
	for id, d := range s.devices {
		if !d.LastRead.IsZero() {
			out[id] = d.LastRead
		}
	}
	return out
}

// SeedLastOnline restores persisted last-online timestamps into already-
// registered devices, without marking them online. It never moves a device's
// LastRead backwards, so a fresh live poll always wins over stale disk state.
func (s *Store) SeedLastOnline(seen map[string]time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, t := range seen {
		d, ok := s.devices[id]
		if !ok {
			continue
		}
		if d.LastRead.Before(t) {
			d.LastRead = t
		}
	}
}

// Snapshot returns deep copies of all devices in registration order.
func (s *Store) Snapshot() []DeviceState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]DeviceState, 0, len(s.order))
	for _, id := range s.order {
		out = append(out, s.devices[id].copy())
	}
	return out
}

// Get returns a deep copy of one device.
func (s *Store) Get(id string) (DeviceState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.devices[id]
	if !ok {
		return DeviceState{}, false
	}
	return d.copy(), true
}

func (d *DeviceState) copy() DeviceState {
	metrics := make(map[string]Measurement, len(d.Metrics))
	for k, m := range d.Metrics {
		if m.Value != nil {
			v := *m.Value
			m.Value = &v
		}
		metrics[k] = m
	}
	cp := *d
	cp.Metrics = metrics
	return cp
}
