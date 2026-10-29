# lite-addon-forwarder

*[English](README.en.md)*

Leitet die Ereignisse der Homematic-Geräte an externe Speicher weiter: **InfluxDB** (1.x, 2.x, 3.x und alles, was das
Line-Protokoll spricht: VictoriaMetrics, QuestDB, Telegraf) und **HTTP-Webhooks** (JSON oder NDJSON). Beliebig viele
Ziele, wiederverwendbare Auswahlen, konfigurierbare Schemata per Jinja-Templates, Status je Ziel. Ein Addon für
[openccu-lite](https://github.com/hobbyquaker/openccu-lite).

## Was es tut

- **Liest jeden Datenpunkt-Wert** aus dem Ereignisstrom des Systems (mit Wiederaufnahme nach einem Neustart) und
  versieht ihn mit **Gerätename, Kanalname, Räumen und Gewerken** aus den Metadaten sowie Typ, Einheit, Gerätetyp und
  Kanaltyp aus den Gerätebeschreibungen. Umbenennungen und Raumänderungen folgen live. Beim Start und nach einer
  Unterbrechung des Stroms geht einmal **jeder aktuelle Wert** (Snapshot) hinaus, damit die Speicher einen
  definierten Startwert haben (abschaltbar).
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
  Bei InfluxDB das Measurement, die Tags und die Felder (mit Typ); beim Webhook die URL und der Inhalt je Datensatz.
  Jedes Template-Feld zeigt **live**, was es für die letzten echten Datensätze erzeugt, Fehler stehen direkt darunter.
  InfluxDB startet aus einer **Vorlage**: *Sauber* (Standard: ein Measurement je Datenpunkt, Adresse und Namen als
  Tags, der Wert als Feld), *Ein Measurement* oder *Je Gerät*; jede lässt sich danach anpassen.
- **Zustellung:** je Ziel eine Warteschlange im Speicher, Pakete nach Größe oder Zeit, Wiederholung mit wachsendem
  Abstand, wenn der Speicher nicht erreichbar ist; mindestens einmal und in Reihenfolge je Ziel. Ein Fehler, den
  Wiederholen nicht heilt (der Speicher lehnt die Daten ab), verwirft das Paket und zählt es. Falsche Zugangsdaten
  oder ein fehlendes Bucket halten die Daten in der Warteschlange, bis Sie es korrigiert haben. Im Normalbetrieb wird
  nichts auf die SD-Karte geschrieben.
- **Geheimnisse** (Passwörter, Token) liegen in einer Datei, die nur das Addon lesen kann, und werden nie wieder
  angezeigt. TLS mit eigener CA oder ohne Prüfung je Ziel.

Die Datensätze der *Datensätze*-Seite zeigen, was das Addon zuletzt gesehen hat und an welche Ziele es ging.

## Installation

Das Paket `forwarder-<arch>-<version>.tar.gz` von der Releases-Seite über die Addons-Seite des Systems installieren
(oder über den Katalog, sobald es dort steht). Das Addon braucht die Rechte `rpc:read` und `meta:read`: es liest
Geräte, Beschreibungen, Werte und den Ereignisstrom der Funkschnittstellen und die Namen, Räume und Gewerke. Die
Konfigurationsseite ist unter `/addons/forwarder/` und über den Einstellungen-Knopf des Addons erreichbar.

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

## Templates

Die Variablen sind die Felder des Datensatzes: `ts` (ein Zeitobjekt; `ts | date("2006-01-02 15:04")`,
`ts | unix`, `ts | unix_ms`, `ts | iso`), `ts_rfc3339`, `ts_unix`, `ts_ms`, `ts_ns`, `kind`, `interface`, `device`,
`channel`, `address`, `key`, `value`, `value_num`, `value_text`, `value_bool`, `type`, `unit`, `device_type`,
`channel_type`, `device_name`, `channel_name`, `rooms`, `functions`, `source` und `record` (der ganze Datensatz:
`{{ record | tojson }}`). Dazu Jinjas Filter (`join`, `lower`, `upper`, `default`, `replace`, `round`, `int`,
`float`, `string`, `first`, `last`, `length`, `tojson`, …) und `slug` (ein Name als Bezeichner: `heizung_bad_1`). Die
Templates sind abgeschottet: kein `include`/`import`, kein Datei- oder Netzzugriff, Grenzen für Laufzeit und
Ausgabe. Die Konfigurationsseite listet alles unter *Variablen und Filter*.

## Webhook

Je Paket ein `POST` (oder `PUT`/`PATCH`) mit einem JSON-Array der gerenderten Inhalte oder, als NDJSON, einer Zeile
je Datensatz; Basic- oder Bearer-Authentifizierung, eigene Header, gzip. Die URL ist selbst ein Template
(`https://host/hook/{{ kind }}`); ein Paket wird nach der erzeugten URL gruppiert. Damit erreichen Sie n8n,
Node-RED, Logstash/Vector/Fluent Bit und eigene Dienste.

## Dateien

`/usr/local/addons/forwarder/etc/forwarder.json` (Einstellungen, Auswahlen, Ziele) und `etc/secrets.json` (0600,
die Geheimnisse). Eine Deinstallation lässt beide für eine Neuinstallation stehen. Die API ist in
[docs/api.md](docs/api.md) beschrieben.

## Entwicklung

Go 1.25+ und Node 22+. `go test -race ./...`; in `ui/`: `npm ci`, `npm run check`, `npm test`, `npm run build`,
`npm run e2e` (Playwright gegen den echten Daemon an einem simulierten System, `internal/devserver`).
`FORWARDER_DOCKER=1 go test -run TestRoundTrip ./internal/roundtrip` schreibt und liest gegen InfluxDB 1.8 und 2.7 in
Docker. `VERSION=0.1.0 scripts/build.sh` baut die Pakete für aarch64, x86_64 und armv7l. Das Addon steht auf
[ccu-addon-kit](https://github.com/hobbyquaker/ccu-addon-kit) und [occulite-ui](https://github.com/hobbyquaker/occulite-ui).

## Lizenz

MIT, siehe [LICENSE](LICENSE).
