// Package config defines the Madbus configuration file format and loading.
package config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// slugPattern is the allowed form for a device id: lowercase letters/digits in
// hyphen-separated groups, e.g. "meter-1".
var slugPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

// Config is the top-level Madbus configuration, loaded from config.json.
type Config struct {
	HTTPAddr            string   `json:"http_addr"`
	PollIntervalSeconds int      `json:"poll_interval_seconds"`
	ProfilesDir         string   `json:"profiles_dir"`
	Debug               bool     `json:"debug"`
	Devices             []Device `json:"devices"`
}

// Device is a single physical device Madbus polls.
type Device struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Profile string `json:"profile"`
	// UnitID is the Modbus slave/unit address on the RS-485 bus.
	UnitID uint8 `json:"unit_id"`
	// PollIntervalSeconds overrides the global default for this device. 0 = use
	// the global PollIntervalSeconds.
	PollIntervalSeconds int    `json:"poll_interval_seconds,omitempty"`
	Serial              Serial `json:"serial"`
}

// Serial describes the serial link to a device. Multiple devices may share the
// same Port (one RS-485 bus, distinguished by UnitID).
type Serial struct {
	// Port is the serial device path, e.g. /dev/ttyUSB0. The literal value
	// "mock" makes Madbus synthesize readings instead of opening a port, so the
	// pipeline can be exercised without hardware.
	Port     string `json:"port"`
	Baud     uint   `json:"baud"`
	DataBits uint   `json:"data_bits"`
	Parity   string `json:"parity"` // none | even | odd
	StopBits uint   `json:"stop_bits"`
}

// Default returns a configuration suitable for first-run: a single energy meter
// on /dev/ttyUSB0 using the generic-meter profile.
func Default() *Config {
	return &Config{
		HTTPAddr:            ":8090",
		PollIntervalSeconds: 5,
		ProfilesDir:         "profiles",
		Debug:               false,
		Devices: []Device{{
			ID:      "meter-1",
			Name:    "Main Energy Meter",
			Profile: "generic-meter",
			UnitID:  1,
			Serial: Serial{
				Port:     "/dev/ttyUSB0",
				Baud:     9600,
				DataBits: 8,
				Parity:   "none",
				StopBits: 1,
			},
		}},
	}
}

// Load reads config from path. If the file does not exist, a default config is
// written to path and returned, so first run is zero-setup.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := Default()
			if werr := Save(path, cfg); werr != nil {
				return nil, fmt.Errorf("write default config: %w", werr)
			}
			return cfg, nil
		}
		return nil, err
	}

	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.applyDefaults()
	return cfg, nil
}

// Save writes cfg to path as indented JSON, atomically (temp file + rename) so a
// concurrent reader (the poll loop reloading config) never sees a partial write.
func Save(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op once renamed away

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// FillSerialDefaults fills zero serial fields (except baud, which is required)
// so a device created via the API is stored fully specified.
func (d *Device) FillSerialDefaults() {
	if strings.EqualFold(d.Serial.Port, "mock") {
		return
	}
	if d.Serial.DataBits == 0 {
		d.Serial.DataBits = 8
	}
	if d.Serial.StopBits == 0 {
		d.Serial.StopBits = 1
	}
	if d.Serial.Parity == "" {
		d.Serial.Parity = "none"
	}
}

// Validate checks a single device's fields.
func (d Device) Validate() error {
	if d.ID == "" {
		return fmt.Errorf("device id is required")
	}
	if !slugPattern.MatchString(d.ID) {
		return fmt.Errorf("device id %q must be a slug: lowercase letters, digits, and hyphens (e.g. meter-1)", d.ID)
	}
	if d.Name == "" {
		return fmt.Errorf("device name is required")
	}
	if d.Profile == "" {
		return fmt.Errorf("device profile is required")
	}
	if d.PollIntervalSeconds < 0 || d.PollIntervalSeconds > 3600 {
		return fmt.Errorf("poll_interval_seconds must be 0 (use default) or between 1 and 3600")
	}
	if d.Serial.Port == "" {
		return fmt.Errorf("serial port is required (a device path like /dev/ttyUSB0, or \"mock\")")
	}
	if !strings.EqualFold(d.Serial.Port, "mock") {
		if d.Serial.Baud == 0 {
			return fmt.Errorf("serial baud is required")
		}
		switch strings.ToLower(d.Serial.Parity) {
		case "", "none", "even", "odd":
		default:
			return fmt.Errorf("serial parity must be none, even, or odd")
		}
	}
	return nil
}

// Validate checks the settings-level fields. Device-level validation is added
// with device management.
func (c *Config) Validate() error {
	if c.HTTPAddr == "" {
		return fmt.Errorf("http_addr must not be empty")
	}
	if _, _, err := net.SplitHostPort(c.HTTPAddr); err != nil {
		return fmt.Errorf("http_addr must be in host:port form (e.g. :8090)")
	}
	if c.PollIntervalSeconds < 1 || c.PollIntervalSeconds > 3600 {
		return fmt.Errorf("poll_interval_seconds must be between 1 and 3600")
	}
	if c.ProfilesDir == "" {
		return fmt.Errorf("profiles_dir must not be empty")
	}
	return nil
}

// applyDefaults fills in sensible values for any zero fields so a sparse config
// file still works.
func (c *Config) applyDefaults() {
	if c.HTTPAddr == "" {
		c.HTTPAddr = ":8090"
	}
	if c.PollIntervalSeconds <= 0 {
		c.PollIntervalSeconds = 5
	}
	if c.ProfilesDir == "" {
		c.ProfilesDir = "profiles"
	}
	for i := range c.Devices {
		s := &c.Devices[i].Serial
		if s.Baud == 0 {
			s.Baud = 9600
		}
		if s.DataBits == 0 {
			s.DataBits = 8
		}
		if s.StopBits == 0 {
			s.StopBits = 1
		}
		if s.Parity == "" {
			s.Parity = "none"
		}
	}
}
