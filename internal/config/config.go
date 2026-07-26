// Package config defines the Madbus configuration file format and loading.
package config

import (
	"encoding/json"
	"fmt"
	"os"
)

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
	UnitID uint8  `json:"unit_id"`
	Serial Serial `json:"serial"`
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

// Save writes cfg to path as indented JSON.
func Save(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
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
