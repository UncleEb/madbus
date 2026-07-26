# Madbus

> Universal telemetry normalization for energy systems.

Madbus is an open-source telemetry normalization platform designed to bridge the gap between industrial hardware and modern energy dashboards.

Its primary goal is simple:

**Read data from supported hardware, normalize it into a consistent schema, and expose it through a clean HTTP REST API.**

Madbus is designed to run independently from any dashboard or visualization software. It communicates directly with supported devices (initially via Modbus RTU over RS-485), translates vendor-specific registers into standardized values, and serves those values over the network for any compatible client.

While Madbus is being developed alongside the Sola dashboard, it is intended to remain a standalone project that can be integrated into other software as well.

---

# Philosophy

Most energy hardware exposes data differently.

Every manufacturer chooses different register layouts, scaling factors, byte ordering, units, and naming conventions.

Madbus exists to eliminate that complexity.

Instead of requiring every application to understand every device, Madbus performs the translation once and presents a consistent interface to all clients.

```
Supported Device
        │
        ▼
   Device Driver
        │
        ▼
Normalization Layer
        │
        ▼
 REST API (JSON)
        │
        ▼
Dashboard / Automation / Custom Software
```

Applications should never need to know that one inverter stores battery voltage in register 40125 while another stores it in register 27 multiplied by 0.01.

That complexity belongs inside Madbus.

---

# Goals

* Support a wide variety of energy hardware
* Normalize device-specific telemetry into a consistent schema
* Expose data through a simple HTTP REST API
* Remain lightweight enough to run on inexpensive hardware such as a Raspberry Pi
* Make adding support for new devices straightforward through reusable profiles
* Keep dashboards independent from hardware implementation details

---

# Initial Hardware Support

Development will begin with devices communicating over:

* Modbus RTU (RS-485)

Additional transports may be added in the future, including:

* Modbus TCP
* CAN Bus
* Serial devices
* Bluetooth
* MQTT (if future use cases justify it)
* Vendor-specific protocols

Madbus is protocol-agnostic internally. Protocol drivers simply provide data to the normalization layer.

---

# Normalization

Madbus does **not** expose raw device registers as its primary interface.

Instead, device values are mapped into standardized concepts.

Example:

| Vendor Register | Normalized Value |
| --------------- | ---------------- |
| Register 37     | `ac.power`       |
| Register 1024   | `ac.power`       |
| Register 0xA14  | `ac.power`       |

Clients consume normalized values rather than vendor-specific register addresses.

Custom values are also supported.

Users may create mappings for values outside the standard schema. Those values remain available through the REST API, even if client software such as Sola does not currently understand them.

---

# Device Profiles

Madbus uses configurable device profiles.

Each profile defines:

* Device type
* Communication settings
* Register mappings
* Data types
* Byte ordering
* Scaling factors
* Units
* Optional conversion logic

Profiles can be created through the web interface without modifying source code.

---

# REST API

Madbus exposes normalized telemetry through a versioned REST API under `/api/v1`. All responses are JSON. Clients never register, subscribe, or hold connections — they simply request the latest values.

| Method & path | Description |
| --- | --- |
| `GET /api/v1/health` | Liveness, uptime, and device count. |
| `GET /api/v1/devices` | List configured devices with online status and last-online time. |
| `GET /api/v1/devices/{id}/measurements` | Current normalized measurements for one device. |
| `POST /api/v1/measurements` | Batch read — request a set of devices (optionally narrowed to specific metrics) and receive their current values in one response. This is the primary endpoint for polling clients such as Sola. |

Each measurement is self-describing, carrying its own unit:

```json
{ "ac.power": { "value": 1832.0, "unit": "W" } }
```

The full contract — request/response shapes, the batch selector format, offline/stale semantics, and the normalized metric vocabulary — is documented in [docs/api.md](docs/api.md).

---

# Web Interface

Madbus includes a built-in web interface for:

* Initial setup
* Device discovery
* Communication configuration
* Register mapping
* Profile management
* Diagnostics
* Live telemetry
* System status

Once configured, normal operation requires no direct interaction with the web interface.

---

# Planned Distribution Options

Madbus is intended to be available in several deployment formats.

## Docker (Recommended)

The standard deployment will be a Docker container with an accompanying Docker Compose configuration.

This provides the easiest installation for users already running self-hosted services.

## Standalone

A standalone executable will be provided for users who prefer not to use Docker.

No database is required. Configuration and device profiles are plain JSON files, and current readings are held in memory — Madbus stores only each device's last-online time across restarts.

## Madbus OS (Long-Term Goal)

The most ambitious deployment target is a dedicated Raspberry Pi operating system image.

The image would boot directly into Madbus with no desktop environment.

Initial setup would occur over Bluetooth using a companion mobile application to configure Wi-Fi credentials.

Once connected to the local network, all future configuration would be performed through the Madbus web interface.

The experience is intended to be similar to appliances such as Victron Venus OS.

---

# Technology Stack

Core Application

* Go

Web Interface

* HTML
* CSS
* JavaScript

Storage

* JSON files for configuration and device profiles
* Current readings held in memory (no historical telemetry — that is Sola's role)
* No database

Deployment

* Docker
* Docker Compose
* Native binaries
* Raspberry Pi OS image (planned)

---

# Relationship to Sola

Madbus and Sola are complementary but independent projects.

Madbus is responsible for communicating with hardware and presenting normalized telemetry.

Sola is responsible for storing, analyzing, and visualizing that telemetry.

Neither project depends on the internal implementation of the other.

Their shared contract is the Madbus REST API and the normalized telemetry schema.

---

# Status

Madbus is in active development.

The initial milestone is support for a Modbus RTU energy meter connected through an RS-485 to USB adapter, establishing the core architecture that future device drivers will build upon.
