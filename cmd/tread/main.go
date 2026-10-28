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
	"strconv"
	"syscall"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/ble"
	"github.com/xarcdotdev/oss-treadmill/internal/config"
	controls "github.com/xarcdotdev/oss-treadmill/internal/control"
	"github.com/xarcdotdev/oss-treadmill/internal/demo"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
	"github.com/xarcdotdev/oss-treadmill/internal/server"
	"github.com/xarcdotdev/oss-treadmill/internal/stats"
	"github.com/xarcdotdev/oss-treadmill/internal/store"
)

const usage = `usage: tread [-config path] <command> [options]

  start | resume | pause | stop    Control the treadmill and report usage
  speed <1-8>                     Set a speed level and queue usage
  toggle                          Toggle known running/paused/stopped state
  speed-up | speed-down           Adjust known running speed by one level
  reset-state                     Forget local knowledge; preserve usage
  status                          Send the device status query (no telemetry decoding)
  sync                            Retry queued usage events
  stats [-period 1w] [-json]       Usage from server (also supports -days N)
  config                          Show effective file-backed configuration
  serve [-demo] [-listen addr]     Run SQLite event API and dashboard
  help                            Show help

Periods: all, 3y, 1y, 3m, 1m, 1w, 1d
Config: TREAD_CONFIG or ~/.config/tread/config.toml
Demo uses an isolated in-memory SQLite database and never uses Bluetooth.`

func main() {
	log.SetFlags(0)
	if err := run(os.Args[1:]); err != nil && !errors.Is(err, flag.ErrHelp) {
		log.Fatal(err)
	}
}
func run(args []string) error {
	root := flag.NewFlagSet("tread", flag.ContinueOnError)
	path := root.String("config", "", "config file")
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
	if cmd == "help" {
		fmt.Println(usage)
		return nil
	}
	var cfg config.Config
	var err error
	if *path != "" {
		cfg, err = config.LoadFile(*path)
	} else {
		cfg, err = config.Load()
	}
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	switch cmd {
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
		if len(args) > 0 {
			return errors.New("usage: tread sync")
		}
		return syncUsage(newController(cfg))
	case "start", "resume", "pause", "stop", "status", "speed", "toggle", "speed-up", "speed-down", "reset-state":
		level := 0
		if cmd == "speed" {
			if len(args) != 1 {
				return errors.New("usage: tread speed <1-8>")
			}
			level, err = strconv.Atoi(args[0])
			if err != nil {
				return errors.New("speed must be 1–8")
			}
		} else if len(args) > 0 {
			return fmt.Errorf("%s takes no arguments", cmd)
		}
		return control(cfg, cmd, level)
	default:
		return fmt.Errorf("unknown command %q; use tread help", cmd)
	}
}

// A file lock serializes separate CLI processes so simultaneous speed commands
// cannot overwrite one another's offline events. This project targets Linux/BlueZ.
func withLock(cfg config.Config, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(cfg.QueuePath()), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(cfg.QueuePath()+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}
func newController(cfg config.Config) *controls.Controller {
	return &controls.Controller{Config: cfg, Queue: event.NewClient(cfg.ServerURL, cfg.QueuePath()), Send: ble.Send}
}
func control(cfg config.Config, action string, level int) error {
	return controlWith(newController(cfg), action, level)
}

func controlWith(c *controls.Controller, action string, level int) error {
	var result controls.Result
	var snapshot []event.Event
	var snapshotErr error
	if err := withLock(c.Config, func() error {
		var err error
		result, err = c.ExecuteResult(action, level)
		if err == nil && result.Motion {
			snapshot, snapshotErr = c.Queue.Snapshot()
		}
		return err
	}); err != nil {
		return err
	}
	fmt.Println(result.Message)
	if !result.Motion {
		return nil
	}
	if snapshotErr == nil {
		snapshotErr = deliver(c, snapshot)
	}
	if snapshotErr != nil {
		log.Printf("command sent; usage delivery pending: %v", snapshotErr)
	} else {
		fmt.Println("Usage delivered.")
	}
	return nil
}

func syncUsage(c *controls.Controller) error {
	var snapshot []event.Event
	if err := withLock(c.Config, func() error {
		var err error
		snapshot, err = c.Sync()
		return err
	}); err != nil {
		return err
	}
	return deliver(c, snapshot)
}

func deliver(c *controls.Controller, snapshot []event.Event) error {
	if len(snapshot) == 0 {
		return nil
	}
	if err := c.Queue.Post(snapshot); err != nil {
		return err
	}
	return withLock(c.Config, func() error { return c.Queue.Acknowledge(snapshot) })
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
	srv := http.Server{Addr: *listen, Handler: server.New(st, cfg, *mock).Handler(), ReadHeaderTimeout: 5 * time.Second}
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
	if resp.StatusCode != 200 {
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
