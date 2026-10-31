package store

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/event"
)

func TestEventStorageKeepsLegacySchema(t *testing.T) {
	for _, name := range []string{"fresh", "legacy"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tread.db")
			ts := time.Now().UTC().Truncate(time.Millisecond)
			legacy := event.Event{ID: "legacy", Device: "desk", Action: event.Speed, Value: 8, TS: ts}
			if name == "legacy" {
				// Independently reproduce the original schema and a stored speed event.
				db, err := sql.Open("sqlite", path)
				if err != nil {
					t.Fatal(err)
				}
				_, err = db.Exec(`CREATE TABLE events (
					id TEXT PRIMARY KEY, device TEXT NOT NULL, action TEXT NOT NULL,
					value INTEGER NOT NULL DEFAULT 0, ts INTEGER NOT NULL
				);
				CREATE INDEX events_ts ON events(ts);
				PRAGMA user_version = 1;
				INSERT INTO events (id, device, action, value, ts) VALUES ('legacy', 'desk', 'speed', 8, ?)`, ts.UnixMilli())
				closeErr := db.Close()
				if err != nil {
					t.Fatal(err)
				}
				if closeErr != nil {
					t.Fatal(closeErr)
				}
			}

			s, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			var version, columns int
			if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
				t.Fatal(err)
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('events')`).Scan(&columns); err != nil {
				t.Fatal(err)
			}
			if version != 1 || columns != 5 {
				t.Fatalf("schema changed: version=%d columns=%d", version, columns)
			}

			higher := event.Event{ID: "higher", Device: "desk", Action: event.Speed, Value: 12, TS: ts}
			start := event.Event{ID: "start", Device: "desk", Action: event.Start, TS: ts}
			want := []event.Event{legacy, higher, start}
			// The existing event is ignored on legacy databases; repeated IDs stay idempotent.
			for range 2 {
				if err := s.AddEvents(want); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.Events(ts, ts.Add(time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("events = %+v, want %+v", got, want)
			}
			for _, e := range got {
				if err := e.Validate(time.Now()); err != nil {
					t.Errorf("stored %s no longer validates: %v", e.ID, err)
				}
			}
		})
	}
}
