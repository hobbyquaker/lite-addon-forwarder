# lite-addon-forwarder

*[Deutsch](README.md)*

Forwards the Homematic devices' events, the system's statistics and the machine's figures to external stores:
**InfluxDB** (1.x, 2.x, 3.x and everything that speaks the line protocol: VictoriaMetrics, QuestDB, Telegraf),
**HTTP webhooks** (JSON or NDJSON), **Elasticsearch / OpenSearch** and **Prometheus** (a scrape endpoint or
remote_write). Any number of targets, reusable selections, configurable schemas through Jinja templates, a status per
target, an overflow to disk while a target is down. An addon for
[openccu-lite](https://github.com/hobbyquaker/openccu-lite).

## What it does

- **Reads every datapoint value** from the system's event stream (resumed after a restart) and labels it with the
  **device name, channel name, rooms and functions** from the metadata, and the type, unit, device type and channel
  type from the device descriptions. Renames and room changes follow live. At start and after a gap in the stream
  **every current value** goes out once (the snapshot), so the stores have a defined starting value (can be switched
  off).
- **Statistics and system metrics** as metric records, in groups a selection ticks (only ticked groups are polled,
  every 60 s, adjustable): *service messages* per type and interface, *RPC events* per interface with their rate,
  *radio* (duty cycle, carrier sense, connection), *reachability* and *stalls* of the callback subscribers,
  *services and addons* (state, observed restarts, memory) - from the system; and *CPU and load*, *memory and swap*,
  *disk IO*, *storage*, *network*, *temperature* and *system* (uptime, processes) from the machine itself. The list
  is below.
- **Selections** (any number) say what is forwarded: tick devices and channels one by one, add rules by interface,
  device type, channel type, datapoint (with wildcards: `*_TEMPERATURE`, `PRESS_*`), room and function, and
  exclusions (datapoints, devices, channels). Within a rule any entry matches, across rules all must, empty rules do
  not restrict. The editor shows **live** how many datapoints, channels and devices match, and the recent matching
  records.
- **Targets** (any number), each with its selection: add, edit, duplicate, enable and disable, **test** (one record
  goes out and the store's answer is shown), **preview** (the recent records as this target sends them) and a
  **status** (connected, last error, lag, queued, sent, dropped).
- **Schemas by template.** Whatever a target names or shapes is a template in the Jinja syntax Home Assistant users
  know: `{{ device_name }}`, `{{ rooms | join(",") }}`, `{{ key | lower }}`, `{% if value > 20 %}…{% endif %}`. For
  InfluxDB the measurement, the tags and the fields (with a type); for the webhook the URL and the body per record;
  for Elasticsearch the index name and the document; for Prometheus the metric name and the labels.
  Every template field shows **live** what it renders for the recent real records, with errors right under it.
  InfluxDB starts from a **preset**: *Clean* (the default: a measurement per datapoint, the address and names as
  tags, the value as a field), *One measurement* or *Per device*; each can be edited afterwards.
- **Delivery:** a queue in memory per target, batches by size or time, retries with growing backoff while the store
  is unreachable; at least once and in order per target. An error retrying cannot mend (the store rejects the data)
  drops the batch and counts it. Wrong credentials or a missing bucket keep the data queued until you fix them. When
  the memory queue is full - the target is down for longer - the records go to an **overflow on disk** (`var/spill/`,
  capped per target, 50 MB by default), and a restart of the addon goes on where it stopped; when the capped overflow
  is full too, the oldest are lost and counted. Nothing is written to the SD card in normal operation.
- **Secrets** (passwords, tokens) are kept in a file only the addon can read and never shown again. TLS with an own
  CA or without the check, per target.

The *Records* page shows what the addon saw last and which targets it went to.

## Installation

Install the package `forwarder-<arch>-<version>.tar.gz` from the releases page through the system's Addons page (or
from the catalogue once it is listed). The addon needs the scopes `rpc:read`, `meta:read` and `system:read`: it reads
the devices, descriptions, values and the event stream of the radio interfaces, the names, rooms and functions, and
the system's statistics (service messages, radio, services). The configuration page is at `/addons/forwarder/` and
behind the addon's Settings button.

## Privacy

The addon sends only to the targets you configured. Without a target it contacts nothing outside the system; there is
no default that sends anywhere, no telemetry, no update check (the system does that). What a target receives is
decided by its selection; the *Preview* shows it before you save.

## The schema in InfluxDB

With the *Clean* preset the addon writes a measurement per datapoint, for example:

```
ACTUAL_TEMPERATURE,interface=HmIP-RF,device=000A1BE9A1B2C3,channel=1,address=000A1BE9A1B2C3:1,device_type=HmIP-eTRV-2,device_name=Heizung\ Bad,channel_name=Heizung\ Bad:1,room=Bad,function=Heizung,unit=°C value=21.4,value_num=21.4 1793282592481000000
STATE,interface=BidCos-RF,device=MEQ0123456,channel=1,address=MEQ0123456:1,device_name=Lampe\ Flur value=true,value_num=1 1793282593000000000
```

`value` carries the value in its own type (numbers always as floats, booleans as booleans, texts as strings),
`value_num` the number for queries across types (booleans 0/1, enums their index), `value_text` the text of an enum
or a string value. The names are tags, so dimensions: a rename starts a new series; if you do not want that, take the
name tags out and query by `address`.

## Elasticsearch and OpenSearch

One `_bulk` per batch with an action line and a document per record. The **index name** is a template (the default
`homematic-{{ kind }}-{{ ts | date('2006.01.02') }}`: an index per kind and day; lower-cased), so is the **document**
(the default is the whole record, plus `@timestamp`); `index` for an index, `create` for a data stream; an optional
ingest pipeline; Basic, API key or Bearer; gzip. On the first connection the addon puts an **index template** for
`homematic-*` that maps the record's fields with fixed types (`value` as keyword, `value_num` as double, `value_bool`
as boolean, `ts` and `@timestamp` as date, texts as keywords), so a value that is a number on one datapoint and a
text on the next cannot break the mapping (can be switched off). A document the store refuses for its content is
dropped and counted; a 429 or a server error retries the batch. Elasticsearch 7 and 8, OpenSearch 1 and 2.

## Prometheus

Two modes per target:

- **scrape:** the addon keeps the newest value per series and the system serves it at
  `/addons/forwarder/metrics/<target id>` - through the system's web server with its certificate, behind its login:
  Prometheus needs an **API token of the system with the scope `addon:forwarder`** (System → Tokens) and sends it as
  a Bearer credential (`authorization: credentials: <token>` in the job). The forwarder's own figures follow
  (`forwarder_target_sent_total`, `…_dropped_total`, `…_queued`, `…_spilled`, `forwarder_source_events_total`, …). A
  series without a new record can be left out after a staleness period.
- **remote_write:** the addon pushes the records as a `prometheus.WriteRequest` (protobuf in snappy) to
  `/api/v1/write` of Prometheus (with `--web.enable-remote-write-receiver`), VictoriaMetrics, Mimir, Thanos or Grafana
  Cloud; Basic or Bearer.

The **metric name** and the **labels** are templates; the *Clean* preset turns an event into
`homematic_<datapoint>{interface,address,device_type,device_name,channel_name,room,function}` and a system metric into
`<group>_<name>{…its labels…}`. A label named `*` stands for every label of a metric. Characters Prometheus does not
allow become `_`; only records with a number are exported (booleans 0/1, enums their index).

## The metric groups

Every metric carries the label `host`; the table names the others. Counters end in `_total`, rates come from the
delta to the previous poll (the first poll yields the counters only).

| Group | Source | Metrics |
| --- | --- | --- |
| `service_messages` | system | `total`; `count{type}`; `by_interface{interface}` |
| `rpc_events` | system | `received_total{interface}` (what the addon received), `rate_per_minute{interface}`; `interface_rate_per_minute{interface}`, `interface_up{interface}` (as the system counts them per interface process) |
| `radio` | system | `duty_cycle_percent`, `carrier_sense_percent`, `connected` `{interface,address,type}`; `busy`; `errors` |
| `reachability` | system | `reachable{interface,id,url}`; `unreachable_total` |
| `stalls` | system | `stalled{interface,kind}`; `total` |
| `services` | system | `running`, `failed`, `restarts_total` (observed since the addon started), `rss_bytes` `{unit,kind,category}` |
| `cpu` | machine | `usage_percent{mode}` (total, user, system, iowait, steal), `load{period}` (1, 5, 15), `count`, `running_tasks` |
| `memory` | machine | `total_bytes`, `available_bytes`, `used_bytes`, `used_percent`, `free_bytes`, `buffers_bytes`, `cached_bytes`, `swap_total_bytes`, `swap_used_bytes`, `swap_used_percent` |
| `disk_io` | machine | `read_bytes_total`, `write_bytes_total`, `reads_total`, `writes_total`, `read_bytes_per_second`, `write_bytes_per_second`, `reads_per_second`, `writes_per_second`, `busy_percent` `{device}` |
| `storage` | machine | `total_bytes`, `used_bytes`, `free_bytes`, `used_percent`, `inodes_used_percent` `{mount,device,fstype}` |
| `network` | machine | `rx_bytes_total`, `tx_bytes_total`, `rx_packets_total`, `tx_packets_total`, `rx_errors_total`, `tx_errors_total`, `rx_dropped_total`, `tx_dropped_total`, `rx_bytes_per_second`, `tx_bytes_per_second` `{interface}` |
| `temperature` | machine | `celsius{sensor,source}` |
| `system` | machine | `uptime_seconds`, `boot_time_seconds`, `processes`, `tasks`, `open_files` |

In InfluxDB the *Clean* preset makes the group the measurement, the labels tags (the `*` tag) and the metric name the
field: `cpu,host=ccu,mode=total usage_percent=12.5`. In Elasticsearch a metric is a document with `group`, `name`,
`labels` and `value_num`.

## Templates

The variables are the record's fields: `ts` (a time object; `ts | date("2006-01-02 15:04")`, `ts | unix`,
`ts | unix_ms`, `ts | iso`), `ts_rfc3339`, `ts_unix`, `ts_ms`, `ts_ns`, `kind`, `interface`, `device`, `channel`,
`address`, `key`, `value`, `value_num`, `value_text`, `value_bool`, `type`, `unit`, `device_type`, `channel_type`,
`device_name`, `channel_name`, `rooms`, `functions`, `source`, for metrics `group`, `name` and `labels`, and `record`
(the whole record: `{{ record | tojson }}`).
Plus Jinja's filters (`join`, `lower`, `upper`, `default`, `replace`, `round`, `int`, `float`, `string`, `first`,
`last`, `length`, `tojson`, …) and `slug` (a name as an identifier: `heizung_bad_1`). The templates are sandboxed: no
`include`/`import`, no file or network access, limits on time and output. The configuration page lists everything
under *Variables and filters*.

## Webhook

One `POST` (or `PUT`/`PATCH`) per batch with a JSON array of the rendered bodies, or, as NDJSON, one line per record;
Basic or Bearer authentication, own headers, gzip. The URL is a template itself (`https://host/hook/{{ kind }}`); a
batch is grouped by the rendered URL. That reaches n8n, Node-RED, Logstash/Vector/Fluent Bit and your own services.

## Files

`/usr/local/addons/forwarder/etc/forwarder.json` (the settings, selections and targets), `etc/secrets.json` (0600, the
secrets) and `var/spill/<target id>/` (a target's overflow while it is down). An uninstall keeps the settings for a
reinstall. The API is described in [docs/api.md](docs/api.md).

## Development

Go 1.25+ and Node 22+. `go test -race ./...`; in `ui/`: `npm ci`, `npm run check`, `npm test`, `npm run build`,
`npm run e2e` (Playwright against the real daemon on a simulated system, `internal/devserver`).
`FORWARDER_DOCKER=1 go test -run TestRoundTrip ./internal/roundtrip` writes and reads against InfluxDB 1.8 and 2.7,
OpenSearch 2, Elasticsearch 8 and VictoriaMetrics in docker. `VERSION=0.2.0 scripts/build.sh` builds the packages for
aarch64, x86_64 and armv7l. The addon stands on
[ccu-addon-kit](https://github.com/hobbyquaker/ccu-addon-kit) and [occulite-ui](https://github.com/hobbyquaker/occulite-ui).

## License

MIT, see [LICENSE](LICENSE).
