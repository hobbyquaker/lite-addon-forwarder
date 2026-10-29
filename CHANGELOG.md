# Changelog

## [0.1.0] - 2026-10-30

The first release, for openccu-lite.

- **Sources:** the system's datapoint event stream (resumed with its last id after a restart, re-seeded after a gap),
  a snapshot of every current value at start, the device and channel names, rooms and functions from the metadata
  (kept live), types and units from the device descriptions.
- **Selections:** any number, each a set of ticked devices and channels plus rules by interface, device type, channel
  type, datapoint key (globs), room, function and source, with exclusions; a live count and a preview of what matches.
- **Targets:** any number, each with its selection; add, edit, duplicate, enable/disable, test, preview, status
  (connected, last error, lag, queued, sent, dropped, retries). Per target a bounded queue in memory, batching by size
  or time, retries with backoff and jitter, at-least-once and in order; secrets in a 0600 file, write-only in the page.
- **InfluxDB** 1 (`/write`) and 2/3 (`/api/v2/write`) with the line protocol: the measurement, tags and typed fields
  as Jinja templates, three presets (clean, one measurement, per device), gzip, TLS with an own CA.
- **HTTP webhook:** JSON or NDJSON batches, a URL template, a body template per record, Basic or Bearer, own headers.
- **Templates:** Jinja syntax (gonja), sandboxed, with the record's fields as variables and `slug`, `date`, `unix`,
  `unix_ms`, `iso` as helpers; every template field renders live against the recent records.
- The page in German and English, light and dark.
