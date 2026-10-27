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
	if c.ServerURL == "" {
		return c.writeQueue(events)
	}
	if err := c.post(events); err != nil {
		if qerr := c.writeQueue(events); qerr != nil {
			return qerr
		}
		return fmt.Errorf("queued %d event(s): %w", len(events), err)
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
	return os.WriteFile(c.QueuePath, buf.Bytes(), 0o600)
}
