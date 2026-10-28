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
	events, err := c.readQueue()
	if err != nil {
		return err
	}
	if len(events) == 0 {
		return nil
	}
	if c.ServerURL == "" {
		return fmt.Errorf("%d event(s) queued; server_url is empty", len(events))
	}
	if err = c.post(events); err != nil {
		return fmt.Errorf("%d event(s) queued: %w", len(events), err)
	}
	return c.writeQueue(nil)
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
		return err
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
	return os.Rename(f.Name(), c.QueuePath)
}
