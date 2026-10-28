//go:build linux

package ownership

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func runtimeDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}
func TestHelperProcess(t *testing.T) {
	dir := os.Getenv("TREAD_LOCK_HELPER")
	if dir == "" {
		return
	}
	l, err := Acquire(dir)
	if os.Getenv("TREAD_LOCK_EXPECT_CONFLICT") == "1" {
		if !errors.Is(err, ErrOwned) {
			t.Fatalf("want conflict, got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	l.Close()
}
func helper(t *testing.T, dir string, conflict bool) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(), "TREAD_LOCK_HELPER="+dir, "TREAD_LOCK_EXPECT_CONFLICT=0")
	if conflict {
		cmd.Env = append(cmd.Env, "TREAD_LOCK_EXPECT_CONFLICT=1")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v %s", err, out)
	}
}
func TestExclusionAndStableFile(t *testing.T) {
	dir := runtimeDir(t)
	l, err := Acquire(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, LockName)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	helper(t, dir, true)
	if _, err := Acquire(dir); !errors.Is(err, ErrOwned) {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	helper(t, dir, false)
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("lock file replaced", err)
	}
}
func TestUnsafePaths(t *testing.T) {
	for _, kind := range []string{"directory-mode", "directory-symlink", "file-symlink", "file-mode", "fifo", "directory-file", "hardlink", "missing"} {
		t.Run(kind, func(t *testing.T) {
			dir := runtimeDir(t)
			path := filepath.Join(dir, LockName)
			must := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "directory-mode":
				must(os.Chmod(dir, 0755))
			case "directory-symlink":
				link := filepath.Join(t.TempDir(), "link")
				must(os.Symlink(dir, link))
				dir = link
			case "file-symlink":
				must(os.Symlink(filepath.Join(dir, "target"), path))
			case "file-mode":
				must(os.WriteFile(path, nil, 0644))
			case "fifo":
				must(unix.Mkfifo(path, 0600))
			case "directory-file":
				must(os.Mkdir(path, 0600))
			case "hardlink":
				target := filepath.Join(dir, "target")
				must(os.WriteFile(target, nil, 0600))
				must(os.Link(target, path))
			case "missing":
				dir = filepath.Join(dir, "missing")
			}
			l, err := Acquire(dir)
			if err == nil {
				l.Close()
				t.Fatal("accepted unsafe path")
			}
		})
	}
}
func TestRuntimeDir(t *testing.T) {
	t.Setenv("TREAD_RUNTIME_DIR", "")
	t.Setenv("XDG_RUNTIME_DIR", "")
	if RuntimeDir() != DefaultRuntimeDir {
		t.Fatal(RuntimeDir())
	}
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	if RuntimeDir() != "/run/user/1000/tread" {
		t.Fatal(RuntimeDir())
	}
	t.Setenv("TREAD_RUNTIME_DIR", "/custom")
	if RuntimeDir() != "/custom" {
		t.Fatal(RuntimeDir())
	}
}
func TestAcquireCreatesOnlyXDGDefault(t *testing.T) {
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", base)
	if _, err := Acquire(filepath.Join(base, "other")); err == nil {
		t.Fatal("created non-default directory")
	}
	lock, err := Acquire(RuntimeDir())
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if st, err := os.Stat(RuntimeDir()); err != nil || st.Mode().Perm() != 0700 {
		t.Fatal(st, err)
	}
}
