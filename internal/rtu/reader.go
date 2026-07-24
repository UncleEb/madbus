// Package rtu reads device registers over Modbus RTU (RS-485), driven by a
// device profile. One Bus wraps a single serial port; devices sharing a port
// (an RS-485 multidrop bus) share a Bus and are told apart by unit ID.
package rtu

import (
	"fmt"
	"strings"
	"sync"
	"time"

	modbuslib "github.com/simonvetter/modbus"

	"madbus/internal/profile"
)

// Sample is one decoded metric plus the raw register words it came from (kept
// for diagnostics / terminal logging).
type Sample struct {
	Metric string
	Raw    []uint16
	Value  float64
	Unit   string
}

// SerialParams configures a serial link.
type SerialParams struct {
	Port     string
	Baud     uint
	DataBits uint
	Parity   uint // modbuslib.PARITY_*
	StopBits uint
	Timeout  time.Duration
}

// Bus is a lazily-opened, self-healing Modbus RTU client for one serial port.
// Serial access is inherently non-concurrent, so reads are serialized by mu.
type Bus struct {
	mu     sync.Mutex
	params SerialParams
	client *modbuslib.ModbusClient
	open   bool
}

func NewBus(p SerialParams) *Bus {
	return &Bus{params: p}
}

// ParseParity maps a config string to a modbus parity constant.
func ParseParity(s string) (uint, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "none":
		return modbuslib.PARITY_NONE, nil
	case "even":
		return modbuslib.PARITY_EVEN, nil
	case "odd":
		return modbuslib.PARITY_ODD, nil
	}
	return 0, fmt.Errorf("invalid parity %q (want none|even|odd)", s)
}

// Read reads every register defined by prof for the given unit and returns the
// decoded samples. A communication error closes the link so the next call
// transparently reconnects.
func (b *Bus) Read(unitID uint8, prof *profile.Profile) ([]Sample, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if err := b.ensureOpen(); err != nil {
		return nil, err
	}
	if err := b.client.SetUnitId(unitID); err != nil {
		return nil, fmt.Errorf("set unit id %d: %w", unitID, err)
	}

	regType := HoldingRegisters
	if prof.RegisterType == profile.Input {
		regType = InputRegisters
	}

	samples := make([]Sample, 0, len(prof.Registers))
	for _, reg := range prof.Registers {
		raw, err := b.client.ReadRegisters(reg.Address, reg.RegisterCount(), regType)
		if err != nil {
			// A read error usually means the link dropped; drop it so the next
			// poll reconnects, and report device-level failure.
			b.markClosed()
			return nil, fmt.Errorf("read %s @%d: %w", reg.Metric, reg.Address, err)
		}
		val, err := reg.Decode(raw, prof.WordOrder)
		if err != nil {
			return nil, err
		}
		samples = append(samples, Sample{Metric: reg.Metric, Raw: raw, Value: val, Unit: reg.Unit})
	}
	return samples, nil
}

// Close releases the serial port.
func (b *Bus) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.markClosed()
}

// Register-type aliases so callers don't import the modbus library directly.
const (
	HoldingRegisters = modbuslib.HOLDING_REGISTER
	InputRegisters   = modbuslib.INPUT_REGISTER
)

func (b *Bus) ensureOpen() error {
	if b.open && b.client != nil {
		return nil
	}
	client, err := modbuslib.NewClient(&modbuslib.ClientConfiguration{
		URL:      "rtu://" + b.params.Port,
		Speed:    b.params.Baud,
		DataBits: b.params.DataBits,
		Parity:   b.params.Parity,
		StopBits: b.params.StopBits,
		Timeout:  b.params.Timeout,
	})
	if err != nil {
		return fmt.Errorf("configure serial %s: %w", b.params.Port, err)
	}
	if err := client.Open(); err != nil {
		return fmt.Errorf("open serial %s: %w", b.params.Port, err)
	}
	b.client = client
	b.open = true
	return nil
}

func (b *Bus) markClosed() {
	if b.client != nil {
		_ = b.client.Close()
	}
	b.client = nil
	b.open = false
}
