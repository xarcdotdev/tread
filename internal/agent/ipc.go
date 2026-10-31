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

	"github.com/xarcdotdev/oss-treadmill/internal/ble"
	"github.com/xarcdotdev/oss-treadmill/internal/control"
	"github.com/xarcdotdev/oss-treadmill/internal/device"
	"github.com/xarcdotdev/oss-treadmill/internal/observed"
	"github.com/xarcdotdev/oss-treadmill/internal/ownership"
)

const maxBody = 16384

type CommandRequest struct {
	Action control.Action `json:"action"`
	Level  *int           `json:"level,omitempty"`
}

type CommandResponse struct {
	Result          control.Result `json:"result"`
	DeliveryPending bool           `json:"delivery_pending"`
	DeliveryMessage string         `json:"delivery_message,omitempty"`
	Error           string         `json:"error"`
	OutcomeUnknown  bool           `json:"outcome_unknown"`
}

func (r CommandRequest) validate() error {
	if !r.Action.Valid() {
		return errors.New("unknown action")
	}
	if r.Action == control.ActionSpeed {
		// The host checks the level against its driver's range.
		if r.Level == nil {
			return errors.New("speed requires a level")
		}
		return nil
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

func fail(w http.ResponseWriter, status int, err string) {
	respond(w, status, CommandResponse{Error: err})
}

// handle routes the private socket API. Every refusal is JSON so the client can
// tell a refusal before execution from an unknown outcome.
func (h *host) handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		fail(w, http.StatusBadRequest, "queries not allowed")
		return
	}
	switch r.Method + " " + r.URL.Path {
	case "POST /v1/command":
		h.handleCommand(w, r)
	case "GET /v1/observed":
		h.handleSnapshot(w, r)
	case "POST /v1/sync":
		h.handleSync(w, r)
	default:
		fail(w, http.StatusNotFound, "unknown endpoint")
	}
}

func (h *host) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if !rejectBody(w, r) {
		return
	}
	_, release, ok := h.admit(w, r)
	if !ok {
		return
	}
	defer release()
	respond(w, http.StatusOK, h.store.Snapshot())
}

func (h *host) handleSync(w http.ResponseWriter, r *http.Request) {
	if !rejectBody(w, r) {
		return
	}
	session, release, ok := h.admit(w, r)
	if !ok {
		return
	}
	defer release()
	var out CommandResponse
	if err := h.service(session).Sync(); err != nil {
		out.Error = err.Error()
	}
	respond(w, http.StatusOK, out)
}

func (h *host) handleCommand(w http.ResponseWriter, r *http.Request) {
	req, err := h.parseCommand(w, r)
	if err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	session, release, ok := h.admitCommand(w, r, req)
	if !ok {
		return
	}
	defer release()
	needsDevice := req.Action != control.ActionResetState
	if needsDevice && session == nil {
		if session = h.waitActive(r.Context()); session == nil {
			fail(w, http.StatusServiceUnavailable, "device unavailable; no command sent")
			return
		}
	}
	// Last check before the single send: nothing has touched the device yet.
	if h.isStopping(r) {
		fail(w, http.StatusServiceUnavailable, "request canceled; no command sent")
		return
	}
	level := 0
	if req.Level != nil {
		level = *req.Level
	}
	result, err := h.service(session).Execute(req.Action, level)
	out := CommandResponse{
		Result:          result.Result,
		DeliveryPending: result.DeliveryPending,
		DeliveryMessage: result.DeliveryMessage,
	}
	if err != nil {
		out.Error = err.Error()
		_, out.OutcomeUnknown = errors.AsType[*control.UnknownOutcomeError](err)
	}
	respond(w, http.StatusOK, out)
}

// rejectBody answers requests that carry a body and reports whether to continue.
func rejectBody(w http.ResponseWriter, r *http.Request) bool {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil || len(b) != 0 {
		fail(w, http.StatusBadRequest, "body not allowed")
		return false
	}
	return true
}

// parseCommand strictly decodes a command: no unknown fields, no trailing
// data, no null level, and a level only for speed, within the driver's range.
func (h *host) parseCommand(w http.ResponseWriter, r *http.Request) (CommandRequest, error) {
	var req CommandRequest
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		return req, errors.New("request too large")
	}
	if err := decode(bytes.NewReader(b), &req); err != nil {
		return req, err
	}
	// Decoding into the struct cannot distinguish a missing level from null.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(b, &fields); err != nil {
		return req, err
	}
	if level, ok := fields["level"]; ok && (req.Action != control.ActionSpeed || string(level) == "null") {
		return req, errors.New("level is allowed only for speed and must be an integer")
	}
	if err := req.validate(); err != nil {
		return req, err
	}
	if req.Level != nil {
		levels, err := device.SpeedLevels(h.driver)
		if err != nil {
			return req, err
		}
		if !levels.Contains(*req.Level) {
			return req, fmt.Errorf("speed requires a level %s", levels)
		}
	}
	return req, nil
}

// isStopping reports whether the host or this request has been cancelled.
func (h *host) isStopping(r *http.Request) bool {
	h.mu.Lock()
	stopping := h.stopping
	h.mu.Unlock()
	return stopping || h.ctx.Err() != nil || r.Context().Err() != nil
}

// admit registers the request with shutdown bookkeeping so Run waits for it,
// and returns the current session, which may be nil. The caller must release.
func (h *host) admit(w http.ResponseWriter, r *http.Request) (*ble.Session, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopping || h.ctx.Err() != nil || r.Context().Err() != nil {
		fail(w, http.StatusServiceUnavailable, "agent stopping")
		return nil, nil, false
	}
	h.work.Add(1)
	return h.active, h.work.Done, true
}

// admitCommand is admit for the single command slot: only one command runs at
// a time and nothing is queued. A command also wakes an idle-suspended link,
// except reset-state, which never needs the device.
func (h *host) admitCommand(w http.ResponseWriter, r *http.Request, req CommandRequest) (*ble.Session, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.stopping || h.ctx.Err() != nil || r.Context().Err() != nil {
		fail(w, http.StatusServiceUnavailable, "agent stopping")
		return nil, nil, false
	}
	if h.busy {
		fail(w, http.StatusConflict, "command already in progress; not queued")
		return nil, nil, false
	}
	h.busy = true
	if req.Action != control.ActionResetState && h.suspended {
		h.suspended = false
		select {
		case h.wake <- struct{}{}:
		default:
		}
	}
	h.work.Add(1)
	release := func() {
		h.mu.Lock()
		h.busy = false
		h.mu.Unlock()
		h.work.Done()
	}
	return h.active, release, true
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
	for part := range strings.SplitSeq(filepath.Clean(dir), "/") {
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
	socket := filepath.Join(c.runtimeDir, ownership.SocketName)
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", socket)
		if err != nil {
			return nil, &notSent{fmt.Errorf("tread agent not reachable: %w", err)}
		}
		return conn, nil
	}
	transport := &http.Transport{DisableKeepAlives: true, DialContext: dial}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://agent"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	// POST requests have no retry-safe identifier; each call uses a new transport.
	req.GetBody = nil
	resp, err := client.Do(req)
	if err != nil {
		if refused, ok := errors.AsType[*notSent](err); ok {
			return refused
		}
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
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
func (c *Client) Execute(ctx context.Context, req CommandRequest) (CommandResponse, error) {
	var out CommandResponse
	if err := req.validate(); err != nil {
		return out, err
	}
	body, _ := json.Marshal(req)
	err := c.request(ctx, "POST", "/v1/command", body, &out)
	if err == nil && out.Error == "" && out.Result.Message == "" {
		err = errors.New("command response lacks a result message")
	}
	if refused, ok := errors.AsType[*notSent](err); ok {
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
		return out, errors.New(out.Error)
	}
	return out, nil
}

func (c *Client) Snapshot(ctx context.Context) (observed.Snapshot, error) {
	var out observed.Snapshot
	err := c.request(ctx, "GET", "/v1/observed", nil, &out)
	return out, err
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
