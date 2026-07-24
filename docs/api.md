# Madbus REST API (v1)

The contract between Madbus and any telemetry client (Sola and others). Madbus
reads supported hardware, normalizes vendor-specific registers into a consistent
schema, and serves the result here. Clients consume normalized values and never
see raw registers.

All paths are versioned under `/api/v1`. All responses are `application/json`.

---

## Conventions

- **Timestamps** are RFC3339 in UTC, e.g. `2026-07-23T14:05:00Z`.
- **Device IDs** are stable string slugs, e.g. `meter-1`. They are assigned by
  Madbus and never reused, so a client can rely on an ID identifying the same
  physical device over time.
- **Metric keys** are flat, dot-namespaced strings, e.g. `ac.power`,
  `battery.soc`. Standard keys are listed under [Normalized metrics](#normalized-metrics).
  Custom/user-defined keys use the same shape and are returned alongside standard
  ones; a client reads the keys it understands and ignores the rest.
- **Values are self-describing.** Every measurement carries its own `unit`, so a
  client never needs out-of-band knowledge to interpret a value.

### Measurement value

A measurement is an object:

```json
{ "value": 1832.0, "unit": "W" }
```

| Field   | Meaning |
|---------|---------|
| `value` | The normalized numeric value, or `null` if this metric has never been successfully read. |
| `unit`  | The unit of `value` (`W`, `V`, `A`, `Hz`, `kWh`, `%`, ...). Always present, even when `value` is `null`. |
| `stale` | Optional. `true` when `value` is a last-known reading that was **not** refreshed on the most recent poll. Omitted (implicitly `false`) when the value is fresh. |

So three states are distinguishable:

```json
{ "value": 1832.0, "unit": "W" }                 // fresh, read this cycle
{ "value": 1832.0, "unit": "W", "stale": true }  // last-known, not refreshed this cycle
{ "value": null,   "unit": "W" }                 // never successfully read
```

### Device object

Returned wherever a device appears:

```json
{
  "id": "meter-1",
  "name": "Main Load Meter",
  "profile": "eastron-sdm630",
  "online": true,
  "last_read": "2026-07-23T14:05:00Z"
}
```

| Field       | Meaning |
|-------------|---------|
| `id`        | Stable slug (see above). |
| `name`      | Human-readable name, configurable in the web UI. |
| `profile`   | The device profile driving normalization for this device. |
| `online`    | `true` if the most recent poll of this device succeeded. |
| `last_read` | Timestamp of the last **successful** read. When `online` is `false`, this shows the age of any last-known values. |

An offline device is never dropped from a response — it returns with `online:
false` and its last-known measurements marked `stale`, so a client can render it
as stale rather than having it vanish.

---

## Endpoints

### `GET /api/v1/health`

Liveness and a coarse summary.

```json
{
  "status": "ok",
  "uptime_seconds": 84213,
  "device_count": 1,
  "time": "2026-07-23T14:05:00Z"
}
```

### `GET /api/v1/devices`

List configured devices (identity/metadata only, no measurements).

```json
{
  "devices": [
    { "id": "meter-1", "name": "Main Load Meter", "profile": "eastron-sdm630",
      "online": true, "last_read": "2026-07-23T14:05:00Z" }
  ]
}
```

### `GET /api/v1/devices/{id}/measurements`

All current measurements for a single device. Convenience/debug endpoint.

```json
{
  "device": {
    "id": "meter-1", "name": "Main Load Meter", "profile": "eastron-sdm630",
    "online": true, "last_read": "2026-07-23T14:05:00Z"
  },
  "measurements": {
    "ac.power":         { "value": 1832.0,  "unit": "W"   },
    "ac.voltage":       { "value": 241.2,   "unit": "V"   },
    "ac.current":       { "value": 7.6,     "unit": "A"   },
    "ac.frequency":     { "value": 60.0,    "unit": "Hz"  },
    "ac.energy.import": { "value": 10432.5, "unit": "kWh" }
  }
}
```

Returns `404` if `{id}` is unknown.

### `POST /api/v1/measurements`  — batch read

The primary endpoint for polling clients. The body is a list of selectors; the
response returns all matching devices' readings in a single round-trip.

**Request**

```json
{
  "devices": [
    { "id": "meter-1", "metrics": ["ac.power", "ac.voltage"] },
    { "id": "meter-2" }
  ]
}
```

- A selector with no `metrics` returns **all** of that device's measurements.
- An empty list (`{"devices": []}`) or an empty body returns **all** devices
  with all measurements (whole-system poll).

**Response** — always `200`; unknowns are reported, not fatal (lenient).

```json
{
  "read_at": "2026-07-23T14:05:00Z",
  "devices": [
    {
      "device": { "id": "meter-1", "name": "Main Load Meter", "profile": "eastron-sdm630",
                  "online": true, "last_read": "2026-07-23T14:05:00Z" },
      "measurements": {
        "ac.power":   { "value": 1832.0, "unit": "W" },
        "ac.voltage": { "value": 241.2,  "unit": "V" }
      }
    },
    {
      "device": { "id": "meter-2", "name": "Battery Shunt", "profile": "victron-shunt",
                  "online": false, "last_read": "2026-07-23T14:04:30Z" },
      "measurements": {
        "battery.voltage": { "value": 52.3, "unit": "V", "stale": true },
        "battery.soc":     { "value": 87,   "unit": "%", "stale": true }
      }
    }
  ],
  "unmatched": {
    "devices": ["meter-99"],
    "metrics": { "meter-1": ["ac.frequency"] }
  }
}
```

- `read_at` is the time Madbus assembled the response.
- `unmatched.devices` lists any requested device `id` that does not exist.
- `unmatched.metrics` maps a device `id` to any requested metric keys that device
  does not expose. Both are omitted when empty. This surfaces config drift
  between client and Madbus instead of silently swallowing it.

---

## Normalized metrics

Starter vocabulary. This grows as device support grows; custom keys follow the
same conventions. Units shown are the canonical unit Madbus normalizes to.

| Key                 | Unit  | Meaning |
|---------------------|-------|---------|
| `ac.power`          | W     | Active AC power. |
| `ac.voltage`        | V     | AC voltage. |
| `ac.current`        | A     | AC current. |
| `ac.frequency`      | Hz    | AC line frequency. |
| `ac.energy.import`  | kWh   | Cumulative imported energy. |
| `ac.energy.export`  | kWh   | Cumulative exported energy. |
| `battery.voltage`   | V     | Battery voltage. |
| `battery.current`   | A     | Battery current (sign: + charging, − discharging). |
| `battery.power`     | W     | Battery power. |
| `battery.soc`       | %     | State of charge. |
| `pv.voltage`        | V     | PV array voltage. |
| `pv.current`        | A     | PV array current. |
| `pv.power`          | W     | PV array power. |
| `pv.yield.today`    | kWh   | Energy harvested today. |
