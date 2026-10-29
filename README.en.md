# lite-addon-forwarder

*[Deutsch](README.md)*

Forwards the Homematic devices' events to external stores: **InfluxDB** (1.x, 2.x, 3.x and everything that speaks
the line protocol: VictoriaMetrics, QuestDB, Telegraf) and **HTTP webhooks** (JSON or NDJSON). Any number of targets,
reusable selections, configurable schemas through Jinja templates, a status per target. An addon for
[openccu-lite](https://github.com/hobbyquaker/openccu-lite).

## What it does

- **Reads every datapoint value** from the system's event stream (resumed after a restart) and labels it with the
  **device name, channel name, rooms and functions** from the metadata, and the type, unit, device type and channel
  type from the device descriptions. Renames and room changes follow live. At start and after a gap in the stream
  **every current value** goes out once (the snapshot), so the stores have a defined starting value (can be switched
  off).
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
  InfluxDB the measurement, the tags and the fields (with a type); for the webhook the URL and the body per record.
  Every template field shows **live** what it renders for the recent real records, with errors right under it.
  InfluxDB starts from a **preset**: *Clean* (the default: a measurement per datapoint, the address and names as
  tags, the value as a field), *One measurement* or *Per device*; each can be edited afterwards.
- **Delivery:** a queue in memory per target, batches by size or time, retries with growing backoff while the store
  is unreachable; at least once and in order per target. An error retrying cannot mend (the store rejects the data)
  drops the batch and counts it. Wrong credentials or a missing bucket keep the data queued until you fix them.
  Nothing is written to the SD card in normal operation.
- **Secrets** (passwords, tokens) are kept in a file only the addon can read and never shown again. TLS with an own
  CA or without the check, per target.

The *Records* page shows what the addon saw last and which targets it went to.

## Installation

Install the package `forwarder-<arch>-<version>.tar.gz` from the releases page through the system's Addons page (or
from the catalogue once it is listed). The addon needs the scopes `rpc:read` and `meta:read`: it reads the devices,
descriptions, values and the event stream of the radio interfaces, and the names, rooms and functions. The
configuration page is at `/addons/forwarder/` and behind the addon's Settings button.

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

## Templates

The variables are the record's fields: `ts` (a time object; `ts | date("2006-01-02 15:04")`, `ts | unix`,
`ts | unix_ms`, `ts | iso`), `ts_rfc3339`, `ts_unix`, `ts_ms`, `ts_ns`, `kind`, `interface`, `device`, `channel`,
`address`, `key`, `value`, `value_num`, `value_text`, `value_bool`, `type`, `unit`, `device_type`, `channel_type`,
`device_name`, `channel_name`, `rooms`, `functions`, `source` and `record` (the whole record: `{{ record | tojson }}`).
Plus Jinja's filters (`join`, `lower`, `upper`, `default`, `replace`, `round`, `int`, `float`, `string`, `first`,
`last`, `length`, `tojson`, …) and `slug` (a name as an identifier: `heizung_bad_1`). The templates are sandboxed: no
`include`/`import`, no file or network access, limits on time and output. The configuration page lists everything
under *Variables and filters*.

## Webhook

One `POST` (or `PUT`/`PATCH`) per batch with a JSON array of the rendered bodies, or, as NDJSON, one line per record;
Basic or Bearer authentication, own headers, gzip. The URL is a template itself (`https://host/hook/{{ kind }}`); a
batch is grouped by the rendered URL. That reaches n8n, Node-RED, Logstash/Vector/Fluent Bit and your own services.

## Files

`/usr/local/addons/forwarder/etc/forwarder.json` (the settings, selections and targets) and `etc/secrets.json` (0600,
the secrets). An uninstall leaves both for a reinstall. The API is described in [docs/api.md](docs/api.md).

## Development

Go 1.25+ and Node 22+. `go test -race ./...`; in `ui/`: `npm ci`, `npm run check`, `npm test`, `npm run build`,
`npm run e2e` (Playwright against the real daemon on a simulated system, `internal/devserver`).
`FORWARDER_DOCKER=1 go test -run TestRoundTrip ./internal/roundtrip` writes and reads back against InfluxDB 1.8 and
2.7 in Docker. `VERSION=0.1.0 scripts/build.sh` builds the packages for aarch64, x86_64 and armv7l. The addon stands on
[ccu-addon-kit](https://github.com/hobbyquaker/ccu-addon-kit) and [occulite-ui](https://github.com/hobbyquaker/occulite-ui).

## License

MIT, see [LICENSE](LICENSE).
