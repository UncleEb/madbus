package rtu

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SerialPort is a detected serial interface and a human-readable description.
type SerialPort struct {
	Path        string `json:"path"`        // e.g. /dev/ttyUSB0
	Description string `json:"description"` // e.g. "FTDI FT232R USB UART"
}

// ListPorts returns the USB-to-serial *bridge adapters* present on the system
// (Linux) — the ttyUSB* devices created by USB-serial bridge chips (FTDI,
// CP210x, CH340, …). This is what every RS-485 adapter is.
//
// CDC/ACM devices (ttyACM*) are deliberately excluded: those are "smart" USB
// gadgets with their own MCU emulating a serial port (Z-Wave/Zigbee sticks,
// Arduinos, modems), essentially never a plain RS-485 adapter — so hiding them
// keeps the picker to real candidates. (A CDC-based serial device would need an
// escape hatch here if we ever support one.)
//
// Descriptions come from the USB device's sysfs metadata; adapters that report
// none get a "Generic USB/Serial connector N" label so the user never has to
// reason about /dev paths.
func ListPorts() []SerialPort {
	matches, _ := filepath.Glob("/dev/ttyUSB*")
	sort.Strings(matches)

	var ports []SerialPort
	generic := 0
	for _, dev := range matches {
		desc := describePort(dev)
		if desc == "" {
			generic++
			desc = fmt.Sprintf("Generic USB/Serial connector %d", generic)
		}
		ports = append(ports, SerialPort{Path: dev, Description: desc})
	}
	return ports
}

// describePort reads "<manufacturer> <product>" from the USB device backing a
// tty, by walking up sysfs from the tty to the USB device dir (the one holding
// idVendor). Returns "" if nothing descriptive is found.
func describePort(dev string) string {
	dir, err := filepath.EvalSymlinks(filepath.Join("/sys/class/tty", filepath.Base(dev), "device"))
	if err != nil {
		return ""
	}
	for i := 0; i < 6 && dir != "/" && dir != "."; i++ {
		if _, err := os.Stat(filepath.Join(dir, "idVendor")); err == nil {
			desc := strings.TrimSpace(readTrim(filepath.Join(dir, "manufacturer")) + " " + readTrim(filepath.Join(dir, "product")))
			return desc
		}
		dir = filepath.Dir(dir)
	}
	return ""
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
