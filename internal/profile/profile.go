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
	"strconv"
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
	// Numeric kinds decode to a scaled number.
	Uint16  ValueType = "uint16"
	Int16   ValueType = "int16"
	Uint32  ValueType = "uint32"
	Int32   ValueType = "int32"
	Float32 ValueType = "float32"

	// Non-numeric kinds. See docs/device-categories.md.
	Enum     ValueType = "enum"     // raw int -> string label
	Bool     ValueType = "bool"     // whole register nonzero, or a single bit
	Bitflags ValueType = "bitflags" // one register -> many named boolean metrics
	Array    ValueType = "array"    // repeated numeric elements -> metric.1 .. metric.N
)

func isNumeric(t ValueType) bool {
	switch t {
	case Uint16, Int16, Uint32, Int32, Float32:
		return true
	}
	return false
}

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
//
//	v1 — numeric register kinds (uint16/int16/uint32/int32/float32).
//	v2 — non-numeric kinds (enum, bool, bitflags, array); measurement values
//	     may be number, string, or boolean.
const CurrentSchemaVersion = 2

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

// Register maps one or more normalized metrics onto a device register (or a run
// of registers). Numeric kinds produce a single scaled value; enum/bool produce
// a single non-numeric value; bitflags and array expand into several metrics.
type Register struct {
	Metric  string    `json:"metric"`  // normalized key, e.g. "ac.power"
	Address uint16    `json:"address"` // starting register address
	Type    ValueType `json:"type"`
	Unit    string    `json:"unit"`  // canonical unit of the normalized value
	Scale   float64   `json:"scale"` // numeric/array: multiplier applied to the raw value (0 => 1)

	// enum: raw integer value (as a decimal string key) -> label.
	Values map[string]string `json:"values,omitempty"`

	// bool: which bit to test; nil means "whole register nonzero".
	Bit *int `json:"bit,omitempty"`

	// bitflags: bit index (decimal string key) -> flag name. Each produces a
	// boolean metric "<Prefix>.<name>" (Prefix falls back to Metric).
	Flags  map[string]string `json:"flags,omitempty"`
	Prefix string            `json:"prefix,omitempty"`

	// array: repeated numeric elements. Count elements of Element type, Stride
	// registers apart (default = element width), labelled "<Metric>.<index>"
	// starting at StartIndex (default 1).
	Element    ValueType `json:"element,omitempty"`
	Count      int       `json:"count,omitempty"`
	Stride     int       `json:"stride,omitempty"`
	StartIndex int       `json:"start_index,omitempty"`
}

// Reading is one decoded metric. A single Register yields one Reading for
// numeric/enum/bool kinds, and several for bitflags/array. Value is a
// float64, string, or bool.
type Reading struct {
	Metric string
	Value  any
	Unit   string
}

// numericWidth is how many 16-bit registers a numeric type occupies.
func numericWidth(t ValueType) uint16 {
	switch t {
	case Uint32, Int32, Float32:
		return 2
	default:
		return 1
	}
}

// arrayStride is the register spacing between array elements.
func (r Register) arrayStride() uint16 {
	if r.Stride > 0 {
		return uint16(r.Stride)
	}
	return numericWidth(r.Element)
}

// RegisterCount is how many 16-bit registers this register reads.
func (r Register) RegisterCount() uint16 {
	switch r.Type {
	case Enum, Bool, Bitflags:
		return 1
	case Array:
		if r.Count <= 0 {
			return numericWidth(r.Element)
		}
		return uint16(r.Count-1)*r.arrayStride() + numericWidth(r.Element)
	default:
		return numericWidth(r.Type)
	}
}

// label names the register for error messages.
func (r Register) label() string {
	if r.Metric != "" {
		return r.Metric
	}
	return r.Prefix
}

// Decode turns this register's raw words into one or more decoded metrics.
func (r Register) Decode(raw []uint16, wo WordOrder) ([]Reading, error) {
	if len(raw) < int(r.RegisterCount()) {
		return nil, fmt.Errorf("metric %s: need %d registers, got %d", r.label(), r.RegisterCount(), len(raw))
	}
	switch r.Type {
	case Uint16, Int16, Uint32, Int32, Float32:
		v, err := decodeNumeric(r.Type, raw, wo, r.Scale)
		if err != nil {
			return nil, err
		}
		return []Reading{{Metric: r.Metric, Value: v, Unit: r.Unit}}, nil

	case Enum:
		code := int(raw[0])
		label, ok := r.Values[strconv.Itoa(code)]
		if !ok {
			label = fmt.Sprintf("unknown(%d)", code)
		}
		return []Reading{{Metric: r.Metric, Value: label, Unit: r.Unit}}, nil

	case Bool:
		return []Reading{{Metric: r.Metric, Value: decodeBool(raw[0], r.Bit), Unit: r.Unit}}, nil

	case Bitflags:
		prefix := r.label()
		out := make([]Reading, 0, len(r.Flags))
		for bitStr, name := range r.Flags {
			bit, err := strconv.Atoi(bitStr)
			if err != nil || bit < 0 || bit > 15 {
				return nil, fmt.Errorf("metric %s: bad flag bit %q", prefix, bitStr)
			}
			out = append(out, Reading{Metric: prefix + "." + name, Value: bitSet(raw[0], bit), Unit: ""})
		}
		return out, nil

	case Array:
		width := int(numericWidth(r.Element))
		stride := int(r.arrayStride())
		start := r.StartIndex
		if start == 0 {
			start = 1
		}
		out := make([]Reading, 0, r.Count)
		for i := 0; i < r.Count; i++ {
			off := i * stride
			v, err := decodeNumeric(r.Element, raw[off:off+width], wo, r.Scale)
			if err != nil {
				return nil, err
			}
			out = append(out, Reading{Metric: fmt.Sprintf("%s.%d", r.Metric, start+i), Value: v, Unit: r.Unit})
		}
		return out, nil

	default:
		return nil, fmt.Errorf("metric %s: unknown value type %q", r.label(), r.Type)
	}
}

func decodeNumeric(t ValueType, raw []uint16, wo WordOrder, scale float64) (float64, error) {
	if scale == 0 {
		scale = 1
	}
	switch t {
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
		return 0, fmt.Errorf("not a numeric type: %q", t)
	}
}

func decodeBool(word uint16, bit *int) bool {
	if bit != nil {
		return bitSet(word, *bit)
	}
	return word != 0
}

func bitSet(word uint16, bit int) bool {
	return (word>>uint(bit))&1 == 1
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
		if err := r.validate(i); err != nil {
			return err
		}
	}
	return nil
}

func (r Register) validate(i int) error {
	switch r.Type {
	case Uint16, Int16, Uint32, Int32, Float32, Enum, Bool:
		if r.Metric == "" {
			return fmt.Errorf("register %d (%s) has no metric", i, r.Type)
		}
		if r.Type == Enum && len(r.Values) == 0 {
			return fmt.Errorf("metric %s: enum has no values", r.Metric)
		}
	case Bitflags:
		if r.Prefix == "" && r.Metric == "" {
			return fmt.Errorf("register %d (bitflags) needs a prefix or metric", i)
		}
		if len(r.Flags) == 0 {
			return fmt.Errorf("register %d (bitflags) has no flags", i)
		}
	case Array:
		if r.Metric == "" {
			return fmt.Errorf("register %d (array) has no metric", i)
		}
		if r.Count <= 0 {
			return fmt.Errorf("metric %s: array needs count > 0", r.Metric)
		}
		if !isNumeric(r.Element) {
			return fmt.Errorf("metric %s: array element must be a numeric type, got %q", r.Metric, r.Element)
		}
	default:
		return fmt.Errorf("register %d: unknown value type %q", i, r.Type)
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
