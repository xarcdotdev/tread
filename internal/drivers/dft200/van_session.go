package dft200

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/device"
	"github.com/xarcdotdev/oss-treadmill/internal/observed"
)

var _ device.TelemetryProvider = Driver{}

// NewTelemetry enables only the experimental VAN link/status protocol. It never
// sends start, stop, pause or speed commands. DFT200 compatibility is unverified.
func (Driver) NewTelemetry(options device.TelemetryOptions) (device.TelemetrySession, error) {
	if !options.ExperimentalVAN {
		return nil, device.ErrTelemetryDisabled
	}
	return &vanSession{
		unit: options.ExperimentalVANUnit,
		// These are conservative application defaults, not confirmed vendor
		// timing. Store freshness independently defaults to five seconds.
		pollInterval: time.Second, handshakeTimeout: 5 * time.Second,
		responseTimeout: 5 * time.Second,
	}, nil
}

type vanSession struct {
	unit             byte
	pollInterval     time.Duration
	handshakeTimeout time.Duration
	responseTimeout  time.Duration
}

func (*vanSession) NotifyUUIDs() []string { return []string{notifyUUID} }

// Run uses no transport goroutines. Parser, handshake and timers belong to this
// connection only. Cancellation is checked before admitting writes or updates;
// an already admitted synchronous Link.Write must finish before Run can return.
func (s *vanSession) Run(ctx context.Context, link device.Link, notifications <-chan device.Notification, emit func(observed.Update)) error {
	write := func(packet []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := link.Write(ctx, writeUUID, packet); err != nil {
			return fmt.Errorf("VAN %02x write: %w", packet[1], err)
		}
		return ctx.Err()
	}
	publish := func(update observed.Update) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		emit(update)
		return nil
	}
	if err := write(vanPacket(0xc0)); err != nil {
		return err
	}
	const (
		awaitD0 = iota
		awaitD1
		linked
	)
	stage := awaitD0
	timeout := time.NewTimer(s.handshakeTimeout)
	defer timeout.Stop()
	var poll <-chan time.Time
	var ticker *time.Ticker
	defer func() {
		if ticker != nil {
			ticker.Stop()
		}
	}()
	var stream vanStream
	accept := func(frame vanFrame) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch frame.command {
		case 0xdd:
			if len(frame.payload) >= 1 {
				return fmt.Errorf("VAN device error 0x%02x", frame.payload[0])
			}
		case 0xd0:
			if stage == awaitD0 {
				// Drill-enable is always zero. Unit is an opaque experimental
				// choice and may change device configuration, even for telemetry.
				if err := write(vanPacket(0xc1, 0, s.unit)); err != nil {
					return err
				}
				stage = awaitD1
				timeout.Reset(s.handshakeTimeout)
			}
		case 0xd1:
			// Static analysis reads at least seven D1 payload bytes. Further
			// version fields are unresolved; no claim is made from a short D1.
			if stage == awaitD1 && len(frame.payload) >= 7 {
				capabilities := vanObservedCapabilities()
				if err := publish(observed.Update{Capabilities: &capabilities}); err != nil {
					return err
				}
				stage = linked
				timeout.Reset(s.responseTimeout)
				if err := write(command(cmdStatus, 0)); err != nil {
					return err
				}
				ticker = time.NewTicker(s.pollInterval)
				poll = ticker.C
			}
		case 0xd3:
			if stage == linked {
				if update, ok := decodeVANStatus(frame.payload, s.unit); ok {
					if err := publish(update); err != nil {
						return err
					}
					timeout.Reset(s.responseTimeout)
				}
			}
		}
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout.C:
			switch stage {
			case awaitD0:
				return fmt.Errorf("VAN handshake timeout awaiting D0")
			case awaitD1:
				return fmt.Errorf("VAN handshake timeout awaiting D1")
			default:
				return fmt.Errorf("VAN status response timeout awaiting D3")
			}
		case <-poll:
			if err := write(command(cmdStatus, 0)); err != nil {
				return err
			}
		case notification, ok := <-notifications:
			if !ok {
				return nil
			}
			if !strings.EqualFold(notification.UUID, notifyUUID) {
				continue
			}
			if err := stream.feed(notification.Data, accept); err != nil {
				return err
			}
		}
	}
}
