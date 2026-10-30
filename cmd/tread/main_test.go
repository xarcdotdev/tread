package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/control"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
)

func fakeController(cfg config.Config) *control.Controller {
	return &control.Controller{Config: cfg, Queue: event.NewClient("", cfg.QueuePath()), BootID: func() (string, error) { return "test-boot", nil }, Send: func(string, string, []byte) error { return nil }}
}

func deliveryController(t *testing.T) *control.Controller {
	t.Helper()
	cfg := config.Default()
	cfg.Path = filepath.Join(t.TempDir(), "config.toml")
	cfg.Treadmill.MAC = "aa:bb:cc:dd:ee:ff"
	return fakeController(cfg)
}

func TestAutomaticDeliveryAndFailure(t *testing.T) {
	for _, failure := range []string{"", "http", "ack"} {
		t.Run(failure, func(t *testing.T) {
			c := deliveryController(t)
			sends, posts := 0, 0
			var received []event.Event
			c.Send = func(string, string, []byte) error { sends++; return nil }
			originalPath := c.Queue.QueuePath
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts++
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Error(err)
				}
				if failure == "http" {
					w.WriteHeader(503)
				}
				if failure == "ack" && posts == 1 {
					// Keep queued bytes while making acknowledgement unreadable.
					if err := os.Rename(originalPath, originalPath+".saved"); err != nil {
						t.Error(err)
					}
					if err := os.Mkdir(originalPath, 0700); err != nil {
						t.Error(err)
					}
				}
			}))
			defer srv.Close()
			c.Queue.ServerURL = srv.URL
			var logs bytes.Buffer
			old := log.Writer()
			log.SetOutput(&logs)
			defer log.SetOutput(old)
			if err := controlWith(c, "start", 0); err != nil {
				t.Fatal(err)
			}
			if failure == "ack" {
				if err := os.Remove(originalPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(originalPath+".saved", originalPath); err != nil {
					t.Fatal(err)
				}
			}
			queued, err := c.Queue.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			if sends != 1 || posts != 1 || len(received) != 1 {
				t.Fatalf("sends=%d posts=%d received=%v", sends, posts, received)
			}
			if failure == "" {
				if len(queued) != 0 {
					t.Fatal(queued)
				}
			} else {
				if len(queued) != 1 || queued[0].ID != received[0].ID {
					t.Fatal(queued)
				}
				if !strings.Contains(logs.String(), "command sent; usage delivery pending") {
					t.Fatal(logs.String())
				}
				if failure == "http" {
					if err := syncUsage(c); err == nil {
						t.Fatal("expected failed retry")
					}
				}
				if failure == "ack" {
					if err := syncUsage(c); err != nil {
						t.Fatal(err)
					}
					if len(received) != 1 || received[0].ID != queued[0].ID {
						t.Fatal("retry changed event ID")
					}
					remaining, err := c.Queue.Snapshot()
					if err != nil || len(remaining) != 0 {
						t.Fatalf("%v %v", remaining, err)
					}
				}
				if sends != 1 {
					t.Fatal("delivery repeated BLE")
				}
			}
		})
	}
}

func TestDeliveryReleasesControlLock(t *testing.T) {
	for _, syncOnly := range []bool{false, true} {
		t.Run(fmt.Sprint(syncOnly), func(t *testing.T) {
			c := deliveryController(t)
			entered, release := make(chan struct{}), make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-release
			}))
			defer srv.Close()
			defer close(release)
			c.Queue.ServerURL = srv.URL
			if syncOnly {
				if err := control.WithLock(c.Config, func() error { _, err := c.Execute("start", 0); return err }); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan error, 1)
			go func() {
				if syncOnly {
					done <- syncUsage(c)
				} else {
					done <- controlWith(c, "start", 0)
				}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("no HTTP")
			}
			physical := make(chan error, 1)
			go func() {
				physical <- control.WithLock(c.Config, func() error {
					other := fakeController(c.Config)
					_, err := other.Execute("pause", 0)
					return err
				})
			}()
			select {
			case err := <-physical:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("HTTP holds physical control lock")
			}
			// Allow the first acknowledgement, leaving only the concurrent pause.
			release <- struct{}{}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			queued, err := c.Queue.Snapshot()
			if err != nil || len(queued) != 1 || queued[0].Action != event.Pause {
				t.Fatalf("%v %v", queued, err)
			}
		})
	}
}

func TestLocalAndNoMotionDoNotDeliver(t *testing.T) {
	c := deliveryController(t)
	posts, sends := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { posts++ }))
	defer srv.Close()
	c.Queue.ServerURL = srv.URL
	c.Send = func(string, string, []byte) error { sends++; return nil }
	if _, err := c.Execute("start", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Execute("speed", 8); err != nil {
		t.Fatal(err)
	}
	if err := controlWith(c, "speed-up", 0); err != nil {
		t.Fatal(err)
	}
	if sends != 2 || posts != 0 {
		t.Fatal("bound sent or delivered")
	}
	if err := controlWith(c, "status", 0); err != nil {
		t.Fatal(err)
	}
	if sends != 3 || posts != 0 {
		t.Fatal("status reported motion")
	}
	if err := controlWith(c, "reset-state", 0); err != nil {
		t.Fatal(err)
	}
	if sends != 3 || posts != 0 {
		t.Fatal("reset used transport")
	}
	queued, err := c.Queue.Snapshot()
	if err != nil || len(queued) != 2 {
		t.Fatalf("%v %v", queued, err)
	}
}

func TestProcessLock(t *testing.T) {
	if dir := os.Getenv("TREAD_LOCK_TEST_DIR"); dir != "" {
		cfg := config.Default()
		cfg.Path = filepath.Join(dir, "config.toml")
		cfg.Treadmill.MAC = "aa:bb:cc:dd:ee:ff"
		f, err := os.OpenFile(cfg.QueuePath()+".lock", os.O_RDWR, 0600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			t.Fatalf("parent lock not held: %v", err)
		}
		fmt.Println("blocked")
		err = control.WithLock(cfg, func() error {
			c := fakeController(cfg)
			c.Send = func(string, string, []byte) error {
				return os.WriteFile(filepath.Join(dir, "sent"), []byte("fake"), 0600)
			}
			_, err := c.Execute("speed-up", 0)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Path = filepath.Join(dir, "config.toml")
	cfg.Treadmill.MAC = "aa:bb:cc:dd:ee:ff"
	child := exec.Command(os.Args[0], "-test.run=^TestProcessLock$", "-test.timeout=15s")
	child.Env = append(os.Environ(), "TREAD_LOCK_TEST_DIR="+dir)
	pipe, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	child.Stderr = os.Stderr
	err = control.WithLock(cfg, func() error {
		c := fakeController(cfg)
		if _, err := c.Execute("start", 0); err != nil {
			return err
		}
		if _, err := c.Execute("speed", 2); err != nil {
			return err
		}
		if err := child.Start(); err != nil {
			return err
		}
		line, err := bufio.NewReader(pipe).ReadString('\n')
		if err != nil {
			return err
		}
		if line != "blocked\n" {
			return fmt.Errorf("child: %s", line)
		}
		if _, err := os.Stat(filepath.Join(dir, "sent")); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("child sent before lock released: %v", err)
		}
		return nil
	})
	if err != nil {
		if child.Process != nil {
			child.Process.Kill()
			child.Wait()
		}
		t.Fatal(err)
	}
	if err := child.Wait(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(cfg.QueuePath())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var es []event.Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e event.Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		es = append(es, e)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(es) != 3 || es[2].Action != event.Speed || es[2].Value != 3 {
		t.Fatalf("serialized events: %+v", es)
	}
}
