// Package profile describes how a device's raw Modbus registers map onto
// normalized metrics, and decodes raw register words into scaled values.
//
// Profiles are plain JSON files so support for a new device can be added
// without touching Go code.
package profile

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

// RegisterType selects the Modbus read function used for a profile.
type RegisterType string

const (
	Input   RegisterType = "input"   // function 0x04, read input registers
	Holding RegisterType = "holding" // function 0x03, read holding registers
)

// ValueType is how the raw register words are interpreted.
type ValueType string

const (
	Uint16  ValueType = "uint16"
	Int16   ValueType = "int16"
	Uint32  ValueType = "uint32"
	Int32   ValueType = "int32"
	Float32 ValueType = "float32"
)

// WordOrder is the order of 16-bit words within a 32-bit value. Individual
// registers are always big-endian (standard Modbus); only the word order
// varies between devices.
type WordOrder string

const (
	HighWordFirst WordOrder = "high_first"
	LowWordFirst  WordOrder = "low_first"
)

// Category is the device class a profile belongs to. It is the contract with
// consumers — Sola renders a widget per category — independent of the vendor.
// See docs/device-categories.md for the canonical vocabulary of each.
type Category string

const (
	CategoryMeter            Category = "meter"
	CategoryChargeController Category = "charge_controller"
	CategoryShunt            Category = "shunt"
	CategoryInverter         Category = "inverter"
	CategoryBMS              Category = "bms"
)

var validCategories = map[Category]bool{
	CategoryMeter:            true,
	CategoryChargeController: true,
	CategoryShunt:            true,
	CategoryInverter:         true,
	CategoryBMS:              true,
}

// CurrentSchemaVersion is the profile format version this build understands.
// A profile omitting schema_version is assumed to target this version.
const CurrentSchemaVersion = 1

// Profile is a device's complete register map.
type Profile struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	Category      Category     `json:"category"`
	SchemaVersion int          `json:"schema_version"`
	RegisterType  RegisterType `json:"register_type"`
	WordOrder     WordOrder    `json:"word_order"`
	Registers     []Register   `json:"registers"`
}

// Register maps one normalized metric onto a device register (or register pair).
type Register struct {
	Metric  string    `json:"metric"`  // normalized key, e.g. "ac.power"
	Address uint16    `json:"address"` // starting register address
	Type    ValueType `json:"type"`
	Unit    string    `json:"unit"`  // canonical unit of the normalized value
	Scale   float64   `json:"scale"` // multiplier applied to the raw value (0 => 1)
}

// RegisterCount is how many 16-bit registers this value occupies.
func (r Register) RegisterCount() uint16 {
	switch r.Type {
	case Uint32, Int32, Float32:
		return 2
	default:
		return 1
	}
}

// Decode turns the raw register words for this register into a scaled value.
func (r Register) Decode(raw []uint16, wo WordOrder) (float64, error) {
	if len(raw) < int(r.RegisterCount()) {
		return 0, fmt.Errorf("metric %s: need %d registers, got %d", r.Metric, r.RegisterCount(), len(raw))
	}
	scale := r.Scale
	if scale == 0 {
		scale = 1
	}
	switch r.Type {
	case Uint16:
		return float64(raw[0]) * scale, nil
	case Int16:
		return float64(int16(raw[0])) * scale, nil
	case Uint32:
		return float64(combine32(raw, wo)) * scale, nil
	case Int32:
		return float64(int32(combine32(raw, wo))) * scale, nil
	case Float32:
		return float64(math.Float32frombits(combine32(raw, wo))) * scale, nil
	default:
		return 0, fmt.Errorf("metric %s: unknown value type %q", r.Metric, r.Type)
	}
}

func combine32(raw []uint16, wo WordOrder) uint32 {
	hi, lo := raw[0], raw[1]
	if wo == LowWordFirst {
		hi, lo = raw[1], raw[0]
	}
	return uint32(hi)<<16 | uint32(lo)
}

func (p *Profile) applyDefaults() {
	if p.RegisterType == "" {
		p.RegisterType = Holding
	}
	if p.WordOrder == "" {
		p.WordOrder = HighWordFirst
	}
	if p.SchemaVersion == 0 {
		p.SchemaVersion = CurrentSchemaVersion
	}
}

func (p *Profile) validate() error {
	if p.ID == "" {
		return fmt.Errorf("profile has no id")
	}
	if p.Category == "" {
		return fmt.Errorf("profile %q: missing category (one of meter, charge_controller, shunt, inverter, bms)", p.ID)
	}
	if !validCategories[p.Category] {
		return fmt.Errorf("profile %q: unknown category %q", p.ID, p.Category)
	}
	if p.SchemaVersion > CurrentSchemaVersion {
		return fmt.Errorf("profile %q: schema_version %d is newer than this build supports (%d)", p.ID, p.SchemaVersion, CurrentSchemaVersion)
	}
	if len(p.Registers) == 0 {
		return fmt.Errorf("profile %q has no registers", p.ID)
	}
	for i, r := range p.Registers {
		if r.Metric == "" {
			return fmt.Errorf("register %d has no metric", i)
		}
		switch r.Type {
		case Uint16, Int16, Uint32, Int32, Float32:
		default:
			return fmt.Errorf("metric %s: unknown value type %q", r.Metric, r.Type)
		}
	}
	return nil
}

// Load reads every *.json profile in dir, keyed by profile ID.
func Load(dir string) (map[string]*Profile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read profiles dir %s: %w", dir, err)
	}
	profiles := make(map[string]*Profile)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read profile %s: %w", path, err)
		}
		var p Profile
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("parse profile %s: %w", path, err)
		}
		if err := p.validate(); err != nil {
			return nil, fmt.Errorf("invalid profile %s: %w", path, err)
		}
		p.applyDefaults()
		if _, dup := profiles[p.ID]; dup {
			return nil, fmt.Errorf("duplicate profile id %q (%s)", p.ID, path)
		}
		profiles[p.ID] = &p
	}
	return profiles, nil
}
