# tread

A small Go CLI for an underdesk treadmill, with a SQLite usage log and a dashboard. DFT200 support is based on the reverse-engineered protocol in `../tread-go` and `../dft200-go`.

## Try the dashboard

```sh
go run ./cmd/tread serve -demo
```

Open <http://127.0.0.1:8080>. Demo mode generates over three years of history in an isolated, in-memory SQLite database. It never connects to Bluetooth or changes your real database. The API is read-only in demo mode. Stop it with Ctrl+C.

The dashboard includes all time, 3 years, 1 year, 3 months, 1 month, 1 week and 1 day. It shows walking hours, estimated steps, time-weighted average speed, estimated calories, distance, activity bars, a speed-distribution donut and recent sessions. The configuration page shows personal settings and speed calibration. All assets are embedded; there are no frontend dependencies or CDN requests.

## Set up your treadmill

Requires Go as specified in `go.mod`, Linux, and BlueZ for Bluetooth commands.

```sh
go build -o tread ./cmd/tread
mkdir -p ~/.config/tread
cp config.example.toml ~/.config/tread/config.toml
```

Edit the file. Set `treadmill.mac`, personal measurements and measured belt speeds. Discover the address with `bluetoothctl`, `scan on`, then `devices`. BlueZ must have discovered the device before connecting by address. Your user needs permission to access BlueZ.

Use `tread -config /path/to/config.toml ...` or `TREAD_CONFIG` for a different file. A missing default config uses defaults and does not enable Bluetooth. `tread config` prints the effective configuration.

Start the server, then use another terminal for control:

```sh
./tread serve
./tread start
./tread speed 3
./tread pause
./tread resume
./tread stop
./tread stats -period 1m
./tread stats -period all -json
./tread sync
```

`speed` takes a **level from 1 to 8**, transmitted as 10 to 80, not a known physical speed. `start` and `resume` send the same command. `status` sends the device's status query, but does not decode telemetry or report its current state. `help` lists commands. `serve -listen 127.0.0.1:8090` overrides the listen address; update `server_url` in CLI configs accordingly. `stats -days N` supports a custom day count.

For a homeserver, run `serve` there and point each controller's `server_url` at it. Give all controllers of one treadmill the same `device` identifier. Keep personal metrics and calibration in the **server's** configuration. The API has no authentication; the server binds to localhost by default. Use a private network or an authenticated reverse proxy to make it available elsewhere.

A successful Bluetooth write is followed immediately by event delivery to the server. Failed Bluetooth writes do not create usage. Events are saved to `queue.jsonl` beside the controller's config before attempting HTTP delivery. If the server is offline, they remain there until the next command or `sync`. Separate CLI processes share a file lock. SQLite deduplicates event IDs, so delivery retries are safe. Commands cannot detect a physical rejection by the treadmill, actions from a remote, or a process/power failure between the Bluetooth write and local event persistence.

## Calibrate statistics

Speed calibration belongs under `[treadmill.speed_kmh]`. Measure each level; do not assume a level equals km/h.

```text
km/h = belt length in metres × full revolutions ÷ elapsed seconds × 3.6
m/s  = km/h ÷ 3.6
```

Fill all eight entries and set `calibrated = true` under `[treadmill]`. Set `initial_level` to the actual starting level. Speed settings carry across recorded stops and pauses, so record any subsequent changes with the CLI.

- Walking time excludes pauses. A pause expires after 270 seconds. A running stretch with no commands is capped at two hours to limit damage from a missing stop. Longer walks should include recorded speed/control activity.
- Distance integrates calibrated speed over walking time. Average speed is distance divided by walking time. Sessions crossing midnight or the selected period are split accurately in the configured timezone. The week window includes today and the preceding six calendar days. The day window is today; month/year windows end now and start at midnight on the corresponding calendar date.
- Steps are estimated from distance divided by `personal.step_length_m`. Measure it by walking a known distance and counting steps. If omitted, height × 0.414 is used as a rough fallback.
- Calories are an estimate for level walking, using the [2024 Adult Compendium treadmill MET values](https://pacompendium.com/walking/). The calculation is MET × resting kcal/hour × walking hours. When weight, age, height and male/female gender are supplied, resting energy uses the [Mifflin-St Jeor equation](https://pubmed.ncbi.nlm.nih.gov/2305711/). Otherwise it uses 1 kcal/kg/hour. Calories include resting energy during the walk. This is an estimate, not measured energy expenditure.
- Missing personal data or speed calibration is visible in the dashboard. Unknown derived metrics display a dash. Historical estimates use the current personal profile and calibration, so config changes recalculate history.

Edit TOML to change configuration. Statistics and the read-only configuration page reload it on each request. Restart the server after changing `db_path` or `listen`. The timezone defaults to UTC; use an IANA name such as `Europe/Berlin` for local calendar boundaries.

## Structure

```text
cmd/tread/          CLI, commands, server startup
internal/ble/      Bluetooth transport
internal/device/   Device driver selection and command encoding
internal/protocol/ DFT200 packet format and checksum
internal/config/   TOML settings and validation
internal/event/    Control events and offline delivery
internal/store/    SQLite event storage
internal/stats/    Sessions, calibration, date ranges and estimates
internal/server/   HTTP API and embedded files
internal/demo/     Repeatable mock history
web/               Dashboard and configuration page, plain HTML/CSS/JS
```

To add a device, implement `device.Driver`, register it in `device.Lookup`, and update config validation for its supported levels. The CLI and transport use the driver's command bytes and write characteristic. Session calculation and the dashboard use calibrated speeds rather than protocol bytes. Different level ranges or telemetry will need small changes to event validation and the config page. This is a simple extension point, not a plugin framework.

## Check

```sh
go test ./...
go vet ./...
```

Tests cover command bytes, pauses, missed stops, speed changes, crossing midnight and range boundaries, daylight saving time, separate device histories, duplicate uploads, API validation, and offline event retries. Bluetooth transport still needs verification on the real treadmill.
