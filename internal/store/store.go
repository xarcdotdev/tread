// Package store persists events in SQLite.
package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/event"
	_ "modernc.org/sqlite"
)

// migrations run in order; append only, never edit.
var migrations = []string{
	`CREATE TABLE events (
		id     TEXT PRIMARY KEY,
		device TEXT NOT NULL,
		action TEXT NOT NULL,
		value  INTEGER NOT NULL DEFAULT 0,
		ts     INTEGER NOT NULL -- unix milliseconds
	);
	CREATE INDEX events_ts ON events(ts);`,
}

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	for i := version; i < len(migrations); i++ {
		if _, err := s.db.Exec(migrations[i]); err != nil {
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, i+1)); err != nil {
			return err
		}
	}
	return nil
}

// AddEvents inserts events; already known IDs are ignored.
func (s *Store) AddEvents(events []event.Event) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range events {
		_, err := tx.Exec(`INSERT OR IGNORE INTO events (id, device, action, value, ts) VALUES (?, ?, ?, ?, ?)`,
			e.ID, e.Device, string(e.Action), e.Value, e.TS.UnixMilli())
		if err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Events returns events in [from, to), ordered by time.
func (s *Store) Events(from, to time.Time) ([]event.Event, error) {
	rows, err := s.db.Query(`SELECT id, device, action, value, ts FROM events WHERE ts >= ? AND ts < ? ORDER BY ts, rowid`,
		from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []event.Event
	for rows.Next() {
		var e event.Event
		var ts int64
		if err := rows.Scan(&e.ID, &e.Device, &e.Action, &e.Value, &ts); err != nil {
			return nil, err
		}
		e.TS = time.UnixMilli(ts).UTC()
		events = append(events, e)
	}
	return events, rows.Err()
}
