package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"syscall"

	"github.com/xarcdotdev/oss-treadmill/internal/agent"
	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"github.com/xarcdotdev/oss-treadmill/internal/control"
	"github.com/xarcdotdev/oss-treadmill/internal/observed"
	"github.com/xarcdotdev/oss-treadmill/internal/ownership"
)

func commandRequest(action string, args []string) (agent.CommandRequest, error) {
	req := agent.CommandRequest{Action: action}
	if action == "speed" {
		if len(args) != 1 {
			return req, errors.New("usage: tread [-direct] speed <level>")
		}
		level, err := strconv.Atoi(args[0])
		if err != nil {
			return req, errors.New("speed requires an integer level")
		}
		req.Level = &level
	} else if len(args) != 0 {
		return req, fmt.Errorf("%s takes no arguments", action)
	}
	return req, nil
}

func agentControl(req agent.CommandRequest) error {
	out, err := agent.NewClient(ownership.RuntimeDir()).Execute(context.Background(), req)
	if err != nil {
		// Delivery warnings do not turn into a second motor command.
		if out.DeliveryPending {
			log.Printf("command sent; usage delivery pending: %s", out.DeliveryMessage)
		}
		return err
	}
	if out.OutcomeUnknown {
		return errors.New("command outcome unknown; do not retry")
	}
	reportControl(control.ServiceResult{Result: out.Result, DeliveryPending: out.DeliveryPending, DeliveryMessage: out.DeliveryMessage})
	return nil
}

// agentSnapshot lets serve show live readings without owning Bluetooth.
func agentSnapshot(ctx context.Context) (observed.Snapshot, error) {
	return agent.NewClient(ownership.RuntimeDir()).Snapshot(ctx)
}

func agentSync() error {
	return agent.NewClient(ownership.RuntimeDir()).Sync(context.Background())
}

func reportControl(out control.ServiceResult) {
	fmt.Println(out.Result.Message)
	if out.DeliveryPending {
		log.Printf("command sent; usage delivery pending: %s", out.DeliveryMessage)
	} else if out.Result.Motion {
		fmt.Println("Usage delivered.")
	}
}

// Keep ownership until the synchronous transport and its cleanup return. There
// is deliberately no deadline goroutine that could release an unresolved send.
func withOwnership(fn func() error) error {
	lock, err := ownership.Acquire(ownership.RuntimeDir())
	if err != nil {
		return err
	}
	defer lock.Close()
	return fn()
}

func runAgent(cfg config.Config) error {
	return runAgentWith(cfg, signal.NotifyContext, agent.Run)
}

func runAgentWith(cfg config.Config,
	notify func(context.Context, ...os.Signal) (context.Context, context.CancelFunc),
	run func(context.Context, config.Config, agent.Options) error,
) error {
	ctx, stop := notify(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, cfg, agent.Options{RuntimeDir: ownership.RuntimeDir()})
}

func showObserved(args []string) error {
	fs := flag.NewFlagSet("observed", flag.ContinueOnError)
	raw := fs.Bool("json", false, "print observed snapshot JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: tread observed [-json]")
	}
	snapshot, err := agent.NewClient(ownership.RuntimeDir()).Snapshot(context.Background())
	if err != nil {
		return err
	}
	return printObserved(os.Stdout, snapshot, *raw)
}

func printObserved(w io.Writer, snapshot observed.Snapshot, raw bool) error {
	if raw {
		return json.NewEncoder(w).Encode(snapshot)
	}
	fmt.Fprintf(w, "Device: %s\nConnection: %s\nTelemetry: %s\n", snapshot.Device, snapshot.Connection, snapshot.Telemetry)
	unavailable := func(field observed.Field) string {
		if snapshot.Connection == observed.ConnectionDisabled || snapshot.Telemetry == observed.TelemetryDisabled {
			return "disabled"
		}
		if snapshot.Telemetry == observed.TelemetryUnsupported || !slices.Contains(snapshot.Capabilities.ObservedFields, field) {
			return "unsupported"
		}
		return "unknown"
	}
	state := string(snapshot.State.Value)
	if state == "" || state == string(observed.StateUnknown) {
		state = unavailable(observed.FieldState)
	}
	if snapshot.State.RawCode != nil {
		state += fmt.Sprintf(" (raw 0x%02x)", *snapshot.State.RawCode)
	}
	if snapshot.State.Stale {
		state += " (stale)"
	}
	fmt.Fprintf(w, "State: %s\n", state)
	for _, field := range observed.MeasurementFields {
		m := snapshot.Measurements[field]
		value := unavailable(field)
		if m.Value != nil {
			value = strconv.FormatFloat(*m.Value, 'f', -1, 64)
			if m.Unit == "" || m.Unit == observed.UnitUnknown || m.UnitConfidence == "" || m.UnitConfidence == observed.UnitUnknownConfidence {
				value += " (units unknown)"
			} else {
				value += fmt.Sprintf(" %s (%s units)", m.Unit, m.UnitConfidence)
			}
			if m.DataConfidence == observed.DataExperimental {
				value += " (experimental)"
			}
		}
		if m.Stale {
			value += " (stale)"
		}
		fmt.Fprintf(w, "%s: %s\n", field, value)
	}
	return nil
}
