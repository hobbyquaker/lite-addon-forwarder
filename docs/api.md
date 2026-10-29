# The forwarder's API

The daemon serves its configuration page and this JSON API on the loopback (port 8295 by default); openccu-lite's web
server proxies `/addons/forwarder/` to it. Every request needs the system's session (the shell opens the page with it)
at the *admin* level; a writing request (`POST`, `PUT`, `DELETE`) also carries the header `X-CCU-Addon-Request` with
any value. Errors are `{"error": "<code>", "message": "…"}` with 400 (`invalid`), 404 (`not-found`) or 409 (`in-use`).

All paths below are under `/addons/forwarder/api/v1`.

## Health and status

| Route | Answer |
| --- | --- |
| `GET /health` | `{version, started, uptime_s, source: {…}, metrics: {…}, targets, selections, types}` |
| `GET /status` | `{source: {…}, metrics: {…}, targets: [Status]}` |
| `GET /stream` | the same as Server-Sent Events (`event: status`), on every change and at least every 5 s |
| `GET /addons/forwarder/metrics/{id}` | **outside the API and the session:** the exposition of a Prometheus target in scrape mode, for a scraper that carries the target's `scrape_token` (`Authorization: Bearer` or `?token=`); 401 otherwise, 404 for an unknown or disabled target. The forwarder's own `forwarder_*` figures follow the series |

`source` is the event source's state: `connected`, `interfaces`, `devices`, `channels`, `datapoints`, `events`,
`snapshots`, `resyncs`, `last_event`, `last_error`, `meta_revision`, `inventory_at`. `metrics` is the poller's:
`last_poll`, `records`, `duration_ms`, `groups` (the groups polled last), `polls`, `errors` (per group), `interval_s`.

A target's `Status`: `id`, `name`, `type`, `enabled`, `running`, `connected`, `state` (`idle`, `ok`, `retrying`,
`error`, `disabled`), `last_success`, `last_error`, `last_error_at`, `queued`, `in_flight`, `spilled`, `spill_bytes`,
`sent`, `batches`, `dropped`, `overflow`, `retried`, `render_errors`, `last_render_error`, `lag_ms`, `next_retry`,
`attempt`.

## The inventory and the records

| Route | Answer |
| --- | --- |
| `GET /inventory` | the devices and channels with names, rooms, functions, types and datapoint keys; the lists of interfaces, rooms, functions, device types, channel types and keys with counts; `metric_groups` (each `{name, source: homematic\|host, title: {de, en}, doc: {de, en}, metrics: [{name, labels, unit, doc}]}`) and `log_streams` for the selection editor |
| `GET /recent?n=50&selection=<id>` | the newest records (newest first), each with `summary` and `targets` (the ids of the targets whose selection matches) |

## The record

```json
{"ts":"2026-10-29T14:03:12.481Z","kind":"event","interface":"HmIP-RF","device":"000A1BE9A1B2C3","channel":1,
 "address":"000A1BE9A1B2C3:1","key":"ACTUAL_TEMPERATURE","value":21.4,"value_num":21.4,"type":"FLOAT","unit":"°C",
 "device_type":"HmIP-eTRV-2","channel_type":"HEATING_CLIMATECONTROL_TRANSCEIVER","device_name":"Heizung Bad",
 "channel_name":"Heizung Bad:1","rooms":["Bad"],"functions":["Heizung"],"source":"event"}
```

`kind` is `event`, `metric` (`group`, `name`, `labels`, `unit`; the number in `value` and `value_num`) or `log`
(later: `stream`, `log_unit`, `priority`, `message`, `fields`). `value` keeps the value's own type; `value_num` is the number a numeric store wants (booleans
0/1, enums their index); `value_text` the text of a string or an enum. `source` is `event` (a change), `snapshot`
(the current value at start or after a stream resync) or `sweep` (the system's periodic read).

## Selections

A selection is the JSON below. Within one criterion the entries are ORed, across the criteria ANDed, and an empty
criterion restricts nothing. `objects` are ticked devices (`<interface>/<device>`, which include their channels) and
channels (`<interface>/<device>:<n>`); `exclude_objects` the same form, applied last. `keys` and `exclude_keys` are
globs (`PRESS_*`). Rooms and functions are matched by name, on the labels the source keeps live.

```json
{"id":"…","name":"Bad","kinds":["event"],"interfaces":[],"device_types":[],"channel_types":[],"objects":[],
 "exclude_objects":[],"keys":[],"exclude_keys":["RSSI_*"],"rooms":["Bad"],"functions":[],"sources":["event","snapshot"],
 "metric_groups":[],"log_streams":[],"log_units":[],"log_priority":""}
```

| Route | |
| --- | --- |
| `GET /selections` | every selection, with `count: {datapoints, channels, devices}`, `used_by` (target names) and `targets` |
| `POST /selections` | create; the body is a selection without `id` |
| `PUT /selections/{id}` | replace |
| `DELETE /selections/{id}` | delete; 409 `in-use` while a target uses it |
| `POST /selections/match?n=20` | the count and the recent matching records of an unsaved selection (the body) |

## Targets

```json
{"id":"…","name":"Influx","type":"influx","enabled":true,"selection":"<selection id or empty>",
 "batch":{"size":500,"interval_ms":5000,"max_bytes":1048576},"queue":{"max_records":50000,"spill_mb":50},
 "retry":{"min_ms":1000,"max_ms":60000},"tls":{"insecure":false,"ca":"","server_name":""},"timeout_s":15,
 "influx":{"version":"2","url":"http://influx:8086","org":"home","bucket":"hm","gzip":true,"preset":"clean",
           "event":{"measurement":"{{ key }}","tags":[{"name":"device","value":"{{ device }}"}],
                    "fields":[{"name":"value","value":"{{ value }}","type":"auto"}]},
           "metric":{…},"log":{…}},
 "secrets":{"token":true}}
```

- `type` is `influx`, `webhook`, `elastic` or `prometheus`; the matching object holds the connection and the
  templates.
- **Secrets** (`password`, `token`, `api_key`, `scrape_token`, as the type allows) are never answered: `secrets` says
  which are set. A write sets one with `"set_secrets": {"token": "…"}` and removes one with
  `"clear_secrets": ["token"]`.
- `queue.spill_mb` caps the overflow on disk (`var/spill/<id>/`) that takes the records when the memory queue is full
  and keeps them over a restart; 0 switches it off.
- `influx.version` is `"1"` (`/write` with `database`, `retention_policy`, `user` and the secret `password`) or
  `"2"` (`/api/v2/write` with `org`, `bucket` and the secret `token`; InfluxDB 3 speaks it too).
- A `webhook` has `url` (a template; a batch is grouped by the rendered URL), `method`, `format` (`json`: an array of
  bodies per batch; `ndjson`: one body per line), `body` (a template per record, default `{{ record | tojson }}`),
  `content_type`, `headers`, `auth` (`none`, `basic` with `user` and the secret `password`, `bearer` with the secret
  `token`), `gzip`.
- Field `type`: `auto` (the value in its own type; numbers as floats), `float`, `integer`, `boolean`, `string`. An
  empty rendered tag or field is left out. A tag named `*` stands for every label of a metric record (every field
  of a log record).
- An `elastic` target has `url`, `index` (a template per record, lower-cased), `document` (a template that renders a
  JSON object; default `{{ record | tojson }}`), `timestamp` (adds `@timestamp`), `action` (`index` or `create` for a
  data stream), `pipeline` (an ingest pipeline), `auth` (`none`, `basic` with `user` and the secret `password`,
  `apikey` with the secret `api_key`, `bearer` with the secret `token`), `gzip`, `install_template` with
  `template_pattern` (an index template put on the first connection), `preset` (`record`, `flat`, `custom`).
- A `prometheus` target has `mode` (`scrape`: the daemon serves `/addons/forwarder/metrics/{id}` to a scraper with
  the secret `scrape_token`; `remote_write`: it pushes to `url` with `auth` `none`, `basic` or `bearer`), `name` (the
  metric name template), `labels` (named templates; a label named `*` stands for every label of a metric record),
  `stale_s` (scrape mode: a series without a new record for this long is left out; 0 keeps all), `preset` (`clean`,
  `one_metric`, `custom`). Only records with a number are exported.

| Route | |
| --- | --- |
| `GET /targets` | every target with its `status` |
| `POST /targets`, `PUT /targets/{id}`, `DELETE /targets/{id}` | create, replace, delete |
| `POST /targets/{id}/enabled` `{"enabled": bool}` | switch |
| `POST /targets/{id}/clear` | drop the queued records |
| `POST /targets/test?sample=recent` | send one test record (or the most recent one) with the body's configuration; answers `{ok, message, record}` |
| `POST /targets/preview?n=10` | the recent records as the body's target serialises them: `{records, text, errors}` |

## Templates

| Route | |
| --- | --- |
| `POST /templates/render` `{"template": "…", "selection": "<id>", "n": 5}` | renders against the recent records: `{error?, results: [{record, output, error?}]}` |
| `GET /templates/help` | the variables and the filters, documented in German and English |
| `GET /presets` | the Influx presets (`clean`, `one_measurement`, `per_device`), the webhook presets, the Elasticsearch presets (`record`, `flat`), the Prometheus presets (`clean`, `one_metric`) and the defaults per type |

## Settings

`GET /settings`, `PUT /settings`: `{"snapshot": true, "recent": 500, "lang": "de", "metrics_interval_s": 60}`.
`metrics_interval_s` (10 … 3600) is how often the statistics and the host's figures are polled - only the metric
groups an enabled target's selection wants.
