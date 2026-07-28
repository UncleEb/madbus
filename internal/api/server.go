// Package api implements the Madbus v1 REST contract over the telemetry store.
// See docs/api.md for the full specification.
package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"time"

	"madbus/internal/telemetry"
)

type Server struct {
	store   *telemetry.Store
	started time.Time
}

func NewServer(store *telemetry.Store) *Server {
	return &Server{store: store, started: time.Now()}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/devices", s.handleDevices)
	mux.HandleFunc("GET /api/v1/devices/{id}/measurements", s.handleDeviceMeasurements)
	mux.HandleFunc("POST /api/v1/measurements", s.handleBatch)
	return mux
}

// --- wire types (see docs/api.md) ---

type measurementDTO struct {
	Value any    `json:"value"` // number, string, or bool; null if never read
	Unit  string `json:"unit"`
	Stale bool   `json:"stale,omitempty"`
}

type deviceDTO struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Profile  string  `json:"profile"`
	Category string  `json:"category"`
	Online   bool    `json:"online"`
	LastRead *string `json:"last_read"`
}

type deviceMeasurements struct {
	Device       deviceDTO                 `json:"device"`
	Measurements map[string]measurementDTO `json:"measurements"`
}

type batchRequest struct {
	Devices []deviceSelector `json:"devices"`
}

type deviceSelector struct {
	ID      string   `json:"id"`
	Metrics []string `json:"metrics"`
}

type unmatchedDTO struct {
	Devices []string            `json:"devices,omitempty"`
	Metrics map[string][]string `json:"metrics,omitempty"`
}

type batchResponse struct {
	ReadAt    string               `json:"read_at"`
	Devices   []deviceMeasurements `json:"devices"`
	Unmatched unmatchedDTO         `json:"unmatched"`
}

// --- handlers ---

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	devices := s.store.Snapshot()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":         "ok",
		"uptime_seconds": int(time.Since(s.started).Seconds()),
		"device_count":   len(devices),
		"time":           time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	devices := s.store.Snapshot()
	out := make([]deviceDTO, 0, len(devices))
	for _, d := range devices {
		out = append(out, toDeviceDTO(d))
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

func (s *Server) handleDeviceMeasurements(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d, ok := s.store.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown device: "+id)
		return
	}
	ms, _ := measurementMap(d.Metrics, nil)
	writeJSON(w, http.StatusOK, deviceMeasurements{
		Device:       toDeviceDTO(d),
		Measurements: ms,
	})
}

func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request) {
	var req batchRequest
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
			return
		}
	}

	snapshot := s.store.Snapshot()
	byID := make(map[string]telemetry.DeviceState, len(snapshot))
	for _, d := range snapshot {
		byID[d.ID] = d
	}

	resp := batchResponse{
		ReadAt:  time.Now().UTC().Format(time.RFC3339),
		Devices: []deviceMeasurements{},
	}

	// Empty selector list => whole-system poll (all devices, all metrics).
	if len(req.Devices) == 0 {
		for _, d := range snapshot {
			ms, _ := measurementMap(d.Metrics, nil)
			resp.Devices = append(resp.Devices, deviceMeasurements{Device: toDeviceDTO(d), Measurements: ms})
		}
		writeJSON(w, http.StatusOK, resp)
		return
	}

	for _, sel := range req.Devices {
		d, ok := byID[sel.ID]
		if !ok {
			resp.Unmatched.Devices = append(resp.Unmatched.Devices, sel.ID)
			continue
		}
		ms, missing := measurementMap(d.Metrics, sel.Metrics)
		resp.Devices = append(resp.Devices, deviceMeasurements{Device: toDeviceDTO(d), Measurements: ms})
		if len(missing) > 0 {
			if resp.Unmatched.Metrics == nil {
				resp.Unmatched.Metrics = make(map[string][]string)
			}
			resp.Unmatched.Metrics[sel.ID] = missing
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// --- helpers ---

// measurementMap converts stored metrics to wire form. With no filter, all
// metrics are returned; with a filter, only the named metrics are returned and
// any that the device does not expose are reported as missing.
func measurementMap(metrics map[string]telemetry.Measurement, filter []string) (map[string]measurementDTO, []string) {
	out := make(map[string]measurementDTO)
	if len(filter) == 0 {
		for k, m := range metrics {
			out[k] = toMeasurementDTO(m)
		}
		return out, nil
	}
	var missing []string
	for _, k := range filter {
		m, ok := metrics[k]
		if !ok {
			missing = append(missing, k)
			continue
		}
		out[k] = toMeasurementDTO(m)
	}
	return out, missing
}

func toDeviceDTO(d telemetry.DeviceState) deviceDTO {
	var lastRead *string
	if !d.LastRead.IsZero() {
		s := d.LastRead.UTC().Format(time.RFC3339)
		lastRead = &s
	}
	return deviceDTO{
		ID:       d.ID,
		Name:     d.Name,
		Profile:  d.Profile,
		Category: d.Category,
		Online:   d.Online,
		LastRead: lastRead,
	}
}

func toMeasurementDTO(m telemetry.Measurement) measurementDTO {
	return measurementDTO{Value: m.Value, Unit: m.Unit, Stale: m.Stale}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("write response", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
