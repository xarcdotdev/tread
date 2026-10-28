package hostsetup

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestValidateBeforeIO(t *testing.T) {
	invalid := []string{"", ".", "a..b", ".a", "a.", "-a", "a-", "a.-b", "a.b-", "a b", "a\tb", "a\nb", "a#b", "a:80", "127.0.0.1", "::1", "[::1]", "é.local", "a_b", strings.Repeat("a", 64), strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("a", 62)}
	for _, name := range invalid {
		t.Run(name, func(t *testing.T) {
			err := ensure(filepath.Join(t.TempDir(), "absent"), name)
			if err == nil || !strings.Contains(err.Error(), "invalid hostname") {
				t.Fatalf("expected validation before IO, got %v", err)
			}
		})
	}
	for _, name := range []string{"tread.local", "TREAD.Local", "a", "a-1.local", strings.Repeat("a", 63), strings.Repeat(strings.Repeat("a", 63)+".", 3) + strings.Repeat("a", 61)} {
		if err := ValidateName(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
}

func TestEnsure(t *testing.T) {
	for _, tc := range []struct {
		name, before, after string
		conflict            bool
	}{
		{"empty", "", "127.0.0.1\ttread.local\n", false},
		{"newline", "127.0.0.1 localhost\n", "127.0.0.1 localhost\n127.0.0.1\ttread.local\n", false},
		{"missing newline", "# preserved", "# preserved\n127.0.0.1\ttread.local\n", false},
		{"aliases case", "127.0.0.1 localhost TREAD.Local other # text", "", false},
		{"duplicates", "127.0.0.1 tread.local tread.local\n127.0.0.1 Tread.Local\n", "", false},
		{"comments", "# ::1 tread.local\n127.0.0.2 other # tread.local\n", "# ::1 tread.local\n127.0.0.2 other # tread.local\n127.0.0.1\ttread.local\n", false},
		{"conflict alias", "192.0.2.1 other TREAD.LOCAL\n", "", true},
		{"ipv6", "::1 tread.local\n", "", true},
		{"malformed address", "bad-address other tread.local\n", "", true},
		{"mixed", "127.0.0.1 tread.local\n::1 other TREAD.local\n", "", true},
		{"mixed reverse", "bad tread.local\n127.0.0.1 tread.local\n", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hosts")
			if err := os.WriteFile(path, []byte(tc.before), 0640); err != nil {
				t.Fatal(err)
			}
			stat, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.after
			if want == "" {
				want = tc.before
			}
			for range 2 {
				err := ensure(path, "tread.local")
				if (err != nil) != tc.conflict {
					t.Fatalf("error = %v, conflict = %v", err, tc.conflict)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if string(data) != want {
					t.Fatalf("got %q, want %q", data, want)
				}
				after, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				if after.Mode() != stat.Mode() || !os.SameFile(stat, after) {
					t.Fatal("mode or inode changed")
				}
			}
		})
	}
}

func TestNonexistentNotCreated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := ensure(path, "tread.local"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("got %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file created: %v", err)
	}
}

func TestConcurrentEnsure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte("# existing"), 0600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := ensure(path, "tread.local"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "# existing\n127.0.0.1\ttread.local\n" {
		t.Fatalf("got %q", data)
	}
}

type failingWriter struct {
	writeErr, syncErr error
	short, synced     bool
}

func (f *failingWriter) Write(p []byte) (int, error) {
	if f.writeErr != nil {
		return 0, f.writeErr
	}
	if f.short {
		return len(p) - 1, nil
	}
	return len(p), nil
}
func (f *failingWriter) Sync() error { f.synced = true; return f.syncErr }

func TestAppendErrors(t *testing.T) {
	failure := errors.New("injected")
	for _, tc := range []struct {
		f    failingWriter
		want error
		sync bool
	}{
		{failingWriter{writeErr: failure}, failure, false},
		{failingWriter{short: true}, io.ErrShortWrite, false},
		{failingWriter{syncErr: failure}, failure, true},
	} {
		if err := appendEntry(&tc.f, "entry\n"); !errors.Is(err, tc.want) {
			t.Fatalf("got %v", err)
		}
		if tc.f.synced != tc.sync {
			t.Fatalf("Sync called = %v", tc.f.synced)
		}
	}
}
