package event

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Client posts events to the server. Events that can't be delivered
// are appended to a local queue file and resent on the next Report.
type Client struct {
	ServerURL string
	QueuePath string
	HTTP      *http.Client
}

func NewClient(serverURL, queuePath string) *Client {
	return &Client{ServerURL: serverURL, QueuePath: queuePath, HTTP: &http.Client{Timeout: 5 * time.Second}}
}

// Report sends queued events plus e. On failure everything stays queued.
// Report and Flush require exclusive queue access for their entire duration.
// Concurrent callers should use Snapshot/Post/Acknowledge with external locking.
func (c *Client) Report(e Event) error {
	events, err := c.readQueue()
	if err != nil {
		return err
	}
	events = append(events, e)
	if err = c.writeQueue(events); err != nil {
		return err
	}
	return c.Flush()
}
func (c *Client) Flush() error {
	events, err := c.Snapshot()
	if err != nil {
		return err
	}
	if err := c.Post(events); err != nil {
		return err
	}
	return c.Acknowledge(events)
}

// Snapshot reads an independent batch under the caller's queue lock.
func (c *Client) Snapshot() ([]Event, error) { return c.readQueue() }

// Post sends only the immutable snapshot; it never accesses the queue or needs
// its lock. A failed/ambiguous delivery can be retried using the same event IDs.
func (c *Client) Post(events []Event) error {
	if len(events) == 0 {
		return nil
	}
	if c.ServerURL == "" {
		return fmt.Errorf("%d event(s) queued; server_url is empty", len(events))
	}
	if err := c.post(events); err != nil {
		return fmt.Errorf("%d event(s) queued: %w", len(events), err)
	}
	return nil
}

// Acknowledge durably removes only delivered IDs, preserving concurrent additions.
// The caller must reacquire the queue lock after Post succeeds.
func (c *Client) Acknowledge(delivered []Event) error {
	if len(delivered) == 0 {
		return nil
	}
	events, err := c.readQueue()
	if err != nil {
		return err
	}
	ids := make(map[string]bool, len(delivered))
	for _, e := range delivered {
		ids[e.ID] = true
	}
	remaining := events[:0]
	for _, e := range events {
		if !ids[e.ID] {
			remaining = append(remaining, e)
		}
	}
	return c.writeQueue(remaining)
}

func (c *Client) post(events []Event) error {
	body, err := json.Marshal(events)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Post(c.ServerURL+"/api/events", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("server: %s", resp.Status)
	}
	return nil
}

// The queue is JSON lines, one event per line.
func (c *Client) readQueue() ([]Event, error) {
	f, err := os.Open(c.QueuePath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var events []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return nil, fmt.Errorf("queue %s: %w", c.QueuePath, err)
		}
		if err := e.Validate(time.Now()); err != nil {
			return nil, fmt.Errorf("queue %s: %w", c.QueuePath, err)
		}
		events = append(events, e)
	}
	return events, sc.Err()
}

func (c *Client) writeQueue(events []Event) error {
	if len(events) == 0 {
		err := os.Remove(c.QueuePath)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		return syncDir(filepath.Dir(c.QueuePath))
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range events {
		if err := enc.Encode(e); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(c.QueuePath), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(c.QueuePath), ".queue-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(buf.Bytes()); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), c.QueuePath); err != nil {
		return err
	}
	return syncDir(filepath.Dir(c.QueuePath))
}

// Preflight validates and durably rewrites the queue before a physical action.
// Callers must hold the per-configuration queue lock.
func (c *Client) Preflight() error {
	events, err := c.readQueue()
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	return c.writeQueue(events)
}

// Enqueue durably records confirmed events without network access. Replaying
// journal IDs is safe, including after an ambiguous queue commit.
func (c *Client) Enqueue(pending ...Event) error {
	events, err := c.readQueue()
	if err != nil {
		return err
	}
	seen := make(map[string]bool)
	for _, e := range events {
		seen[e.ID] = true
	}
	for _, e := range pending {
		if err := e.Validate(time.Now()); err != nil {
			return err
		}
		if !seen[e.ID] {
			events = append(events, e)
			seen[e.ID] = true
		}
	}
	return c.writeQueue(events)
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
