package control

import (
	"os"
	"path/filepath"
	"syscall"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/event"
)

// WithLock shares the CLI's per-configuration journal and queue lock.
func WithLock(cfg config.Config, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(cfg.QueuePath()), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(cfg.QueuePath()+".lock", os.O_CREATE|os.O_RDWR, 0600)
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

type Service struct{ Controller *Controller }
type ServiceResult struct {
	Result          Result `json:"result"`
	DeliveryPending bool   `json:"delivery_pending"`
	DeliveryMessage string `json:"delivery_message,omitempty"`
}

func (s *Service) Execute(action string, level int) (out ServiceResult, err error) {
	c := s.Controller
	var batch []event.Event
	var deliveryErr error
	err = WithLock(c.Config, func() error {
		var e error
		out.Result, e = c.ExecuteResult(action, level)
		if e == nil && out.Result.Motion {
			batch, deliveryErr = c.Queue.Snapshot()
		}
		return e
	})
	if err != nil || !out.Result.Motion {
		return
	}
	if deliveryErr == nil {
		deliveryErr = s.deliver(batch)
	}
	if deliveryErr != nil {
		out.DeliveryPending = true
		out.DeliveryMessage = deliveryErr.Error()
	}
	return
}
func (s *Service) Sync() error {
	var batch []event.Event
	if err := WithLock(s.Controller.Config, func() error {
		var err error
		batch, err = s.Controller.Sync()
		return err
	}); err != nil {
		return err
	}
	return s.deliver(batch)
}
func (s *Service) deliver(batch []event.Event) error {
	if len(batch) == 0 {
		return nil
	}
	c := s.Controller
	if err := c.Queue.Post(batch); err != nil {
		return err
	}
	return WithLock(c.Config, func() error { return c.Queue.Acknowledge(batch) })
}
