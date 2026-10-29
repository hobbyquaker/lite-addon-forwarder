# lite-addon-forwarder

*[English](README.en.md)*

Leitet die Ereignisse der Homematic-Geräte, die Statistiken des Systems und die Kennzahlen des Rechners an externe
Speicher weiter: **InfluxDB** (1.x, 2.x, 3.x und alles, was das Line-Protokoll spricht: VictoriaMetrics, QuestDB,
Telegraf), **HTTP-Webhooks** (JSON oder NDJSON), **Elasticsearch / OpenSearch** und **Prometheus** (ein Scrape-Endpunkt
oder remote_write). Beliebig viele Ziele, wiederverwendbare Auswahlen, konfigurierbare Schemata per Jinja-Templates,
Status je Ziel, ein Überlauf auf den Datenträger, wenn ein Ziel ausfällt. Ein Addon für
[openccu-lite](https://github.com/hobbyquaker/openccu-lite).

## Was es tut

- **Liest jeden Datenpunkt-Wert** aus dem Ereignisstrom des Systems (mit Wiederaufnahme nach einem Neustart) und
  versieht ihn mit **Gerätename, Kanalname, Räumen und Gewerken** aus den Metadaten sowie Typ, Einheit, Gerätetyp und
  Kanaltyp aus den Gerätebeschreibungen. Umbenennungen und Raumänderungen folgen live. Beim Start und nach einer
  Unterbrechung des Stroms geht einmal **jeder aktuelle Wert** (Snapshot) hinaus, damit die Speicher einen
  definierten Startwert haben (abschaltbar).
- **Statistiken und Systemkennzahlen** als Metriken, in Gruppen, die eine Auswahl anhakt (nur angehakte Gruppen
  werden abgefragt, alle 60 s, einstellbar): *Servicemeldungen* je Typ und Schnittstelle, *RPC-Ereignisse* je
  Schnittstelle mit Rate, *Funk* (Duty Cycle, Carrier Sense, Verbindung), *Erreichbarkeit* und *Blockaden* der
  Callback-Empfänger, *Dienste und Addons* (Zustand, beobachtete Neustarts, Arbeitsspeicher) - vom System; dazu
  *CPU und Last*, *Arbeitsspeicher und Swap*, *Datenträger-IO*, *Speicherplatz*, *Netzwerk*, *Temperatur* und
  *System* (Laufzeit, Prozesse) vom Rechner selbst. Die Liste steht unten.
- **Auswahlen** (beliebig viele) sagen, was weitergeleitet wird: Geräte und Kanäle einzeln anhaken, dazu Regeln nach
  Schnittstelle, Gerätetyp, Kanaltyp, Datenpunkt (mit Platzhaltern: `*_TEMPERATURE`, `PRESS_*`), Raum und Gewerk,
  sowie Ausschlüsse (Datenpunkte, Geräte, Kanäle). Innerhalb einer Regel genügt ein Eintrag, über Regeln hinweg
  müssen alle passen, leere Regeln schränken nicht ein. Der Editor zeigt **live**, wie viele Datenpunkte, Kanäle und
  Geräte passen, und die letzten passenden Datensätze.
- **Ziele** (beliebig viele), jedes mit seiner Auswahl: anlegen, bearbeiten, duplizieren, ein- und ausschalten,
  **testen** (ein Datensatz geht hinaus, die Antwort des Speichers wird gezeigt), **Vorschau** (die letzten Datensätze,
  so wie dieses Ziel sie sendet) und **Status** (verbunden, letzter Fehler, Rückstand, wartend, gesendet, verworfen).
- **Schemata per Template.** Was ein Ziel benennt oder formt, ist ein Template in Jinja-Syntax, wie Home Assistant
  sie kennt: `{{ device_name }}`, `{{ rooms | join(",") }}`, `{{ key | lower }}`, `{% if value > 20 %}…{% endif %}`.
  Bei InfluxDB das Measurement, die Tags und die Felder (mit Typ); beim Webhook die URL und der Inhalt je Datensatz;
  bei Elasticsearch der Indexname und das Dokument; bei Prometheus der Metrikname und die Labels.
  Jedes Template-Feld zeigt **live**, was es für die letzten echten Datensätze erzeugt, Fehler stehen direkt darunter.
  InfluxDB startet aus einer **Vorlage**: *Sauber* (Standard: ein Measurement je Datenpunkt, Adresse und Namen als
  Tags, der Wert als Feld), *Ein Measurement* oder *Je Gerät*; jede lässt sich danach anpassen.
- **Zustellung:** je Ziel eine Warteschlange im Speicher, Pakete nach Größe oder Zeit, Wiederholung mit wachsendem
  Abstand, wenn der Speicher nicht erreichbar ist; mindestens einmal und in Reihenfolge je Ziel. Ein Fehler, den
  Wiederholen nicht heilt (der Speicher lehnt die Daten ab), verwirft das Paket und zählt es. Falsche Zugangsdaten
  oder ein fehlendes Bucket halten die Daten in der Warteschlange, bis Sie es korrigiert haben. Ist die Warteschlange
  im Speicher voll - das Ziel fällt länger aus -, gehen die Datensätze als **Überlauf auf den Datenträger**
  (`var/spill/`, je Ziel begrenzt, 50 MB voreingestellt), und ein Neustart des Addons macht dort weiter, wo es
  aufgehört hat; ist auch der begrenzt volle Überlauf voll, gehen die ältesten verloren und werden gezählt. Im
  Normalbetrieb wird nichts auf die SD-Karte geschrieben.
- **Geheimnisse** (Passwörter, Token) liegen in einer Datei, die nur das Addon lesen kann, und werden nie wieder
  angezeigt. TLS mit eigener CA oder ohne Prüfung je Ziel.

Die Datensätze der *Datensätze*-Seite zeigen, was das Addon zuletzt gesehen hat und an welche Ziele es ging.

## Installation

Das Paket `forwarder-<arch>-<version>.tar.gz` von der Releases-Seite über die Addons-Seite des Systems installieren
(oder über den Katalog, sobald es dort steht). Das Addon braucht die Rechte `rpc:read`, `meta:read` und
`system:read`: es liest Geräte, Beschreibungen, Werte und den Ereignisstrom der Funkschnittstellen, die Namen, Räume
und Gewerke, und die Statistiken des Systems (Servicemeldungen, Funk, Dienste). Die Konfigurationsseite ist unter
`/addons/forwarder/` und über den Einstellungen-Knopf des Addons erreichbar.

## Datenschutz

Das Addon sendet ausschließlich an die Ziele, die Sie eingerichtet haben. Ohne Ziel kontaktiert es nichts außerhalb
des Systems; es gibt keine Voreinstellung, die irgendwohin sendet, keine Telemetrie, keine Update-Abfrage (die macht
das System). Was ein Ziel erhält, bestimmt seine Auswahl; die *Vorschau* zeigt es vor dem Speichern.

## Das Schema in InfluxDB

Mit der Vorlage *Sauber* schreibt das Addon je Datenpunkt ein Measurement, zum Beispiel:

```
ACTUAL_TEMPERATURE,interface=HmIP-RF,device=000A1BE9A1B2C3,channel=1,address=000A1BE9A1B2C3:1,device_type=HmIP-eTRV-2,device_name=Heizung\ Bad,channel_name=Heizung\ Bad:1,room=Bad,function=Heizung,unit=°C value=21.4,value_num=21.4 1793282592481000000
STATE,interface=BidCos-RF,device=MEQ0123456,channel=1,address=MEQ0123456:1,device_name=Lampe\ Flur value=true,value_num=1 1793282593000000000
```

`value` trägt den Wert in seinem Typ (Zahlen immer als Float, Wahrheitswerte als Boolean, Texte als String),
`value_num` die Zahl für Abfragen über Typen hinweg (Wahrheitswerte 0/1, Aufzählungen ihr Index), `value_text` den
Text einer Aufzählung oder eines Textwerts. Die Namen sind Tags, also Dimensionen: eine Umbenennung beginnt eine
neue Serie; wer das nicht will, nimmt die Namenstags heraus und fragt über `address` ab.

## Elasticsearch und OpenSearch

Je Paket ein `_bulk` mit einer Aktionszeile und einem Dokument je Datensatz. Der **Indexname** ist ein Template
(voreingestellt `homematic-{{ kind }}-{{ ts | date('2006.01.02') }}`: ein Index je Art und Tag; kleingeschrieben),
das **Dokument** auch (voreingestellt der ganze Datensatz, dazu `@timestamp`); `index` für einen Index, `create` für
einen Data Stream; optional eine Ingest-Pipeline; Basic, API-Key oder Bearer; gzip. Bei der ersten Verbindung legt das
Addon ein **Index-Template** für `homematic-*` an, das die Felder des Datensatzes mit festen Typen abbildet (`value`
als keyword, `value_num` als double, `value_bool` als boolean, `ts` und `@timestamp` als date, Texte als keyword),
damit ein Wert, der bei einem Datenpunkt eine Zahl und beim nächsten ein Text ist, das Mapping nicht sprengt
(abschaltbar). Ein Dokument, das der Speicher wegen seines Inhalts ablehnt, wird verworfen und gezählt; ein 429 oder
ein Serverfehler wiederholt das Paket. Elasticsearch 7 und 8, OpenSearch 1 und 2.

## Prometheus

Zwei Betriebsarten je Ziel:

- **scrape:** das Addon hält je Zeitreihe den neuesten Wert und das System liefert ihn unter
  `/addons/forwarder/metrics/<Ziel-ID>` aus - über den Webserver des Systems, mit dessen Zertifikat, geschützt durch
  einen **Scrape-Token**, den die Seite beim Anlegen erzeugt und einmal zeigt. Prometheus sendet ihn als Bearer
  (`authorization: credentials: <token>` im Job) oder als `?token=`. Dahinter stehen die Kennzahlen des Forwarders
  selbst (`forwarder_target_sent_total`, `…_dropped_total`, `…_queued`, `…_spilled`, `forwarder_source_events_total`,
  …). Zeitreihen ohne neuen Datensatz können nach einer Frist wegfallen.
- **remote_write:** das Addon sendet die Datensätze als `prometheus.WriteRequest` (Protobuf in snappy) an
  `/api/v1/write` von Prometheus (mit `--web.enable-remote-write-receiver`), VictoriaMetrics, Mimir, Thanos oder
  Grafana Cloud; Basic oder Bearer.

Der **Metrikname** und die **Labels** sind Templates; die Vorlage *Sauber* macht aus einem Ereignis
`homematic_<datenpunkt>{interface,address,device_type,device_name,channel_name,room,function}` und aus einer
Systemmetrik `<gruppe>_<name>{…ihre Labels…}`. Ein Label namens `*` steht für jedes Label einer Metrik. Zeichen, die
Prometheus nicht erlaubt, werden zu `_`; nur Datensätze mit einer Zahl werden exportiert (Wahrheitswerte 0/1,
Aufzählungen ihr Index).

## Die Metrikgruppen

Jede Metrik trägt das Label `host`; die Tabelle nennt die weiteren. Zähler enden auf `_total`, Raten entstehen aus
der Differenz zur vorigen Abfrage (die erste Abfrage liefert nur die Zähler).

| Gruppe | Quelle | Metriken |
| --- | --- | --- |
| `service_messages` | System | `total`; `count{type}`; `by_interface{interface}` |
| `rpc_events` | System | `received_total{interface}` (vom Addon empfangen), `rate_per_minute{interface}`; `interface_rate_per_minute{interface}`, `interface_up{interface}` (wie das System sie je Schnittstellenprozess zählt) |
| `radio` | System | `duty_cycle_percent`, `carrier_sense_percent`, `connected` `{interface,address,type}`; `busy`; `errors` |
| `reachability` | System | `reachable{interface,id,url}`; `unreachable_total` |
| `stalls` | System | `stalled{interface,kind}`; `total` |
| `services` | System | `running`, `failed`, `restarts_total` (seit dem Start des Addons beobachtet), `rss_bytes` `{unit,kind,category}` |
| `cpu` | Rechner | `usage_percent{mode}` (total, user, system, iowait, steal), `load{period}` (1, 5, 15), `count`, `running_tasks` |
| `memory` | Rechner | `total_bytes`, `available_bytes`, `used_bytes`, `used_percent`, `free_bytes`, `buffers_bytes`, `cached_bytes`, `swap_total_bytes`, `swap_used_bytes`, `swap_used_percent` |
| `disk_io` | Rechner | `read_bytes_total`, `write_bytes_total`, `reads_total`, `writes_total`, `read_bytes_per_second`, `write_bytes_per_second`, `reads_per_second`, `writes_per_second`, `busy_percent` `{device}` |
| `storage` | Rechner | `total_bytes`, `used_bytes`, `free_bytes`, `used_percent`, `inodes_used_percent` `{mount,device,fstype}` |
| `network` | Rechner | `rx_bytes_total`, `tx_bytes_total`, `rx_packets_total`, `tx_packets_total`, `rx_errors_total`, `tx_errors_total`, `rx_dropped_total`, `tx_dropped_total`, `rx_bytes_per_second`, `tx_bytes_per_second` `{interface}` |
| `temperature` | Rechner | `celsius{sensor,source}` |
| `system` | Rechner | `uptime_seconds`, `boot_time_seconds`, `processes`, `tasks`, `open_files` |

In InfluxDB wird mit der Vorlage *Sauber* die Gruppe zum Measurement, die Labels zu Tags (der Tag `*`), der
Metrikname zum Feld: `cpu,host=ccu,mode=total usage_percent=12.5`. In Elasticsearch ist eine Metrik ein Dokument
mit `group`, `name`, `labels` und `value_num`.

## Templates

Die Variablen sind die Felder des Datensatzes: `ts` (ein Zeitobjekt; `ts | date("2006-01-02 15:04")`,
`ts | unix`, `ts | unix_ms`, `ts | iso`), `ts_rfc3339`, `ts_unix`, `ts_ms`, `ts_ns`, `kind`, `interface`, `device`,
`channel`, `address`, `key`, `value`, `value_num`, `value_text`, `value_bool`, `type`, `unit`, `device_type`,
`channel_type`, `device_name`, `channel_name`, `rooms`, `functions`, `source`, bei Metriken `group`, `name` und
`labels`, und `record` (der ganze Datensatz: `{{ record | tojson }}`). Dazu Jinjas Filter (`join`, `lower`, `upper`, `default`, `replace`, `round`, `int`,
`float`, `string`, `first`, `last`, `length`, `tojson`, …) und `slug` (ein Name als Bezeichner: `heizung_bad_1`). Die
Templates sind abgeschottet: kein `include`/`import`, kein Datei- oder Netzzugriff, Grenzen für Laufzeit und
Ausgabe. Die Konfigurationsseite listet alles unter *Variablen und Filter*.

## Webhook

Je Paket ein `POST` (oder `PUT`/`PATCH`) mit einem JSON-Array der gerenderten Inhalte oder, als NDJSON, einer Zeile
je Datensatz; Basic- oder Bearer-Authentifizierung, eigene Header, gzip. Die URL ist selbst ein Template
(`https://host/hook/{{ kind }}`); ein Paket wird nach der erzeugten URL gruppiert. Damit erreichen Sie n8n,
Node-RED, Logstash/Vector/Fluent Bit und eigene Dienste.

## Dateien

`/usr/local/addons/forwarder/etc/forwarder.json` (Einstellungen, Auswahlen, Ziele), `etc/secrets.json` (0600,
die Geheimnisse) und `var/spill/<Ziel-ID>/` (der Überlauf eines Ziels, solange es ausfällt). Eine Deinstallation lässt
die Einstellungen für eine Neuinstallation stehen. Die API ist in [docs/api.md](docs/api.md) beschrieben.

## Entwicklung

Go 1.25+ und Node 22+. `go test -race ./...`; in `ui/`: `npm ci`, `npm run check`, `npm test`, `npm run build`,
`npm run e2e` (Playwright gegen den echten Daemon an einem simulierten System, `internal/devserver`).
`FORWARDER_DOCKER=1 go test -run TestRoundTrip ./internal/roundtrip` schreibt und liest gegen InfluxDB 1.8 und 2.7,
OpenSearch 2, Elasticsearch 8 und VictoriaMetrics in Docker. `VERSION=0.2.0 scripts/build.sh` baut die Pakete für
aarch64, x86_64 und armv7l. Das Addon steht auf
[ccu-addon-kit](https://github.com/hobbyquaker/ccu-addon-kit) und [occulite-ui](https://github.com/hobbyquaker/occulite-ui).

## Lizenz

MIT, siehe [LICENSE](LICENSE).
