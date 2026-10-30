// Command demo runs an isolated agent and server for screenshots and recordings.
//
// The agent talks to a fake Bluetooth backend that accepts every command, and
// the server keeps generated history in a SQLite file inside -dir, so the real
// tread CLI works end to end without a treadmill or your data:
//
//	go run ./scripts/demo -dir "$(mktemp -d)" -listen 127.0.0.1:18799
//
// Point tread at it with TREAD_CONFIG=<dir>/config.toml,
// TREAD_STATE_DIR=<dir>/state and TREAD_RUNTIME_DIR=<dir>/run.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/xarcdotdev/oss-treadmill/internal/agent"
	"github.com/xarcdotdev/oss-treadmill/internal/ble"
	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/demo"
	"github.com/xarcdotdev/oss-treadmill/internal/server"
	"github.com/xarcdotdev/oss-treadmill/internal/store"
)

// Same targets as goals.example.yaml.
const goalsYAML = `daily:
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
`

func main() {
	log.SetFlags(0)
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	dir := flag.String("dir", "", "directory for config, database and agent state (required)")
	listen := flag.String("listen", "127.0.0.1:18799", "dashboard address")
	flag.Parse()
	if *dir == "" || flag.NArg() != 0 {
		flag.Usage()
		return errors.New("usage: demo -dir <dir> [-listen addr]")
	}
	cfg, err := writeConfig(*dir, *listen)
	if err != nil {
		return err
	}
	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.AddEvents(demo.Events(time.Now(), cfg.Location())); err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runtimeDir := filepath.Join(*dir, "run")
	app := server.New(st, cfg, false)
	app.Live = agent.NewClient(runtimeDir).Snapshot
	srv := http.Server{Addr: cfg.Listen, Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second}
	errs := make(chan error, 2)
	go func() { errs <- agent.Run(ctx, cfg, agent.Options{RuntimeDir: runtimeDir, Backend: fakeBackend{}}) }()
	go func() { errs <- srv.ListenAndServe() }()
	log.Printf("Dashboard: http://%s", cfg.Listen)
	select {
	case <-ctx.Done():
	case err = <-errs:
	}
	stop()
	srv.Close()
	return err
}

// writeConfig stores the demo profile where the server reloads it on each
// request, with addresses and paths that stay inside dir.
func writeConfig(dir, listen string) (config.Config, error) {
	for _, d := range []string{dir, filepath.Join(dir, "run"), filepath.Join(dir, "state")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return config.Config{}, err
		}
	}
	cfg := demo.Config()
	cfg.Path = filepath.Join(dir, "config.toml")
	cfg.StateDir = filepath.Join(dir, "state")
	cfg.DBPath = filepath.Join(dir, "tread.db")
	cfg.Listen = listen
	cfg.ServerURL = "http://" + listen
	cfg.Treadmill.MAC = "02:00:00:00:00:01"
	f, err := os.Create(cfg.Path)
	if err != nil {
		return cfg, err
	}
	err = toml.NewEncoder(f).Encode(cfg)
	if e := f.Close(); err == nil {
		err = e
	}
	if err == nil {
		err = os.WriteFile(filepath.Join(dir, "goals.yaml"), []byte(goalsYAML), 0o600)
	}
	if err == nil {
		err = cfg.Validate()
	}
	return cfg, err
}

// fakeBackend is a treadmill that is always in range and accepts every write.
type fakeBackend struct{}

func (fakeBackend) Enable() error                          { return nil }
func (fakeBackend) Connect(string) (ble.Connection, error) { return &fakeConnection{}, nil }

type fakeConnection struct {
	mu     sync.Mutex
	closed bool
}

func (*fakeConnection) Discover([]string) error              { return nil }
func (*fakeConnection) Subscribe(string, func([]byte)) error { return nil }
func (*fakeConnection) Unsubscribe(string) error             { return nil }
func (*fakeConnection) Write(string, []byte) error           { return nil }
func (c *fakeConnection) Connected() (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed, nil
}
func (c *fakeConnection) Disconnect() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}
