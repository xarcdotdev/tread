package agent

import (
	"context"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
)

func TestMalformedResponseIsUnknownWithoutRetry(t *testing.T) {
	for _, body := range []string{`{}`, `null`, `{"result":{},"delivery_pending":false,"error":"","outcome_unknown":false} {}`} {
		t.Run(body, func(t *testing.T) {
			dir := t.TempDir()
			os.Chmod(dir, 0700)
			listener, cleanup, err := listen(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			var calls atomic.Int32
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.Write([]byte(body)) })}
			done := make(chan struct{})
			go func() { defer close(done); server.Serve(listener) }()
			defer func() { server.Close(); <-done }()
			out, err := NewClient(dir).Execute(context.Background(), CommandRequest{Action: "start"})
			if err == nil || !out.OutcomeUnknown || calls.Load() != 1 {
				t.Fatalf("response=%+v err=%v calls=%d", out, err, calls.Load())
			}
		})
	}
}
