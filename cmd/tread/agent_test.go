package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/agent"
	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/control"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
	"github.com/xarcdotdev/oss-treadmill/internal/observed"
	"github.com/xarcdotdev/oss-treadmill/internal/ownership"
)

func privateRuntime(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TREAD_RUNTIME_DIR", dir)
	return dir
}

func fakeAgentServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	dir := privateRuntime(t)
	path := filepath.Join(dir, ownership.SocketName)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		listener.Close()
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	done := make(chan struct{})
	go func() { defer close(done); server.Serve(listener) }()
	t.Cleanup(func() { server.Close(); <-done })
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	old := os.Stdout
	os.Stdout = f
	defer func() { os.Stdout = old }()
	err = fn()
	if _, seekErr := f.Seek(0, 0); seekErr != nil {
		t.Fatal(seekErr)
	}
	b, readErr := io.ReadAll(f)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return string(b), err
}

func brokenConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[broken"), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestAgentCommandRoutesOnceWithoutLocalConfig(t *testing.T) {
	for _, action := range []string{"start", "resume", "pause", "stop", "status", "speed", "toggle", "speed-up", "speed-down", "reset-state"} {
		t.Run(action, func(t *testing.T) {
			var calls atomic.Int32
			requests := make(chan agent.CommandRequest, 2)
			fakeAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.Method != "POST" || r.URL.Path != "/v1/command" {
					t.Errorf("request %s %s", r.Method, r.URL)
				}
				var req agent.CommandRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				requests <- req
				json.NewEncoder(w).Encode(agent.CommandResponse{Result: control.Result{Message: "query or control sent"}})
			})
			path := brokenConfig(t)
			args := []string{"-config", path, action}
			if action == "speed" {
				args = append(args, "3")
			}
			if err := run(args); err != nil {
				t.Fatal(err)
			}
			req := <-requests
			if calls.Load() != 1 || req.Action != action {
				t.Fatalf("calls=%d request=%+v", calls.Load(), req)
			}
			if action == "speed" {
				if req.Level == nil || *req.Level != 3 {
					t.Fatalf("speed level pointer: %+v", req)
				}
			} else if req.Level != nil {
				t.Fatalf("non-speed level supplied: %+v", req)
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(path), "queue.jsonl.lock")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("agent client touched local control files: %v", err)
			}
		})
	}
}

func TestAgentErrorsNeverRetryOrFallBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"malformed", 200, `{`, "outcome unknown"},
		{"missing fields", 200, `{}`, "outcome unknown"},
		{"empty success", 200, `{"result":{},"delivery_pending":false,"error":"","outcome_unknown":false}`, "outcome unknown"},
		{"refusal before execution", 503, `{"error":"device unavailable; no command sent"}`, "device unavailable; no command sent"},
		{"http without agent error", 503, ``, "outcome unknown"},
		{"redirect", 307, "", "outcome unknown"},
		{"application rejection", 200, `{"result":{},"delivery_pending":false,"error":"device unavailable; no command sent","outcome_unknown":false}`, "device unavailable; no command sent"},
		{"unknown motor outcome", 200, `{"result":{},"delivery_pending":false,"error":"transport outcome unknown; do not retry","outcome_unknown":true}`, "outcome unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			fakeAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Location", "/v1/command")
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			})
			err := run([]string{"-config", brokenConfig(t), "start"})
			if err == nil || !strings.Contains(err.Error(), tc.want) || calls.Load() != 1 {
				t.Fatalf("calls=%d err=%v", calls.Load(), err)
			}
		})
	}
	t.Run("agent absent", func(t *testing.T) {
		privateRuntime(t)
		err := run([]string{"-config", brokenConfig(t), "start"})
		if err == nil || !strings.Contains(err.Error(), "not running") || !strings.Contains(err.Error(), "no command sent") || strings.Contains(err.Error(), "outcome unknown") {
			t.Fatal(err)
		}
	})
}

func TestAgentDeliveryWarningIsNotMotorError(t *testing.T) {
	fakeAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(agent.CommandResponse{Result: control.Result{Message: "start sent; usage queued", Motion: true}, DeliveryPending: true, DeliveryMessage: "server rejected usage"})
	})
	var logs bytes.Buffer
	old := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(old)
	out, err := captureStdout(t, func() error { return run([]string{"start"}) })
	if err != nil || !strings.Contains(out, "start sent") || strings.Contains(out, "Usage delivered") || !strings.Contains(logs.String(), "command sent; usage delivery pending: server rejected usage") {
		t.Fatalf("out=%q logs=%q err=%v", out, &logs, err)
	}
}

func TestDirectConflictPrecedesConfigAndPreservesOwner(t *testing.T) {
	dir := privateRuntime(t)
	owner, err := ownership.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	before, err := os.Stat(filepath.Join(dir, ownership.LockName))
	if err != nil {
		t.Fatal(err)
	}
	path := brokenConfig(t)
	if err := run([]string{"-config", path, "-direct", "start"}); !errors.Is(err, ownership.ErrOwned) {
		t.Fatalf("config or hardware accessed before ownership: %v", err)
	}
	after, err := os.Stat(filepath.Join(dir, ownership.LockName))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("owner lock replaced: %v", err)
	}
	if _, err := ownership.Acquire(dir); !errors.Is(err, ownership.ErrOwned) {
		t.Fatalf("owner stolen: %v", err)
	}
	for _, name := range []string{"queue.jsonl.lock", "control-state.json"} {
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("local side effect %s: %v", name, err)
		}
	}
}

func TestDirectOwnershipHeldThroughUnresolvedSend(t *testing.T) {
	dir := privateRuntime(t)
	c := deliveryController(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var sends atomic.Int32
	c.Send = func(string, string, []byte) error {
		sends.Add(1)
		close(entered)
		<-release
		return errors.New("unresolved transport finished with failure")
	}
	done := make(chan error, 1)
	go func() { done <- withOwnership(func() error { return controlWith(c, "start", 0) }) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("no injected send")
	}
	if lock, err := ownership.Acquire(dir); !errors.Is(err, ownership.ErrOwned) {
		if lock != nil {
			lock.Close()
		}
		t.Errorf("ownership released during synchronous send: %v", err)
	}
	close(release)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "do not automatically retry") || sends.Load() != 1 {
		t.Fatalf("sends=%d err=%v", sends.Load(), err)
	}
	lock, err := ownership.Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	lock.Close()
}

func TestSyncRoutesAndDirectIsNetworkOnly(t *testing.T) {
	t.Run("agent default", func(t *testing.T) {
		var calls atomic.Int32
		fakeAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.Method != "POST" || r.URL.Path != "/v1/sync" {
				t.Errorf("request %s %s", r.Method, r.URL)
			}
			json.NewEncoder(w).Encode(agent.CommandResponse{})
		})
		if err := run([]string{"-config", brokenConfig(t), "sync"}); err != nil || calls.Load() != 1 {
			t.Fatalf("calls=%d err=%v", calls.Load(), err)
		}
	})
	for _, status := range []int{200, 400} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			dir := privateRuntime(t)
			owner, err := ownership.Acquire(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts.Add(1)
				w.WriteHeader(status)
			}))
			defer server.Close()
			t.Setenv("TREAD_STATE_DIR", "")
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte("server_url = "+`"`+server.URL+`"`+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			queue := event.NewClient(server.URL, cfg.QueuePath())
			if err := queue.Enqueue(event.New(cfg.Device, event.Stop, 0)); err != nil {
				t.Fatal(err)
			}
			err = run([]string{"-config", path, "-direct", "sync"})
			if (err != nil) != (status != 200) || posts.Load() != 1 {
				t.Fatalf("posts=%d err=%v", posts.Load(), err)
			}
			queued, err := queue.Snapshot()
			if err != nil || (len(queued) == 0) != (status == 200) {
				t.Fatalf("queue=%v err=%v", queued, err)
			}
		})
	}
}

func TestObservedRenderingAndJSON(t *testing.T) {
	value := 2.5
	now := time.Now().UTC()
	snapshot := observed.Snapshot{Device: "test-device", Connection: observed.ConnectionConnected, Telemetry: observed.TelemetryStale,
		Capabilities: observed.Capabilities{ObservedFields: []observed.Field{observed.FieldState, observed.FieldSpeed, observed.FieldDistance}},
		State:        observed.State{Value: observed.StateUnknown, Stale: true},
		Measurements: map[observed.Field]observed.Measurement{
			observed.FieldSpeed: {Value: &value, Unit: observed.UnitUnknown, UnitConfidence: observed.UnitUnknownConfidence, Timestamp: &now, Stale: true},
		},
	}
	var out bytes.Buffer
	if err := printObserved(&out, snapshot, false); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Telemetry: stale", "State: unknown (stale)", "speed: 2.5 (units unknown) (stale)", "distance: unknown", "pulse: unsupported"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %q", want, &out)
		}
	}
	if strings.Contains(out.String(), "km/h") {
		t.Fatal("invented physical units")
	}
	fakeAgentServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/v1/observed" {
			t.Errorf("request %s %s", r.Method, r.URL)
		}
		json.NewEncoder(w).Encode(snapshot)
	})
	raw, err := captureStdout(t, func() error { return run([]string{"-config", brokenConfig(t), "observed", "-json"}) })
	if err != nil {
		t.Fatal(err)
	}
	var decoded observed.Snapshot
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil || !reflect.DeepEqual(snapshot, decoded) {
		t.Fatalf("JSON changed snapshot: %s err=%v", raw, err)
	}
	for _, telemetry := range []observed.TelemetryStatus{observed.TelemetryDisabled, observed.TelemetryUnsupported} {
		out.Reset()
		snapshot.Telemetry = telemetry
		snapshot.Measurements = nil
		snapshot.State = observed.State{Value: observed.StateUnknown}
		if err := printObserved(&out, snapshot, false); err != nil || !strings.Contains(out.String(), "speed: "+string(telemetry)) {
			t.Fatalf("%s: %s err=%v", telemetry, &out, err)
		}
	}
}

func TestAgentSignalContext(t *testing.T) {
	dir := privateRuntime(t)
	cfg := config.Default()
	stopped := false
	sentinel := errors.New("agent runner ended")
	err := runAgentWith(cfg, func(parent context.Context, signals ...os.Signal) (context.Context, context.CancelFunc) {
		if !reflect.DeepEqual(signals, []os.Signal{os.Interrupt, syscall.SIGTERM}) {
			t.Fatalf("signals=%v", signals)
		}
		ctx, cancel := context.WithCancel(parent)
		cancel()
		return ctx, func() { stopped = true }
	}, func(ctx context.Context, got config.Config, opts agent.Options) error {
		if ctx.Err() != context.Canceled || opts.RuntimeDir != dir || !reflect.DeepEqual(got, cfg) || stopped {
			t.Fatalf("context=%v opts=%+v stopped=%v", ctx.Err(), opts, stopped)
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) || !stopped {
		t.Fatalf("err=%v stopped=%v", err, stopped)
	}
}

func TestAgentArgumentsAndHelp(t *testing.T) {
	path := brokenConfig(t)
	privateRuntime(t)
	for _, args := range [][]string{
		{"agent", "extra"}, {"agent", "-signal", "TERM"}, {"-direct", "agent"},
		{"observed", "extra"}, {"observed", "-unknown"}, {"-direct", "observed"},
		{"sync", "extra"}, {"start", "extra"}, {"speed"}, {"speed", "fast"}, {"speed", "2", "3"},
	} {
		err := run(append([]string{"-config", path}, args...))
		if err == nil || strings.Contains(err.Error(), "config:") || strings.Contains(err.Error(), "outcome unknown") {
			t.Errorf("args=%q err=%v", args, err)
		}
	}
	for _, args := range [][]string{{"help"}, {"-help"}} {
		out, err := captureStdout(t, func() error { return run(args) })
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"[-direct]", "speed <level>", "observed [-json]", "agent", "setup-host", "no physical"} {
			if want == "no physical" {
				want = "not physical speed"
			}
			if !strings.Contains(out, want) {
				t.Errorf("help missing %q", want)
			}
		}
	}
}
