// Command tread controls a DFT200 treadmill and tracks its usage.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/ble"
	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
	"github.com/xarcdotdev/oss-treadmill/internal/protocol"
	"github.com/xarcdotdev/oss-treadmill/internal/server"
	"github.com/xarcdotdev/oss-treadmill/internal/stats"
	"github.com/xarcdotdev/oss-treadmill/internal/store"
)

const usage = `usage: tread <command> [args]

control:
  start            start or resume
  pause            pause
  stop             stop
  speed <1-8>      set speed level

tracking:
  serve            run the event API and dashboard
  stats [-days N]  show usage from the server

config: ` + "~/.config/tread/config.toml"

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(2)
	}
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "start":
		err = control(cfg, event.Start, 0, protocol.Encode(protocol.CmdStart, 0))
	case "pause":
		err = control(cfg, event.Pause, 0, protocol.Encode(protocol.CmdPause, 0))
	case "stop":
		err = control(cfg, event.Stop, 0, protocol.Encode(protocol.CmdStop, 0))
	case "speed":
		err = speed(cfg, args)
	case "serve":
		err = serve(cfg)
	case "stats":
		err = showStats(cfg, args)
	case "-h", "--help", "help":
		fmt.Println(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s\n", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Fatal(err)
	}
}

func speed(cfg config.Config, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: tread speed <1-8>")
	}
	level, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("speed: %w", err)
	}
	packet, err := protocol.Speed(level)
	if err != nil {
		return err
	}
	return control(cfg, event.Speed, level, packet)
}

// control sends the packet, then reports the event. A failed report is only a warning:
// the event stays queued and the treadmill has already been controlled.
func control(cfg config.Config, action event.Action, value int, packet []byte) error {
	if cfg.MAC == "" {
		return errors.New("mac not set in config")
	}
	if err := ble.Send(cfg.MAC, packet); err != nil {
		return err
	}
	if err := os.MkdirAll(config.Dir(), 0o700); err != nil {
		return err
	}
	client := event.NewClient(cfg.ServerURL, filepath.Join(config.Dir(), "queue.jsonl"))
	if err := client.Report(event.New(cfg.Device, action, value)); err != nil {
		log.Printf("warning: %v", err)
	}
	return nil
}

func serve(cfg config.Config) error {
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	log.Printf("listening on %s, db %s", cfg.Listen, cfg.DBPath)
	return http.ListenAndServe(cfg.Listen, server.New(st).Handler())
}

func showStats(cfg config.Config, args []string) error {
	fs := flag.NewFlagSet("stats", flag.ExitOnError)
	days := fs.Int("days", 7, "number of days")
	fs.Parse(args)
	if cfg.ServerURL == "" {
		return errors.New("server_url not set in config")
	}

	resp, err := http.Get(fmt.Sprintf("%s/api/stats?days=%d", cfg.ServerURL, *days))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server: %s", resp.Status)
	}
	var body struct {
		Days []stats.Day `json:"days"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return err
	}

	var total time.Duration
	var km float64
	for _, d := range body.Days {
		fmt.Printf("%s  %6s  %5.2f km\n", d.Date, d.Walking.Round(time.Minute), d.Distance)
		total += d.Walking
		km += d.Distance
	}
	fmt.Printf("total       %6s  %5.2f km\n", total.Round(time.Minute), km)
	return nil
}
