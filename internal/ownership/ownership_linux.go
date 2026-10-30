//go:build linux

// Package ownership coordinates the single Bluetooth owner on a Linux host.
// All agent and direct writers must use the same directory. Changing directories
// bypasses coordination. This lock is separate from config and queue locks.
package ownership

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/unix"
)

const DefaultRuntimeDir = "/run/tread"
const LockName = "owner.lock"
const SocketName = "agent.sock"

var ErrOwned = errors.New("Bluetooth host already owned")

// RuntimeDir returns TREAD_RUNTIME_DIR, else $XDG_RUNTIME_DIR/tread, else
// /run/tread. Every writer must resolve the same directory. Only the XDG default
// is created on demand (see Acquire); other directories must be provisioned.
func RuntimeDir() string {
	if dir := os.Getenv("TREAD_RUNTIME_DIR"); dir != "" {
		return dir
	}
	if dir := xdgRuntimeDir(); dir != "" {
		return dir
	}
	return DefaultRuntimeDir
}

func xdgRuntimeDir() string {
	if base := os.Getenv("XDG_RUNTIME_DIR"); filepath.IsAbs(base) {
		return filepath.Join(base, "tread")
	}
	return ""
}

type Lock struct {
	mu     sync.Mutex
	fd     int
	closed bool
	err    error
}

// Acquire takes a nonblocking flock on a stable 0600 owner.lock. Call this before
// any adapter access, and hold it until all BLE work and cleanup have finished.
// dir must be absolute, service-account-owned, and exactly 0700. Every path
// component must be a real directory, not a symlink. Never unlink owner.lock.
func Acquire(dir string) (*Lock, error) {
	fail := func(err error) (*Lock, error) {
		return nil, fmt.Errorf("ownership %s: %w; provision a real directory with mkdir, chown to the service account, and chmod 0700", dir, err)
	}
	if !filepath.IsAbs(dir) {
		return fail(errors.New("runtime directory must be absolute"))
	}
	// The per-user XDG runtime directory is private, so creating our child is safe.
	// The checks below still verify ownership and mode of whatever exists.
	if dir == xdgRuntimeDir() {
		if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return fail(err)
		}
	}
	// Walk via directory descriptors so symlinks in ancestors are rejected too.
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return fail(err)
	}
	for part := range strings.SplitSeq(filepath.Clean(dir), "/") {
		if part == "" {
			continue
		}
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return fail(e)
		}
		fd = next
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return fail(err)
	}
	if st.Mode&07777 != 0700 || st.Uid != uint32(os.Geteuid()) {
		return fail(errors.New("runtime directory must be owned by current service account with mode 0700"))
	}
	lockfd, err := unix.Openat(fd, LockName, unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0600)
	if err != nil {
		return fail(err)
	}
	bad := func(err error) (*Lock, error) { unix.Close(lockfd); return fail(err) }
	if err := unix.Fstat(lockfd, &st); err != nil {
		return bad(err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&07777 != 0600 || st.Uid != uint32(os.Geteuid()) || st.Nlink != 1 {
		return bad(errors.New("owner.lock must be a single-link regular file owned by current service account with mode 0600"))
	}
	if err := unix.Flock(lockfd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		unix.Close(lockfd)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrOwned
		}
		return fail(err)
	}
	return &Lock{fd: lockfd}, nil
}

// Close releases ownership once. The stable lock file remains on disk.
func (l *Lock) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.closed {
		l.closed = true
		l.err = unix.Close(l.fd)
	}
	return l.err
}
