// Package api implements the Madbus v1 REST contract over the telemetry store.
// See docs/api.md for the full specification.
package api

import (
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"madbus/internal/config"
	"madbus/internal/profile"
	"madbus/internal/rtu"
	"madbus/internal/telemetry"
)

type Server struct {
	store      *telemetry.Store
	started    time.Time
	web        fs.FS  // embedded web UI (contents of the web/ dir)
	configPath string // config.json is the source of truth for settings/devices
	writeMu    sync.Mutex
}

// NewServer builds the API server. webFS is the embedded filesystem whose "web"
// subdirectory holds the static UI served at /. configPath is the config file
// the running poll loop reloads; write endpoints edit it atomically.
func NewServer(store *telemetry.Store, webFS fs.FS, configPath string) *Server {
	content, err := fs.Sub(webFS, "web")
	if err != nil {
		// The embed guarantees web/ exists; fall back to the raw FS if not.
		content = webFS
	}
	return &Server{store: store, started: time.Now(), web: content, configPath: configPath}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/devices", s.handleDevices)
	mux.HandleFunc("POST /api/v1/devices", s.handleCreateDevice)
	mux.HandleFunc("PUT /api/v1/devices/{id}", s.handleUpdateDevice)
	mux.HandleFunc("DELETE /api/v1/devices/{id}", s.handleDeleteDevice)
	mux.HandleFunc("GET /api/v1/devices/{id}/measurements", s.handleDeviceMeasurements)
	mux.HandleFunc("POST /api/v1/measurements", s.handleBatch)
	mux.HandleFunc("GET /api/v1/profiles", s.handleProfiles)
	mux.HandleFunc("GET /api/v1/serial-ports", s.handleSerialPorts)
	mux.HandleFunc("GET /api/v1/settings", s.handleGetSettings)
	mux.HandleFunc("PUT /api/v1/settings", s.handlePutSettings)
	// Static web UI. API routes above are more specific, so they win; anything
	// else (/, /style.css, /app.js, /logo.svg) is served from the embedded FS.
	mux.Handle("/", http.FileServer(http.FS(s.web)))
	return mux
}

// --- wire types (see docs/api.md) ---

type measurementDTO struct {
	Value any    `json:"value"` // number, string, or bool; null if never read
	Unit  string `json:"unit"`
	Stale bool   `json:"stale,omitempty"`
}

type deviceDTO struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Profile   string  `json:"profile"`
	Category  string  `json:"category"`
	Online    bool    `json:"online"`
	LastError string  `json:"last_error,omitempty"`
	LastRead  *string `json:"last_read"`
}

// deviceFullDTO is the management view: device config merged with live status.
type deviceFullDTO struct {
	ID                  string        `json:"id"`
	Name                string        `json:"name"`
	Profile             string        `json:"profile"`
	Category            string        `json:"category"`
	UnitID              uint8         `json:"unit_id"`
	PollIntervalSeconds int           `json:"poll_interval_seconds,omitempty"`
	Serial              config.Serial `json:"serial"`
	Online              bool          `json:"online"`
	LastError           string        `json:"last_error,omitempty"`
	LastRead            *string       `json:"last_read"`
}

func toDeviceFullDTO(dc config.Device, rt telemetry.DeviceState) deviceFullDTO {
	var lastRead *string
	if !rt.LastRead.IsZero() {
		t := rt.LastRead.UTC().Format(time.RFC3339)
		lastRead = &t
	}
	return deviceFullDTO{
		ID:                  dc.ID,
		Name:                dc.Name,
		Profile:             dc.Profile,
		Category:            rt.Category,
		UnitID:              dc.UnitID,
		PollIntervalSeconds: dc.PollIntervalSeconds,
		Serial:              dc.Serial,
		Online:              rt.Online,
		LastError:           rt.LastError,
		LastRead:            lastRead,
	}
}

type profileDTO struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
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

// handleDevices lists devices merged from config (identity + comms config) and
// the store (live status). config.json is the source of truth for the set.
func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read config: "+err.Error())
		return
	}
	runtime := s.runtimeByID()
	out := make([]deviceFullDTO, 0, len(cfg.Devices))
	for _, dc := range cfg.Devices {
		out = append(out, toDeviceFullDTO(dc, runtime[dc.ID]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

func (s *Server) runtimeByID() map[string]telemetry.DeviceState {
	m := make(map[string]telemetry.DeviceState)
	for _, d := range s.store.Snapshot() {
		m[d.ID] = d
	}
	return m
}

func (s *Server) handleCreateDevice(w http.ResponseWriter, r *http.Request) {
	var dev config.Device
	if err := json.NewDecoder(r.Body).Decode(&dev); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	dev.FillSerialDefaults()
	if err := dev.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read config: "+err.Error())
		return
	}
	for _, ex := range cfg.Devices {
		if ex.ID == dev.ID {
			writeError(w, http.StatusConflict, "device id "+dev.ID+" already exists")
			return
		}
	}
	if code, msg := s.checkProfile(cfg.ProfilesDir, dev.Profile); code != 0 {
		writeError(w, code, msg)
		return
	}
	cfg.Devices = append(cfg.Devices, dev)
	if err := config.Save(s.configPath, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "write config: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, toDeviceFullDTO(dev, telemetry.DeviceState{}))
}

func (s *Server) handleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var dev config.Device
	if err := json.NewDecoder(r.Body).Decode(&dev); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	dev.ID = id // the path is authoritative
	dev.FillSerialDefaults()
	if err := dev.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read config: "+err.Error())
		return
	}
	idx := -1
	for i, ex := range cfg.Devices {
		if ex.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeError(w, http.StatusNotFound, "unknown device: "+id)
		return
	}
	if code, msg := s.checkProfile(cfg.ProfilesDir, dev.Profile); code != 0 {
		writeError(w, code, msg)
		return
	}
	cfg.Devices[idx] = dev
	if err := config.Save(s.configPath, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "write config: "+err.Error())
		return
	}
	rt, _ := s.store.Get(id)
	writeJSON(w, http.StatusOK, toDeviceFullDTO(dev, rt))
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read config: "+err.Error())
		return
	}
	idx := -1
	for i, ex := range cfg.Devices {
		if ex.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		writeError(w, http.StatusNotFound, "unknown device: "+id)
		return
	}
	cfg.Devices = append(cfg.Devices[:idx], cfg.Devices[idx+1:]...)
	if err := config.Save(s.configPath, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "write config: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"deleted": id})
}

// checkProfile returns (0, "") if the profile exists, else an (http status,
// message) to write.
func (s *Server) checkProfile(dir, id string) (int, string) {
	profs, err := profile.Load(dir)
	if err != nil {
		return http.StatusInternalServerError, "load profiles: " + err.Error()
	}
	if _, ok := profs[id]; !ok {
		return http.StatusBadRequest, "unknown profile: " + id
	}
	return 0, ""
}

// handleSerialPorts lists detected USB serial adapters so the UI can offer a
// pick-from-hardware dropdown instead of a typed device path.
func (s *Server) handleSerialPorts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ports": rtu.ListPorts()})
}

func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read config: "+err.Error())
		return
	}
	profs, err := profile.Load(cfg.ProfilesDir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load profiles: "+err.Error())
		return
	}
	out := make([]profileDTO, 0, len(profs))
	for _, p := range profs {
		out = append(out, profileDTO{ID: p.ID, Name: p.Name, Category: string(p.Category)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	writeJSON(w, http.StatusOK, map[string]any{"profiles": out})
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

// settingsDTO is the settings-level view of the config (no devices).
type settingsDTO struct {
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	Debug               bool   `json:"debug"`
	HTTPAddr            string `json:"http_addr"`
	ProfilesDir         string `json:"profiles_dir"`
}

func settingsOf(c *config.Config) settingsDTO {
	return settingsDTO{
		PollIntervalSeconds: c.PollIntervalSeconds,
		Debug:               c.Debug,
		HTTPAddr:            c.HTTPAddr,
		ProfilesDir:         c.ProfilesDir,
	}
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read config: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settingsOf(cfg))
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	// Serialize writes, and always edit a freshly-loaded config so device
	// changes made elsewhere aren't clobbered.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	cfg, err := config.Load(s.configPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read config: "+err.Error())
		return
	}
	oldAddr := cfg.HTTPAddr
	cfg.PollIntervalSeconds = in.PollIntervalSeconds
	cfg.Debug = in.Debug
	cfg.HTTPAddr = in.HTTPAddr
	if in.ProfilesDir != "" {
		cfg.ProfilesDir = in.ProfilesDir
	}
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// If the listen port is changing, make sure the new one is actually free —
	// otherwise the change would silently break the server on next restart. The
	// current port is skipped (our own server holds it, and it isn't changing).
	if newPort := portOf(cfg.HTTPAddr); newPort != portOf(oldAddr) {
		if err := checkAddrFree(cfg.HTTPAddr); err != nil {
			writeError(w, http.StatusConflict, "port "+newPort+" is already in use — listen address not changed")
			return
		}
	}
	if err := config.Save(s.configPath, cfg); err != nil {
		writeError(w, http.StatusInternalServerError, "write config: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settingsOf(cfg))
}

// portOf extracts the port from a host:port listen address; if it can't be
// parsed, the whole string is returned so unequal addresses still compare.
func portOf(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil {
		return port
	}
	return addr
}

// checkAddrFree reports whether addr can be bound right now (a pre-flight so a
// changed listen address that's already taken is rejected instead of saved).
func checkAddrFree(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	return ln.Close()
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
		ID:        d.ID,
		Name:      d.Name,
		Profile:   d.Profile,
		Category:  d.Category,
		Online:    d.Online,
		LastError: d.LastError,
		LastRead:  lastRead,
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
