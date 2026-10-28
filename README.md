# tread

A small Go CLI for an underdesk treadmill, with a SQLite usage log and a dashboard. DFT200 support is based on the reverse-engineered protocol in `../tread-go` and `../dft200-go`.

## Try the dashboard

```sh
go run ./cmd/tread serve -demo
```

Open <http://127.0.0.1:18743>. Demo mode generates over three years of history in an isolated, in-memory SQLite database. It never connects to Bluetooth or changes your real database. The API is read-only in demo mode. Stop it with Ctrl+C.

The dashboard includes all time, 3 years, 1 year, 3 months, 1 month, 1 week and 1 day. It shows walking hours, estimated steps, time-weighted average speed, estimated calories, distance, activity bars, a speed-distribution donut and recent sessions. The configuration page shows personal settings and speed calibration. All assets are embedded; there are no frontend dependencies or CDN requests. Icons are a vendored subset of [Lucide](https://lucide.dev) (ISC, license in `web/icons.svg`).

## Set up your treadmill

Requires Go as specified in `go.mod`, Linux, and BlueZ for Bluetooth commands.

```sh
go build -o tread ./cmd/tread
mkdir -p ~/.local/bin ~/.config/tread
ln -s "$(pwd)/tread" ~/.local/bin/tread  # Once, if the link does not already exist
cp config.example.toml ~/.config/tread/config.toml
```

Edit the file. Set `treadmill.mac`, personal measurements and measured belt speeds. Discover the address with `bluetoothctl`, `scan on`, then `devices`. BlueZ must have discovered the device before connecting by address. Your user needs permission to access BlueZ.

Use `tread -config /path/to/config.toml ...` or `TREAD_CONFIG` for a different file. A missing default config uses defaults and does not enable Bluetooth. `tread config` prints the effective configuration.

The defaults are `listen = "127.0.0.1:18743"` and `server_url = "http://127.0.0.1:18743"`. The port is fixed; if it is occupied, startup fails rather than choosing a fallback. Existing explicit addresses, including port 8080, stay unchanged. To migrate an existing config, set both values:

```toml
listen = "127.0.0.1:18743"
server_url = "http://127.0.0.1:18743"
```

These settings are independent. Overriding only one does not update the other.

Run `tread serve` for the dashboard if it is not already running. Bare `tread` shows help; controls do not implicitly start a server. No server restart is needed for controller or keybinding changes.

### Controls and local state

`start`, `resume`, `pause`, `stop` and `speed <1-8>` are explicit controls. `speed` takes a **level from 1 to 8**, transmitted as 10 to 80, not a known physical speed. `start` and `resume` send the same command. A speed command can start the belt even while stopped. Only issue movement commands deliberately, with the belt clear and when ready for movement.

- `toggle` pauses a locally known running treadmill, otherwise starts it. With unknown mode it sends start, which leaves an already running belt running and establishes mode, so the next toggle pauses.
- `speed-up` and `speed-down` change one level only when running mode is locally known; they refuse otherwise, because a speed command can start a stopped belt. With unknown speed, `speed-down` sets the lowest level and `speed-up` refuses. At levels 8 and 1 respectively they do nothing, with no Bluetooth write or usage event.
- `reset-state` forgets local mode and speed without moving the belt. It preserves queued usage and confirmed pending events.
- `status` sends a query but decodes no telemetry. It cannot discover or confirm mode or speed.

A new installation starts with unknown knowledge. `toggle` establishes mode by starting, and `speed-down` while running establishes speed at the lowest level; after that both speed keys work. An explicit `speed` also establishes speed. For example, after checking the treadmill and surroundings:

```sh
tread start
tread speed 1
# Only after both succeed, relative speed keys can be used.
tread pause
tread resume
tread stop
```

Successful explicit start/resume/pause/stop commands establish only mode; `speed` establishes only speed. Starting never invents a speed from `initial_level`, which is for statistics. Pause/stop establish enough mode knowledge for toggle, but relative speed still requires running mode and known speed.

Knowledge lives in `control-state.json` and the crash marker `control-state.json.in-flight`, beside the config and `queue.jsonl`. It is bound to the configured device, driver, Bluetooth address and Linux boot ID. Reboot, identity changes or an interrupted/failed write invalidate knowledge. These files record sent commands, not telemetry: the treadmill can reject a write without the CLI knowing. Physical power cycles, remote controls and suspend can invalidate assumptions without automatic detection. Run `tread reset-state` after those situations, then deliberately re-establish knowledge only when ready for movement. Do not delete runtime files to reset; they may contain confirmed usage awaiting recovery. Bluetooth failures are never automatically retried.

Successful movement commands commit local state and queue usage before automatically attempting HTTP delivery outside the control lock. Offline events remain queued until the next successful movement command or `sync`; status, reset and bound no-ops do not deliver them. Separate CLI processes share a file lock, and SQLite deduplicates event IDs. A crash between a Bluetooth write and local confirmation can still leave the physical outcome and usage unknown.

```sh
tread sync                       # Retry usage delivery, not Bluetooth controls
tread stats -period 1m
tread stats -period all -json
tread help
```

`help` lists all commands, including `config` and `serve`. `serve -listen 127.0.0.1:8090` overrides the listen address; update `server_url` in the controller config accordingly. `stats -days N` supports a custom day count.

For a homeserver, run `serve` there and point the controller's `server_url` at it. Use only one controller/config for physical controls per treadmill to avoid divergent local knowledge. Keep a stable `device` identifier and keep personal metrics and calibration in the **server's** configuration. The API has no authentication; the server binds to localhost by default. Use a private network or an authenticated reverse proxy to make it available elsewhere.

### Bluetooth agent and live readings

`tread agent` owns the Bluetooth link: it holds a persistent connection, reconnects after drops, and accepts control commands over a private local socket. Control commands (`start`, `toggle`, `speed`, …), `reset-state`, `sync` and `observed` go through the agent by default. They are sent at most once and never fall back to direct Bluetooth. A command that arrives while the agent is still connecting waits up to 10 seconds for the link, then fails with "no command sent".

Errors say whether anything reached the treadmill. "No command sent" (agent not running, device unavailable, another command in progress) is safe to repeat. "Command outcome unknown; do not retry" means the request may have been executed; check the treadmill before acting again.

The agent's lock and socket live in a runtime directory: `TREAD_RUNTIME_DIR` if set, otherwise `$XDG_RUNTIME_DIR/tread`, otherwise `/run/tread`. tread creates `$XDG_RUNTIME_DIR/tread` (mode 0700) on demand. Any other directory must already exist, owned by you with mode 0700. Every process that controls the treadmill must use the same directory; it is what keeps a second Bluetooth owner out.

`tread -direct <command>` skips the agent and opens a short-lived Bluetooth connection, as earlier versions did. It takes the same ownership lock, so it fails while the agent runs.

`tread observed [-json]` prints the agent's live snapshot: connection, telemetry status and any readings. `tread serve` reads the same snapshot through `/api/live` without touching Bluetooth. The sidebar shows the link status, and the overview shows a live card while telemetry is active. Readings are what the treadmill reports, kept separate from history, which still comes from sent commands.

Telemetry is off by default. `experimental_van_telemetry = true` enables the decoder recovered from the VAN FIT app (`docs/van-fit-protocol.md`). It works on a DFT200 but remains experimental. With the default unit byte, speed and distance are labelled km/h and km as inferred units: the treadmill's nominal speed, not your calibrated belt speed. The two will disagree.

Under `[treadmill]`, `idle_disconnect_timeout = "5m"` disconnects the agent after five minutes of continuously fresh paused state and zero reported speed. Set `"0s"` to disable; restart the agent after changing it. Missing, unknown, running, nonzero or stale readings reset the timer. Without telemetry, there is no automatic disconnect. The decoder recognizes paused state, not a generic stopped state.

The next Bluetooth command, including `status`, reconnects before being sent once. `observed`, dashboard polling, `sync` and `reset-state` do not reconnect. While disconnected, the agent cannot see or wake for activity from the physical remote.

### Hyprland startup

The CLI does not start the server implicitly. On the configured workstation below, the treadmill control wrapper starts or reuses the agent and server user services on the first control-key press; there is no startup at login. The wrapper then sends the requested movement command once, even if the server is unavailable, so offline usage can still queue. A brief readiness check does not retry the command. Normal Hyprland logout stops both services; crashes or forced termination may not. For manual control, use `systemctl --user stop tread.service tread-agent.service`; inspect logs with `journalctl --user -u tread-agent.service -u tread.service`.

The optional browser shortcut below only opens the dashboard URL. It does not start the server.

### Optional local hostname

`tread setup-host [-name tread.local]` explicitly edits `/etc/hosts` to map the chosen name to `127.0.0.1` only. This is optional and does not start the server. Run:

```sh
sudo "$(command -v tread)" setup-host
```

Resolving the executable before `sudo` avoids sudo's possibly different `PATH`. Repeating the command is idempotent; an existing mapping to another address is an error, not overwritten. Only this system edit needs elevated permission.

Open <http://tread.local:18743>. Hosts entries do not map ports, so `:18743` is still required. `.local` is reserved for mDNS and may conflict with your resolver setup. Use `sudo "$(command -v tread)" setup-host -name tread.localhost` and <http://tread.localhost:18743> instead, or skip the hosts edit and use <http://127.0.0.1:18743>.

Optional Hyprland browser shortcut after hostname setup, using an illustrative key combination you should change if already bound. This opens the dashboard but does not start the server:

```ini
bind = SUPER, T, exec, xdg-open http://tread.local:18743
```

### Kyria and Hyprland on this workstation

On this workstation, the installed nonrepeating host bindings in `~/.config/hypr/UserConfigs/UserKeybinds.lua` map F14 to `speed-down`, F16 to `toggle`, and F15 to `speed-up`. `~/.config/hypr/UserScripts/TreadmillControl.sh` validates those actions, starts or reuses `tread-agent.service` and `tread.service` with a bounded nonblocking request, waits briefly for a freshly started agent's socket, then invokes `~/.local/bin/tread` once regardless of server availability. It does not retry movement commands; offline usage remains queued. When it starts either service, it sends a notification naming them. Unknown mode makes `toggle` start the belt; with unknown speed, `speed-down` slows to level 1 and `speed-up` fails with an error notification.

This workstation specifically has `~/.config/systemd/user/tread.service` and `tread-agent.service` linked into the dotfiles systemd configuration. They run `~/.local/bin/tread serve` and `~/.local/bin/tread agent` as user services and are not enabled at login. The agent unit has `TimeoutStopSec=10`, because shutdown can block on a stuck Bluetooth call. A native Hyprland shutdown hook stops both on normal logout. Generic installation does not create this service or wrapper behavior.

`~/.local/bin/tread` links to `/home/lman/Code/go/oss-treadmill/tread`; rebuild that ignored binary with `go build -o tread ./cmd/tread` after CLI changes. The Kyria NSL right-bottom three mappings are compiled in `/home/lman/Code/qmk/splitkb_kyria_rev2_base_xarcdev.uf2` but **not flashed**. The keyboard still needs a deliberate firmware flash before those keys emit the new mappings. Host binding installation does not initialize treadmill state; the first toggle press starts the belt.

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
- Calories are an estimate for level walking, using the [2024 Adult Compendium treadmill MET values](https://pacompendium.com/walking/). The calculation is MET × resting kcal/hour × walking hours. When weight, birthdate, height and male/female gender are supplied, resting energy uses the [Mifflin-St Jeor equation](https://pubmed.ncbi.nlm.nih.gov/2305711/). Otherwise it uses 1 kcal/kg/hour. Calories include resting energy during the walk. This is an estimate, not measured energy expenditure.
- Set optional `personal.birthdate` as a quoted `YYYY-MM-DD` date, for example `birthdate = "1990-06-15"`. It replaces `personal.age`; there is no legacy age fallback. The date must not be in the future or imply an age above 120. Age is completed years on the report date in the configured timezone, including for historical estimates. Feb 29 birthdays advance on March 1 in non-leap years. An empty or omitted birthdate uses the basic MET baseline.
- Missing personal data or speed calibration is visible in the dashboard. Unknown derived metrics display a dash. Historical estimates use the current personal profile and calibration, so config changes recalculate history.

Edit TOML to change configuration. Statistics and the read-only configuration page reload it on each request. Restart the server after changing `db_path` or `listen`. The timezone defaults to UTC; use an IANA name such as `Europe/Berlin` for local calendar boundaries.

## Calendar goals

Copy the optional targets file beside the server's TOML config:

```sh
cp goals.example.yaml ~/.config/tread/goals.yaml
```

With `-config /path/to/config.toml` or `TREAD_CONFIG`, use `/path/to/goals.yaml` instead. Open **Goals** in the dashboard. The page shows the resolved path and is read-only. Edit YAML on the server and click Refresh to reload targets, timezone, personal profile and calibration without restarting. `GET /api/goals` also reloads on every request. Missing or empty files show an explanatory empty page; malformed YAML shows an error, not zero progress.

```yaml
daily:
  steps: 6000
  calories: 250
  time: 30m
  average_speed_kmh: 3.5
weekly:
  time: 5h
monthly:
  steps: 150000
yearly:
  time: 250h
```

All four period mappings and their metrics are optional. Omitted metrics have no bar. Targets must be positive: `steps` is an integer count, `calories` is kcal, `time` is a Go duration string such as `30m` or `1h30m`, and `average_speed_kmh` is km/h. Numbers must be finite; unknown or duplicate keys are errors. Use `24h`, not `1d`, for a day's duration.

Goals use calendar days, Monday-start weeks, months and years in the configured timezone, with activity counted only through now. They do not change the existing rolling overview or TOML statistics windows. Average speed is time-weighted: distance divided by walking hours. Current values remain visible above target, while progress bars stop at 100%.

Steps require step length or height; calories require weight. Missing calibration for a used level makes speed-derived metrics unavailable, and average speed is unavailable with no walking. The page labels these cases with a reason instead of showing fake completion. Unconfirmed calibration and other estimate warnings remain visible. Historical progress uses the current profile and calibration.

Demo mode uses illustrative targets in memory and sample history. It never reads or writes your real `goals.yaml`, profile or database.

## Structure

```text
cmd/tread/          CLI, commands, server startup
internal/agent/    Bluetooth owner process and its local socket API
internal/ownership/ Runtime directory and single-owner lock
internal/observed/ Live readings model, separate from history
internal/ble/      Bluetooth transport
internal/device/   Device driver selection and command encoding
internal/protocol/ DFT200 packet format and checksum
internal/config/   TOML settings and validation
internal/event/    Control events and offline delivery
internal/control/  Conservative local mode/speed knowledge and command journal
internal/store/    SQLite event storage
internal/stats/    Sessions, calibration, date ranges and estimates
internal/goals/    Optional YAML targets and calendar progress
internal/server/   HTTP API and embedded files
internal/demo/     Repeatable mock history
web/               Overview, goals and configuration, plain HTML/CSS/JS
```

To add a device, implement `device.Driver`, register it in `device.Lookup`, and update config validation for its supported levels. The CLI and transport use the driver's command bytes and write characteristic. Session calculation and the dashboard use calibrated speeds rather than protocol bytes. Different level ranges or telemetry will need small changes to event validation and the config page. This is a simple extension point, not a plugin framework.

## Check

```sh
go test ./...
go vet ./...
```

Tests cover command bytes, pauses, missed stops, speed changes, crossing midnight and range boundaries, daylight saving time, separate device histories, duplicate uploads, API validation, and offline event retries. Bluetooth transport still needs verification on the real treadmill.
