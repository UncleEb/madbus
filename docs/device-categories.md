# Madbus Device Categories

The canonical vocabulary of device **categories** and their normalized metrics.
This is the reference that device profiles, shared community profiles, and
downstream consumers (Sola) all build against.

> Status: living reference. The profile-schema additions (`category`,
> `schema_version`) and the non-numeric value kinds (`enum`, `bool`, `bitflags`,
> indexed `array`) are **implemented** as of schema v2. Category *vocabularies*
> below are stable to build against; per-category register maps and templates
> are filled in as hardware is acquired.

---

## Why categories exist

The device **category is the contract with Sola, not the vendor.** Sola renders
a widget based on a device's category + normalized metrics, so any RS-485 device
a user can map to a category looks identical on the dashboard regardless of who
made it. The vendor becomes an implementation detail.

### Two layers — keep them separate

1. **Category spec** (this document) — the canonical set of normalized metric
   keys for a category. Curated here; shared by all devices of that category;
   what Sola renders against.
2. **Device profile** (`profiles/*.json`) — a concrete mapping of *one device's*
   registers onto those keys. User-authored, or downloaded and shared.

A **template** is the bridge: a scaffold pre-stubbed with a category's standard
keys and blank register addresses. A user picks a category, fills in the
addresses from their device's manual, and any key they don't map simply comes
back `null`. Values outside the canonical set are always allowed as **custom
keys** (same shape, same conventions) — a consumer reads the keys it understands
and ignores the rest.

---

## Profile schema additions

Every profile gains two fields:

```json
{
  "id": "victron-smartshunt",
  "category": "shunt",
  "schema_version": 1,
  "name": "...",
  "register_type": "input",
  "word_order": "high_first",
  "registers": [ ... ]
}
```

- `category` — one of `meter`, `charge_controller`, `shunt`, `inverter`, `bms`.
  Tells the consumer which widget/readout applies.
- `schema_version` — integer, lets shared profiles stay parseable as the format
  evolves.

---

## Metric conventions

- **Flat, dot-namespaced keys** (`battery.voltage`, `pv.power`). The vocabulary
  is **shared and orthogonal** to category — `battery.*` appears in shunts, BMSs,
  and charge controllers alike. The category decides which bundle applies.
- **Canonical units** — each metric normalizes to one unit (V, A, W, VA, var,
  Hz, %, kWh, Ah, °C, s). Values are self-describing (carry their unit).
- **Core vs optional** — `core` metrics are what Sola needs to render the
  category's widget. Optional metrics are nice-to-haves a device may or may not
  report. A device missing an optional key just returns `null` for it.
- **Sign conventions** — currents that can flow both ways use **+ charging /
  − discharging** (battery, shunt) and **+ import / − export** where applicable.

### Value kinds (decoder must support)

Meters are all scaled numbers; the other categories are not. The profile decoder
grows these value kinds:

| Kind | Meaning | Normalized value | Sketch |
|------|---------|------------------|--------|
| `number` | scaled numeric (today's behavior) | number | `{"type":"float32","scale":1,"unit":"V"}` |
| `enum` | one of a fixed set of states | string label (+ optional numeric `code`) | `{"type":"enum","values":{"0":"off","3":"float"}}` |
| `bool` | on/off, optionally from one bit | boolean | `{"type":"bool","bit":0}` |
| `bitflags` | one register → many named booleans | expands to several `bool` metrics | `{"type":"bitflags","prefix":"protection","flags":{"1":"over_voltage"}}` |
| `array` | repeated/indexed registers (per-cell) | expands to `key.1 … key.N` | `{"metric":"cell.voltage","type":"uint16","count":16,"stride":1,"scale":0.001}` |

> **API impact (done, schema v2):** a measurement `value` is `number | string |
> boolean`. The `unit` field stays for numeric metrics and is empty for
> enum/bool. See `docs/api.md`.

---

## Categories

### `meter` — AC energy meter

Status: **implemented** (single-phase aggregate); per-leg keys in progress.

| Key | Unit/kind | Core | Notes |
|-----|-----------|:---:|-------|
| `ac.voltage` | V | ✓ | |
| `ac.frequency` | Hz | ✓ | |
| `ac.current` | A | ✓ | aggregate / average |
| `ac.power` | W | ✓ | total active power (all legs) |
| `ac.power.apparent` | VA | | total |
| `ac.power.reactive` | var | | total |
| `ac.power_factor` | ratio | | total |
| `ac.energy.import` | kWh | ✓ | |
| `ac.energy.export` | kWh | | |
| `ac.energy.total` | kWh | | |
| `ac.voltage.l1` / `.l2` / `.l3` | V | | per-leg, where reported |
| `ac.current.l1` / `.l2` / `.l3` | A | | per-leg |
| `ac.power.l1` / `.l2` / `.l3` | W | | per-leg active |
| `ac.power.apparent.l1` / `.l2` | VA | | per-leg |
| `ac.power.reactive.l1` / `.l2` | var | | per-leg |
| `ac.power_factor.l1` / `.l2` | ratio | | per-leg |

### `charge_controller` — solar MPPT / charge controller

Status: not yet built. Maps to Sola's existing `charge_controller` type.

| Key | Unit/kind | Core | Notes |
|-----|-----------|:---:|-------|
| `pv.voltage` | V | ✓ | array voltage |
| `pv.current` | A | ✓ | |
| `pv.power` | W | ✓ | |
| `battery.voltage` | V | ✓ | |
| `battery.current` | A | ✓ | + charging / − discharging |
| `battery.power` | W | | |
| `charge.state` | enum | ✓ | off / bulk / absorption / float / equalize / fault |
| `pv.yield.today` | kWh | ✓ | |
| `pv.yield.total` | kWh | | |
| `device.temperature` | °C | | |
| `load.current` | A | | controllers with a load output |
| `load.power` | W | | |
| `error.code` | enum | | device-specific fault codes |

### `shunt` — battery monitor / shunt

Status: not yet built. Maps to Sola's existing `shunt` type.

| Key | Unit/kind | Core | Notes |
|-----|-----------|:---:|-------|
| `battery.voltage` | V | ✓ | |
| `battery.current` | A | ✓ | + charging / − discharging |
| `battery.power` | W | | |
| `battery.soc` | % | ✓ | state of charge |
| `battery.consumed_ah` | Ah | | consumed amp-hours |
| `battery.time_to_go` | s | | seconds to empty (∞ when charging) |
| `battery.temperature` | °C | | |
| `battery.soh` | % | | state of health |
| `battery.voltage.aux` | V | | secondary/starter battery |

### `inverter` — inverter / inverter-charger

Status: not yet built. New readout on the Sola side.

| Key | Unit/kind | Core | Notes |
|-----|-----------|:---:|-------|
| `ac.out.voltage` | V | ✓ | |
| `ac.out.current` | A | ✓ | |
| `ac.out.power` | W | ✓ | |
| `ac.out.frequency` | Hz | | |
| `ac.in.voltage` | V | | AC input / passthrough |
| `ac.in.current` | A | | |
| `ac.in.power` | W | | |
| `ac.in.frequency` | Hz | | |
| `dc.in.voltage` | V | ✓ | battery side |
| `dc.in.current` | A | | |
| `dc.in.power` | W | | |
| `inverter.mode` | enum | ✓ | off / inverting / charging / passthrough / bulk / absorption / float |
| `inverter.load_percent` | % | | |
| `device.temperature` | °C | | |

### `bms` — battery management system

Status: not yet built. New readout on the Sola side. Needs the `enum` / `bool` /
`bitflags` / `array` value kinds most of all.

| Key | Unit/kind | Core | Notes |
|-----|-----------|:---:|-------|
| `battery.voltage` | V | ✓ | pack voltage |
| `battery.current` | A | ✓ | + charging / − discharging |
| `battery.power` | W | | |
| `battery.soc` | % | ✓ | |
| `battery.soh` | % | | |
| `battery.capacity.remaining` | Ah | | |
| `battery.capacity.full` | Ah | | |
| `battery.cycles` | count | | |
| `cell.voltage.min` | V | ✓ | |
| `cell.voltage.max` | V | ✓ | |
| `cell.voltage.delta` | V | | max − min (imbalance) |
| `cell.voltage.{n}` | V (array) | | per-cell, pack-size dependent |
| `cell.temperature.min` / `.max` | °C | | |
| `battery.temperature` | °C | | |
| `charge.mosfet` | bool | | charge FET enabled |
| `discharge.mosfet` | bool | | discharge FET enabled |
| `balancing` | bool | | balancing active |
| `protection.*` | bitflags | | over/under voltage, over-current (chg/dis), over/under temp, short-circuit |

---

## Building against this

- **No hardware needed** to define/refine the vocabularies above — that's the
  point. Register maps get filled in per device as hardware is acquired.
- Implementation order that isn't gated on owning every device:
  1. **[done]** Per-leg keys on the `meter` category (profile-only).
  2. **[done]** `category` + `schema_version` fields, validated on load;
     `category` surfaced on the API device object.
  3. **[done]** Decoder `enum` / `bool` / `bitflags` / `array` kinds; measurement
     value is now `number | string | boolean` (schema v2).
  4. **[done]** Ship a **template** per category (`templates/`). Remaining: wire
     into the (future) web UI.
  5. Fill real register maps as devices arrive; tweak details against hardware.
