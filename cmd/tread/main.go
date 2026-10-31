// Command tread controls a treadmill and tracks walking sessions.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/ble"
	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/control"
	"github.com/xarcdotdev/oss-treadmill/internal/demo"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
	"github.com/xarcdotdev/oss-treadmill/internal/server"
	"github.com/xarcdotdev/oss-treadmill/internal/stats"
	"github.com/xarcdotdev/oss-treadmill/internal/store"
)

const usage = `usage: tread [-config path] [-direct] <command> [options]

  start | resume | pause | stop    Control the treadmill and report usage
  speed <level>                   Set a driver speed level and queue usage
  toggle                          Pause if running, otherwise start
  speed-up | speed-down           Adjust running speed one level (down sets lowest if unknown)
  reset-state                     Forget local knowledge; preserve usage
  status                          Send a status query, not a command acknowledgment
  observed [-json]                Read the agent's live device snapshot
  sync                            Retry queued usage events through the agent
  agent                           Run the signal-aware Bluetooth owner
  stats [-period 1w] [-json]       Usage from server (also supports -days N)
  config                          Show effective file-backed configuration
  serve [-demo] [-listen addr]     Run SQLite event API and dashboard
  setup-host [-name tread.local]   Explicitly map a local hostname in /etc/hosts
  help                            Show help

Periods: all, 3y, 1y, 3m, 1m, 1w, 1d
Config: TREAD_CONFIG or ~/.config/tread/config.toml
Database: TREAD_DB_PATH, else db_path, else tread.db beside the default config.
State: TREAD_STATE_DIR, else $XDG_STATE_HOME/tread or ~/.local/state/tread.
Controls use the agent, without fallback or automatic retry.
-direct explicitly uses short-lived Bluetooth; -direct sync only delivers local usage.
Runtime: TREAD_RUNTIME_DIR, else $XDG_RUNTIME_DIR/tread (created), else /run/tread.
DFT200 speed levels are 1-8, not physical speed.
Demo uses an isolated in-memory SQLite database and never uses Bluetooth.`

func main() {
	log.SetFlags(0)
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Fatal(err)
	}
}

func run(args []string) error {
	return runWithSetupHost(args, setupHost)
}

func runWithSetupHost(args []string, setup func([]string) error) error {
	root := flag.NewFlagSet("tread", flag.ContinueOnError)
	path := root.String("config", "", "config file")
	direct := root.Bool("direct", false, "explicit legacy control transport; sync delivers locally without Bluetooth")
	root.Usage = func() { fmt.Println(usage) }
	if err := root.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	args = root.Args()
	if len(args) == 0 {
		fmt.Println(usage)
		return nil
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "help":
		fmt.Println(usage)
		return nil
	case "setup-host":
		return setup(args)
	case "start", "resume", "pause", "stop", "status", "speed", "toggle", "speed-up", "speed-down", "reset-state":
		req, err := commandRequest(control.Action(cmd), args)
		if err != nil {
			return err
		}
		if !*direct {
			return agentControl(req)
		}
		// Own the host before loading configuration or touching local journals.
		return withOwnership(func() error {
			cfg, err := loadConfig(*path)
			if err != nil {
				return err
			}
			level := 0
			if req.Level != nil {
				level = *req.Level
			}
			return controlWith(newController(cfg), req.Action, level)
		})
	case "observed":
		if *direct {
			return errors.New("observed requires the agent; omit -direct")
		}
		return showObserved(args)
	case "sync":
		if len(args) != 0 {
			return errors.New("usage: tread [-direct] sync")
		}
		if !*direct {
			return agentSync()
		}
	case "agent":
		if len(args) != 0 {
			return errors.New("usage: tread agent")
		}
		fallthrough
	default:
		if *direct {
			return errors.New("-direct is only supported for control commands and sync")
		}
	}
	cfg, err := loadConfig(*path)
	if err != nil {
		return err
	}
	switch cmd {
	case "agent":
		return runAgent(cfg)
	case "serve":
		return serve(cfg, args)
	case "stats":
		return showStats(cfg, args)
	case "config":
		if len(args) > 0 {
			return errors.New("usage: tread config")
		}
		return json.NewEncoder(os.Stdout).Encode(cfg)
	case "sync":
		// Explicit direct sync is queue/network only, with no Bluetooth owner.
		return syncUsage(newController(cfg))
	default:
		return fmt.Errorf("unknown command %q; use tread help", cmd)
	}
}

func loadConfig(path string) (config.Config, error) {
	var cfg config.Config
	var err error
	if path != "" {
		cfg, err = config.LoadFile(path)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	return cfg, nil
}

func newController(cfg config.Config) *control.Controller {
	return &control.Controller{Config: cfg, Queue: event.NewClient(cfg.ServerURL, cfg.QueuePath()), Send: ble.Send}
}

func controlWith(c *control.Controller, action control.Action, level int) error {
	out, err := (&control.Service{Controller: c}).Execute(action, level)
	if err != nil {
		return err
	}
	reportControl(out)
	return nil
}

func syncUsage(c *control.Controller) error {
	return (&control.Service{Controller: c}).Sync()
}

func serve(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	mock := fs.Bool("demo", false, "isolated mock history")
	listen := fs.String("listen", cfg.Listen, "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected serve arguments")
	}
	db := cfg.DBPath
	if *mock {
		cfg = demo.Config()
		db = ":memory:"
	} else if err := os.MkdirAll(filepath.Dir(db), 0o700); err != nil {
		return err
	}
	st, err := store.Open(db)
	if err != nil {
		return err
	}
	defer st.Close()
	if *mock {
		if err := st.AddEvents(demo.Events(time.Now(), cfg.Location())); err != nil {
			return err
		}
		log.Print("Demo mode: mock data, read-only API, isolated SQLite")
	}
	log.Printf("Dashboard: http://%s", *listen)
	app := server.New(st, cfg, *mock)
	app.Live = agentSnapshot
	srv := http.Server{Addr: *listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second}
	return srv.ListenAndServe()
}

func showStats(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("stats", flag.ContinueOnError)
	period := fs.String("period", "1w", "statistics period")
	days := fs.Int("days", 0, "override with N days")
	raw := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected stats arguments")
	}
	if _, err := stats.PeriodStart(*period, time.Now(), cfg.Location()); err != nil {
		return err
	}
	query := "period=" + url.QueryEscape(*period)
	if *days != 0 {
		if *days < 1 {
			return errors.New("days must be positive")
		}
		query = fmt.Sprintf("days=%d", *days)
	}
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(cfg.ServerURL + "/api/stats?" + query)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server: %s", resp.Status)
	}
	var r stats.Report
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return err
	}
	if *raw {
		return json.NewEncoder(os.Stdout).Encode(r)
	}
	fmt.Printf("Walking     %.2f hours\n", r.Totals.Hours)
	if r.DistanceAvailable {
		fmt.Printf("Distance    %.2f km\nAvg. speed  %.2f km/h\n", r.Totals.DistanceKM, r.Totals.AverageSpeed)
	}
	if r.StepsAvailable {
		fmt.Printf("Steps       %d (estimated)\n", r.Totals.Steps)
	}
	if r.CaloriesAvailable {
		fmt.Printf("Calories    %.0f kcal (estimated)\n", r.Totals.Calories)
	}
	for _, w := range r.Warnings {
		fmt.Println("Note:", w)
	}
	return nil
}
