package profile

import (
	"math"
	"testing"
)

func find(t *testing.T, rs []Reading, metric string) Reading {
	t.Helper()
	for _, r := range rs {
		if r.Metric == metric {
			return r
		}
	}
	t.Fatalf("reading %q not found in %+v", metric, rs)
	return Reading{}
}

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestDecodeNumeric(t *testing.T) {
	// float32 0x42F00000 == 120.0, high-word-first
	r := Register{Metric: "ac.voltage", Type: Float32, Unit: "V", Scale: 1}
	got, err := r.Decode([]uint16{0x42F0, 0x0000}, HighWordFirst)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !approx(got[0].Value.(float64), 120.0) {
		t.Fatalf("float32: got %+v, want 120", got)
	}

	// low-word-first swaps the words
	got, _ = r.Decode([]uint16{0x0000, 0x42F0}, LowWordFirst)
	if !approx(got[0].Value.(float64), 120.0) {
		t.Fatalf("float32 low-first: got %v", got[0].Value)
	}

	// uint16 with scale
	u := Register{Metric: "x", Type: Uint16, Scale: 0.1}
	got, _ = u.Decode([]uint16{100}, HighWordFirst)
	if !approx(got[0].Value.(float64), 10.0) {
		t.Fatalf("uint16 scale: got %v, want 10", got[0].Value)
	}

	// int16 negative
	s := Register{Metric: "x", Type: Int16, Scale: 1}
	got, _ = s.Decode([]uint16{0xFFFF}, HighWordFirst)
	if !approx(got[0].Value.(float64), -1) {
		t.Fatalf("int16: got %v, want -1", got[0].Value)
	}
}

func TestDecodeEnum(t *testing.T) {
	r := Register{Metric: "charge.state", Type: Enum,
		Values: map[string]string{"0": "off", "3": "float"}}

	got, _ := r.Decode([]uint16{3}, HighWordFirst)
	if got[0].Value.(string) != "float" {
		t.Fatalf("enum known: got %v", got[0].Value)
	}
	got, _ = r.Decode([]uint16{9}, HighWordFirst)
	if got[0].Value.(string) != "unknown(9)" {
		t.Fatalf("enum unknown: got %v", got[0].Value)
	}
}

func TestDecodeBool(t *testing.T) {
	bit := 2
	r := Register{Metric: "charge.mosfet", Type: Bool, Bit: &bit}
	if got, _ := r.Decode([]uint16{0b100}, HighWordFirst); got[0].Value.(bool) != true {
		t.Fatal("bit set should be true")
	}
	if got, _ := r.Decode([]uint16{0b010}, HighWordFirst); got[0].Value.(bool) != false {
		t.Fatal("bit clear should be false")
	}
	// no bit => whole register nonzero
	w := Register{Metric: "x", Type: Bool}
	if got, _ := w.Decode([]uint16{5}, HighWordFirst); got[0].Value.(bool) != true {
		t.Fatal("nonzero should be true")
	}
	if got, _ := w.Decode([]uint16{0}, HighWordFirst); got[0].Value.(bool) != false {
		t.Fatal("zero should be false")
	}
}

func TestDecodeBitflags(t *testing.T) {
	r := Register{Type: Bitflags, Prefix: "protection",
		Flags: map[string]string{"0": "under_voltage", "2": "over_temp"}}
	got, err := r.Decode([]uint16{0b001}, HighWordFirst) // bit0 set, bit2 clear
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 flag readings, got %d", len(got))
	}
	if find(t, got, "protection.under_voltage").Value.(bool) != true {
		t.Fatal("under_voltage should be set")
	}
	if find(t, got, "protection.over_temp").Value.(bool) != false {
		t.Fatal("over_temp should be clear")
	}
}

func TestDecodeArray(t *testing.T) {
	r := Register{Metric: "cell.voltage", Type: Array, Element: Uint16, Count: 3, Scale: 0.001, Unit: "V"}
	if rc := r.RegisterCount(); rc != 3 {
		t.Fatalf("array RegisterCount: got %d, want 3", rc)
	}
	got, err := r.Decode([]uint16{3200, 3210, 3195}, HighWordFirst)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 readings, got %d", len(got))
	}
	if !approx(find(t, got, "cell.voltage.1").Value.(float64), 3.2) {
		t.Fatal("cell 1")
	}
	if !approx(find(t, got, "cell.voltage.3").Value.(float64), 3.195) {
		t.Fatal("cell 3")
	}
}

func TestRegisterValidate(t *testing.T) {
	bad := []Register{
		{Metric: "x", Type: Enum},                          // enum without values
		{Metric: "x", Type: Array, Element: Uint16},        // array without count
		{Metric: "x", Type: Array, Element: Enum, Count: 2}, // array non-numeric element
		{Prefix: "p", Type: Bitflags},                      // bitflags without flags
		{Metric: "x", Type: "weird"},                       // unknown type
		{Type: Float32},                                    // numeric without metric
	}
	for i, r := range bad {
		if err := r.validate(i); err == nil {
			t.Fatalf("register %d (%+v) should have failed validation", i, r)
		}
	}

	ok := []Register{
		{Metric: "x", Type: Float32, Scale: 1},
		{Metric: "x", Type: Enum, Values: map[string]string{"0": "a"}},
		{Metric: "x", Type: Bool},
		{Prefix: "p", Type: Bitflags, Flags: map[string]string{"0": "f"}},
		{Metric: "x", Type: Array, Element: Uint16, Count: 4},
	}
	for i, r := range ok {
		if err := r.validate(i); err != nil {
			t.Fatalf("register %d (%+v) should be valid: %v", i, r, err)
		}
	}
}
