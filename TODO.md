# Madbus TODO

## Surface device read failures in the API + web UI (don't fail silently)

When a device can't be read, Madbus currently only flips it to `online: false`
(`Store.RecordFailure` in `internal/telemetry/telemetry.go`) and **discards the
underlying error**. The most common cause on Linux is a serial-port permission
failure: the Madbus process isn't in the `dialout` group, so `ensureOpen()` in
`internal/rtu/reader.go` returns `open serial /dev/ttyUSB0: permission denied`.

From the outside this looks like a mysterious offline device with no explanation,
and new users burn real time on it (the README "Serial Port Access (Linux)"
section documents the `dialout` workaround, but Madbus itself should say so).

**To do:**

- [x] Retain the last read/open error per device in `RecordFailure`
  (`DeviceState.LastError`). — done 2026-07-27.
- [x] Expose it via the API — `last_error` on the device object (omitted when
  online). — done 2026-07-27.
- [x] Log the offline edge with its reason (first failure, recovery, or changed
  reason) so a disconnected/never-connected device is visible in the terminal
  instead of silent, then retries quietly. — done 2026-07-27. (Reconnection
  itself already worked — verified: a device connected after startup comes online
  within a poll.)
- **Web UI (not yet built) — the important one:** when a user opens the Madbus
  page and a device is offline because of a port-open/permission error, show a
  clear, prominent diagnostic (e.g. *"Cannot open /dev/ttyUSB0: permission denied
  — add the Madbus user to the `dialout` group"*) with the fix, rather than a
  silent offline state. `last_error` now carries exactly this text.
- Consider a startup self-check that loudly logs/flags an unreadable serial port
  (permission denied vs. no such device vs. busy) so it's obvious even before the
  web UI exists.

## Expose every documented value for a device category (start with meters: per-leg / split-phase)

A device category should pipe **every reading the device documentation exposes**
that we can reasonably anticipate — regardless of whether a given value happens
to be present on the hardware currently connected in the test rig. Exposing only
a subset isn't honest to the consumer (Sola, and ultimately the user) about what
the device actually reports; a downstream reader can't tell "this device doesn't
have that value" from "Madbus just didn't bother to decode it."

Concrete first case — **split-phase / per-phase energy meter values.** The
generic-meter profile currently emits aggregate `ac.*` only (`ac.voltage`,
`ac.current`, `ac.power`, `ac.power.apparent`, `ac.power.reactive`,
`ac.power_factor`, `ac.energy.import`/`export`/`total`). Split-phase (240V) and
three-phase meters expose per-leg registers that we're dropping on the floor.

**To do:**

- [x] **Meter per-leg keys added** (2026-07-27) — `generic-meter.json` now emits
  `ac.current.l1`/`.l2`, `ac.power.l1`/`.l2`, and per-leg apparent/reactive/pf
  alongside the aggregates. Verified live. Voltage/frequency stay single (this
  meter reports one of each).
- Apply the same "decode everything documented" pass to the other categories as
  they come online — see `docs/device-categories.md` for the canonical
  per-category vocabulary.

Downstream: Sola will ingest per-leg values and render L1/L2 once these keys
exist (tracked in Sola's own `TODO.md`).

## Device-category taxonomy — implementation

Reference: `docs/device-categories.md` (canonical metric vocabulary per category:
meter / charge_controller / shunt / inverter / bms). Build order — nothing but
the final register maps needs hardware:

1. [x] Per-leg keys on the `meter` category (profile-only). — done 2026-07-27
2. [x] Add `category` + `schema_version` fields to the profile schema
   (`internal/profile`), validated on load, and surface `category` on the API
   device object. — done 2026-07-27
3. [x] Extend the decoder with non-numeric value kinds — `enum` (charge.state,
   inverter.mode), `bool` (MOSFETs, balancing), `bitflags` (protection/alarm
   registers → many named booleans), indexed `array` (per-cell voltages). — done
   2026-07-27. Measurement `value` is now `number | string | boolean`; schema
   bumped to v2; `docs/api.md` updated; unit tests in `internal/profile`.
4. [x] Ship a template profile per category (all standard keys, placeholder
   addresses). — done 2026-07-27 (`templates/` + drift-guard test). Remaining:
   wire templates into the future web UI (device discovery / mapping).
5. [partial] Fill real register maps per device. **Meter done + hardware-verified**
   (`profiles/generic-meter.json`). Remaining: charge_controller / shunt / bms /
   inverter — each needs the physical device + its register documentation (do NOT
   invent addresses). Gated on hardware acquisition.

## Web UI

Build the built-in web interface (README "Web Interface"): initial setup, device
discovery, comms config, register mapping, profile management, diagnostics, live
telemetry, status.

Done so far:
- [x] **Home screen** (embedded `web/`, served at `/`): logo, nav, device list
  with connection status + `last_error`. Read-only. (2026-07-28)
- [x] **Settings page + write API** (`GET`/`PUT /api/v1/settings`): default poll
  interval, debug, listen address. Atomic `config.json` writes; poll loop live-
  reloads settings each tick (interval + debug apply live; listen addr on
  restart). Changing the listen port pre-flight-checks it's bindable and rejects
  (409, not saved) if it's already in use. (2026-07-28)
- [x] **Device management** — engine (`poller.go`: live reconcile, per-bus
  scheduler, per-device `poll_interval_seconds`), CRUD API (`POST`/`PUT`/`DELETE
  /api/v1/devices`, `GET /api/v1/profiles`, validation), and home-screen UI
  (+ Add device, edit/delete, form dialog). (2026-07-29)
- [x] **Hardware Interface discovery** (`GET /api/v1/serial-ports`, sysfs USB
  descriptions) — dropdown of detected USB-serial bridge adapters (ttyUSB, with
  descriptions like "FTDI USB-RS485-WE"); CDC/ttyACM devices (Z-Wave sticks etc.)
  filtered out; Mock option; Rescan; saved-but-unplugged port preserved. (2026-07-29)

Remaining (next session — Levi's priorities):
- **Clean up label spacing on the Add/Edit Device form** (the dialog in
  `web/index.html` / `web/style.css`).
- **Device value mapping page** — a UI to map an individual device's registers to
  normalized metric keys (i.e. author/edit a profile from the web UI). This is the
  guided mapping flow below.

Remaining (rest):
- **Wire the category templates (`templates/`) into a guided mapping flow:** pick
  a category -> template scaffold -> assign register addresses/comms -> save as a
  profile. (This is the "device value mapping page" above.)
- **Test the template -> real-profile mapping flow against MORE devices.** So far
  only the meter has been mapped and verified against hardware. The mapping UX
  and the non-numeric value kinds (enum / bool / bitflags / array) need exercising
  against real charge controllers / shunts / BMSs / inverters as they're acquired.
- Surface device read/permission errors prominently in the UI (ties into the
  dialout item above).
