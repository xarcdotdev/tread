<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/images/logo_light.png">
    <source media="(prefers-color-scheme: light)" srcset="docs/images/logo_dark.png">
    <img src="docs/images/logo_dark.png" alt="tread" width="250">
  </picture>
</p>

<p align="center">A small Go CLI and dashboard for under-desk treadmills, with a SQLite usage log. Supports the Sportstech DFT200.</p>

<p align="center">
  <a href="#setup">Setup</a> · <a href="#commands">Commands</a> · <a href="#configuration">Configuration</a>
</p>

<br>

> [!NOTE]
> This project is entirely vibe-coded. Use at your own risk.

![Dashboard overview with walking time, steps, speed, calories and charts for the past month](docs/images/dashboard.png)

## How it works

```text
  tread <command>                 browser
        │ local socket                │
        ▼                             ▼
   tread agent ──usage (HTTP)──► tread serve ──► SQLite
        │
        │ Bluetooth LE
        ▼
    treadmill
```

`tread agent` is the only process that talks Bluetooth; `tread serve` owns the database and the dashboard. Every command reaches the treadmill at most once. Usage events queue locally and are delivered to the server when it is reachable.

## Try the dashboard

```sh
go run ./cmd/tread serve -demo
```

Open <http://127.0.0.1:18743>. Demo mode uses generated history in an in-memory database; it never touches Bluetooth or your real data. The server has no authentication and is meant for local use.

## Supported devices

| Device            | `treadmill.driver` | Controls                                  | Speed levels | Live telemetry                                          |
| ----------------- | ------------------ | ----------------------------------------- | ------------ | ------------------------------------------------------- |
| Sportstech DFT200 | `dft200`           | start, pause, resume, stop, speed, status | 1–8          | Experimental: speed, distance ([Telemetry](#telemetry)) |

Devices connect over Bluetooth LE through BlueZ. Speed levels come from the driver.

## Setup

### Requirements

Requires Go (see `go.mod`), Linux and BlueZ.

### Install

```sh
go install ./cmd/tread                   # installs to $(go env GOBIN) or ~/go/bin
mkdir -p ~/.config/tread
cp config.example.toml ~/.config/tread/config.toml
```

Or build in place with `go build -o tread ./cmd/tread` and symlink the binary into your `PATH`.

### Configuration

| Setting                                      | Override                         | Default                                                     |
| -------------------------------------------- | -------------------------------- | ----------------------------------------------------------- |
| Config file                                  | `-config <path>`, `TREAD_CONFIG` | `~/.config/tread/config.toml`                               |
| `treadmill.mac`                              |                                  | Required. Find it with `bluetoothctl`: `scan on`, `devices` |
| `[personal]`, `[treadmill.speed_kmh]`        |                                  | Unset; see [Calibration](#calibration)                      |
| `db_path` (SQLite database)                  | `TREAD_DB_PATH`                  | `tread.db` beside the config file                           |
| State directory (usage queue, control state) | `TREAD_STATE_DIR`                | `~/.local/state/tread`                                      |

Keep the state directory persistent; it can hold usage not yet delivered. `tread config` prints the effective configuration.

### Run

Then run these two long-lived processes:

```sh
tread agent   # owns the Bluetooth connection
tread serve   # dashboard and API
```

## Commands

![Starting the treadmill, changing speed, stopping and printing monthly stats](docs/images/cli.gif)

> [!CAUTION]
> A speed command can start a stopped belt. Only send movement commands with the belt clear.

| Command                                     | What it does                                          |
| ------------------------------------------- | ----------------------------------------------------- |
| `tread start` / `resume` / `pause` / `stop` | Control the belt and record usage                     |
| `tread speed <level>`                       | Set a driver speed level (DFT200: 1–8), not km/h      |
| `tread speed-up` / `speed-down`             | One level up or down                                  |
| `tread toggle`                              | Pause if running, otherwise start                     |
| `tread status`                              | Send a status query                                   |
| `tread observed [-json]`                    | Live telemetry snapshot from the agent                |
| `tread stats -period 1m`                    | Usage summary; also `-period all`, `-days N`, `-json` |
| `tread sync`                                | Retry queued usage delivery                           |
| `tread reset-state`                         | Forget local mode and speed knowledge                 |
| `tread help`                                | Full usage                                            |

Commands go through the agent and are sent at most once. `tread -direct <command>` bypasses the agent with a short-lived connection and fails while the agent runs.

| Response                                | Meaning                                                                         |
| --------------------------------------- | ------------------------------------------------------------------------------- |
| "No command sent"                       | Nothing reached the treadmill. Safe to repeat.                                  |
| "Command outcome unknown; do not retry" | The command may have reached the belt. Check the treadmill before sending more. |

## Calibration

Speed levels are ordinal, so distance needs the real belt speed of each level. Measure every level you use, fill `[treadmill.speed_kmh]`, then set `calibrated = true`:

```text
km/h = belt length (m) × revolutions ÷ seconds × 3.6
```

## How estimates work

| Metric       | Source                                                                                                                                                |
| ------------ | ----------------------------------------------------------------------------------------------------------------------------------------------------- |
| Walking time | Commands sent, excluding pauses. A pause longer than 270 s ends the session; a stretch without commands is capped at two hours.                       |
| Distance     | Walking time × calibrated speed per level                                                                                                             |
| Steps        | Distance ÷ `step_length_m` (fallback: height × 0.414)                                                                                                 |
| Calories     | [Compendium MET values](https://pacompendium.com/walking/) and, given full personal data, [Mifflin-St Jeor](https://pubmed.ncbi.nlm.nih.gov/2305711/) |

All of these are estimates. History is recalculated from the current config on every request, so a calibration change applies to past sessions too. Missing data shows as a dash. Restart the server after changing `db_path` or `listen`.

## Goals

```sh
cp goals.example.yaml ~/.config/tread/goals.yaml
```

```yaml
daily:
  steps: 1000
  calories: 250
  time: 30m
  average_speed_kmh: 1.5
weekly:
  time: 5h
monthly:
  steps: 150000
yearly:
  time: 250h
```

All entries are optional. Periods are calendar-based (Monday-start weeks) in the configured timezone. The file is reloaded on each request.

![Goals page with daily, weekly, monthly and yearly progress bars](docs/images/goals.png)

## Advanced usage

### Control state and when to reset

tread tracks mode and speed from the commands it sends, not from telemetry. `speed-up`/`speed-down` only work once running mode and speed are known: start the belt, then use `speed <n>` or `speed-down` to establish speed.

After a power cycle, remote-control use or suspend, run `tread reset-state` to forget local knowledge. Don't delete the files in the state directory; they may hold usage awaiting delivery.

### Telemetry

Set `experimental_van_telemetry = true` to decode live readings (speed, distance) shown in `observed` and the dashboard. Reported values are the treadmill's nominal speed, not your calibrated speed. With telemetry on, `idle_disconnect_timeout` disconnects after a period of paused, zero-speed state; the next command reconnects.

### Remote server

Run `serve` on another host and point the controller's `server_url` at it. Keep personal settings and calibration in the server's config. The API has no authentication, so use a private network or authenticated reverse proxy.

### Optional hostname

```sh
sudo "$(command -v tread)" setup-host -name tread.localhost
```

Adds a `/etc/hosts` entry for <http://tread.localhost:18743>.

## Development

```sh
go test ./...
go vet ./...
```

The images in `docs/images` come from `scripts/demo`, which runs the agent on a fake Bluetooth backend with generated history:

```sh
(cd scripts && npm install && npm run screenshots)  # needs Chrome or Chromium
vhs scripts/cli.tape                                # needs VHS
```

To add a device, implement `device.Driver` in a new `internal/drivers/` subpackage and add it to the registry in `internal/drivers/drivers.go`.

<details>
<summary>Directory structure</summary>

```text
cmd/tread/          CLI, commands, server startup
internal/agent/     Bluetooth owner process and local socket API
internal/ownership/ Runtime directory and single-owner lock
internal/observed/  Live readings model
internal/ble/       Bluetooth transport
internal/device/    Driver contract: commands, capabilities, telemetry
internal/drivers/   Driver registry, one subpackage per device (dft200/)
internal/config/    TOML settings
internal/event/     Control events and offline delivery
internal/control/   Local mode/speed knowledge
internal/store/     SQLite storage
internal/stats/     Sessions, calibration and estimates
internal/goals/     YAML targets and progress
internal/server/    HTTP API and embedded files
internal/demo/      Mock history
web/                Plain HTML/CSS/JS dashboard
```

</details>

## License

Licensed under [Apache 2.0](LICENSE). Includes [Lucide](https://lucide.dev) icons; their ISC/MIT license notices are embedded in [`web/icons.svg`](web/icons.svg).
