# Device profile templates

One starting-point profile per device **category**, pre-filled with that
category's canonical normalized metrics (see [`../docs/device-categories.md`](../docs/device-categories.md)).

These are **scaffolds, not runnable profiles.** Madbus only loads the `profiles/`
directory, never this one, so nothing here is polled. The register **addresses,
`register_type`, `word_order`, and comms settings are placeholders** — they must
be set from your device's Modbus documentation.

## Using a template

1. Copy the template for your device's category into `profiles/`:
   `cp templates/bms.json profiles/my-bms.json`
2. Change the `id` (it must be unique; drop the `template-` prefix).
3. Set each register's `address` (and `register_type` / `word_order`) from the
   device's register map. Delete metrics the device doesn't report; add custom
   keys for anything outside the canonical set.
4. For `enum` / `bitflags`, map the device's raw codes/bits to the labels.
5. Point a device at the profile in `config.json` and run.

## Value kinds in these templates

- **number** — scaled numeric (`float32`/`uint16`/… × `scale`).
- **enum** — raw integer → label string (`charge.state`, `inverter.mode`).
- **bool** — a flag; whole register nonzero, or a single `bit`.
- **bitflags** — one register → many named boolean metrics (`protection.*`).
- **array** — repeated numeric elements → `metric.1 … metric.N` (per-cell voltages).

Every template here is loaded and validated by the test suite, so they stay
valid as the profile format evolves.
