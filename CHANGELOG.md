# Changelog

## [0.2.0] - 2026-10-30

- **Statistics and system metrics** as metric records, in thirteen groups a selection ticks (only ticked groups are
  polled, every 60 s by default): the service messages per type and interface, the RPC events per interface with
  their rate, the radio modules' duty cycle, carrier sense and connection, the callback subscribers' reachability and
  stalls, the services' and addons' state, observed restarts and memory - from the system; CPU and load, memory and
  swap, disk IO, storage, network, temperature and uptime from the machine. The addon now asks for `system:read`.
- **Elasticsearch / OpenSearch:** `_bulk` with the index name and the document as templates, `@timestamp`, index or
  data stream, an ingest pipeline, Basic, API key or Bearer, gzip, and an index template installed on the first
  connection that maps the record's fields with fixed types. A refused document is dropped and counted, a 429 or
  server error retries the batch.
- **Prometheus:** a scrape endpoint at `/addons/forwarder/metrics/<target id>`, protected by a token the page
  generates, with the newest value per series and the forwarder's own figures; or remote_write (protobuf in snappy)
  to Prometheus, VictoriaMetrics, Mimir, Thanos or Grafana Cloud. The metric name and the labels are templates.
- **Overflow on disk:** when a target's memory queue is full the records go to `var/spill/<target id>/` (50 MB per
  target by default, adjustable, 0 switches it off), and a restart of the addon goes on where it stopped; when the
  overflow is full the oldest are dropped and counted. Nothing is written while a target keeps up.
- The selection editor lists the metric groups with their descriptions; the Influx presets tag every label of a
  metric (a tag named `*`); the targets show what waits on disk; the system page shows the last poll, the groups and
  their errors, and the poll interval.

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
