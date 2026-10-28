package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/control"
	"github.com/xarcdotdev/oss-treadmill/internal/observed"
	"github.com/xarcdotdev/oss-treadmill/internal/ownership"
)

const maxBody = 16384

type CommandRequest struct {
	Action string `json:"action"`
	Level  *int   `json:"level,omitempty"`
}
type CommandResponse struct {
	Result          control.Result `json:"result"`
	DeliveryPending bool           `json:"delivery_pending"`
	DeliveryMessage string         `json:"delivery_message,omitempty"`
	Error           string         `json:"error"`
	OutcomeUnknown  bool           `json:"outcome_unknown"`
}

func (r CommandRequest) validate() error {
	switch r.Action {
	case "speed":
		if r.Level == nil || *r.Level < 1 || *r.Level > 8 {
			return errors.New("speed requires level 1–8")
		}
		return nil
	case "start", "resume", "pause", "stop", "status", "toggle", "speed-up", "speed-down", "reset-state":
	default:
		return errors.New("unknown action")
	}
	if r.Level != nil {
		return errors.New("level is allowed only for speed")
	}
	return nil
}
func decode(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
func respond(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func (h *host) handle(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, err string) { respond(w, status, CommandResponse{Error: err}) }
	if r.URL.RawQuery != "" {
		fail(400, "queries not allowed")
		return
	}
	command := r.Method == "POST" && r.URL.Path == "/v1/command"
	snapshot := r.Method == "GET" && r.URL.Path == "/v1/observed"
	syncUsage := r.Method == "POST" && r.URL.Path == "/v1/sync"
	if !command && !snapshot && !syncUsage {
		fail(404, "unknown endpoint")
		return
	}
	var req CommandRequest
	body := http.MaxBytesReader(w, r.Body, maxBody)
	if command {
		// Reject null levels as well as explicit levels on non-speed actions.
		var fields map[string]json.RawMessage
		b, err := io.ReadAll(body)
		if err != nil {
			fail(400, "request too large")
			return
		}
		if err = decode(bytes.NewReader(b), &req); err != nil {
			fail(400, err.Error())
			return
		}
		if err = json.Unmarshal(b, &fields); err != nil {
			fail(400, err.Error())
			return
		}
		if level, ok := fields["level"]; ok && (req.Action != "speed" || string(level) == "null") {
			fail(400, "level is allowed only for speed and must be an integer")
			return
		}
		if err = req.validate(); err != nil {
			fail(400, err.Error())
			return
		}
	} else {
		b, err := io.ReadAll(body)
		if err != nil || len(b) != 0 {
			fail(400, "body not allowed")
			return
		}
	}
	h.mu.Lock()
	if h.stopping || h.ctx.Err() != nil || r.Context().Err() != nil {
		h.mu.Unlock()
		fail(503, "agent stopping")
		return
	}
	if command && h.busy {
		h.mu.Unlock()
		fail(409, "command already in progress; not queued")
		return
	}
	session := h.active
	if command {
		h.busy = true
		if req.Action != "reset-state" && h.suspended {
			h.suspended = false
			select {
			case h.wake <- struct{}{}:
			default:
			}
		}
	}
	h.work.Add(1)
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		if command {
			h.busy = false
		}
		h.mu.Unlock()
		h.work.Done()
	}()
	if command && session == nil && req.Action != "reset-state" {
		if session = h.waitActive(r.Context()); session == nil {
			fail(503, "device unavailable; no command sent")
			return
		}
	}
	if snapshot {
		respond(w, 200, h.store.Snapshot())
		return
	}
	service := h.service(session)
	if syncUsage {
		err := service.Sync()
		out := CommandResponse{}
		if err != nil {
			out.Error = err.Error()
		}
		respond(w, 200, out)
		return
	}
	level := 0
	if req.Level != nil {
		level = *req.Level
	}
	h.mu.Lock()
	stopping := h.stopping
	h.mu.Unlock()
	if stopping || h.ctx.Err() != nil || r.Context().Err() != nil {
		fail(503, "request canceled; no command sent")
		return
	}
	result, err := service.Execute(req.Action, level)
	out := CommandResponse{Result: result.Result, DeliveryPending: result.DeliveryPending, DeliveryMessage: result.DeliveryMessage}
	if err != nil {
		out.Error = err.Error()
		out.OutcomeUnknown = strings.Contains(out.Error, "unknown") || strings.Contains(out.Error, "command sent;")
	}
	respond(w, 200, out)
}

// notSent marks failures where the request certainly never reached a command
// handler: no agent socket, a failed connect, or a refusal the agent sends
// before executing anything.
type notSent struct{ err error }

func (e *notSent) Error() string { return e.err.Error() }
func (e *notSent) Unwrap() error { return e.err }

// Client never retries, redirects, or falls back to direct BLE access.
type Client struct{ runtimeDir string }

func NewClient(runtimeDir string) *Client {
	if runtimeDir == "" {
		runtimeDir = ownership.RuntimeDir()
	}
	return &Client{runtimeDir: runtimeDir}
}

func validatePath(dir string) error {
	if !filepath.IsAbs(dir) {
		return errors.New("runtime directory must be absolute")
	}
	path := "/"
	for _, part := range strings.Split(filepath.Clean(dir), "/") {
		if part == "" {
			continue
		}
		path = filepath.Join(path, part)
		st, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return errors.New("runtime path must contain real directories")
		}
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || st.Mode().Perm() != 0700 || st.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return errors.New("runtime directory must be private (0700) and owned by current user")
	}
	st, err = os.Lstat(filepath.Join(dir, ownership.SocketName))
	if err != nil {
		return err
	}
	stat, ok = st.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0600 {
		return errors.New("agent socket must be owned by current user with mode 0600")
	}
	return nil
}
func (c *Client) request(ctx context.Context, method, path string, body []byte, out any) error {
	if err := validatePath(c.runtimeDir); err != nil {
		return &notSent{fmt.Errorf("tread agent not running: %w", err)}
	}
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", filepath.Join(c.runtimeDir, ownership.SocketName))
		if err != nil {
			return nil, &notSent{fmt.Errorf("tread agent not reachable: %w", err)}
		}
		return conn, nil
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	// POST requests have no retry-safe identifier; each call uses a new transport.
	req.GetBody = nil
	resp, err := client.Do(req)
	if err != nil {
		var refused *notSent
		if errors.As(err, &refused) {
			return refused
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		// The agent answers non-200 only before executing; its JSON error says why.
		var refusal CommandResponse
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		if resp.StatusCode >= 400 && json.Unmarshal(b, &refusal) == nil && refusal.Error != "" {
			return &notSent{errors.New(refusal.Error)}
		}
		return fmt.Errorf("agent HTTP status %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return err
	}
	if len(b) > maxBody {
		return errors.New("oversized agent response")
	}
	if _, ok := out.(*CommandResponse); ok {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(b, &fields); err != nil {
			return err
		}
		for _, key := range []string{"result", "delivery_pending", "error", "outcome_unknown"} {
			value, present := fields[key]
			if !present || string(value) == "null" {
				return fmt.Errorf("missing response field %s", key)
			}
		}
	}
	return decode(bytes.NewReader(b), out)
}

// Execute treats every ambiguous transport or response error as outcome unknown.
// Do not retry a command after such an error; inspect/reset knowledge explicitly.
func (c *Client) Execute(ctx context.Context, req CommandRequest) (out CommandResponse, err error) {
	if err = req.validate(); err != nil {
		return out, err
	}
	body, _ := json.Marshal(req)
	err = c.request(ctx, "POST", "/v1/command", body, &out)
	if err == nil && out.Error == "" && out.Result.Message == "" {
		err = errors.New("command response lacks a result message")
	}
	var refused *notSent
	if errors.As(err, &refused) {
		out.Error = refused.Error()
		if !strings.Contains(out.Error, "no command sent") && !strings.Contains(out.Error, "not queued") {
			out.Error += "; no command sent"
		}
		return out, errors.New(out.Error)
	}
	if err != nil {
		out.OutcomeUnknown = true
		out.Error = fmt.Sprintf("command outcome unknown; do not retry: %v", err)
		return out, errors.New(out.Error)
	}
	if out.Error != "" {
		err = errors.New(out.Error)
	}
	return
}
func (c *Client) Snapshot(ctx context.Context) (out observed.Snapshot, err error) {
	err = c.request(ctx, "GET", "/v1/observed", nil, &out)
	return
}
func (c *Client) Sync(ctx context.Context) error {
	var out CommandResponse
	if err := c.request(ctx, "POST", "/v1/sync", nil, &out); err != nil {
		return err
	}
	if out.Error != "" {
		return errors.New(out.Error)
	}
	return nil
}
