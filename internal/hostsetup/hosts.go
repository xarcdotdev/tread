// Package hostsetup provides explicitly requested local hostname setup.
package hostsetup

import (
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
)

// ValidateName accepts ASCII DNS names without a trailing dot or IP literals.
func ValidateName(name string) error {
	if len(name) == 0 || len(name) > 253 || net.ParseIP(name) != nil {
		return fmt.Errorf("invalid hostname %q", name)
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("invalid hostname %q", name)
		}
		for _, c := range []byte(label) {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return fmt.Errorf("invalid hostname %q", name)
			}
		}
	}
	return nil
}

// Ensure maps name to 127.0.0.1 in the existing /etc/hosts file.
// Its advisory lock coordinates only cooperating writers. Appends are not
// crash-atomic; this function neither replaces the file nor elevates privileges.
func Ensure(name string) error { return ensure("/etc/hosts", name) }

func ensure(path, name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock hosts: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	data, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("read hosts: %w", err)
	}
	found := false
	for i, line := range strings.Split(string(data), "\n") {
		line, _, _ = strings.Cut(line, "#")
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		for _, alias := range fields[1:] {
			if strings.EqualFold(alias, name) {
				if fields[0] != "127.0.0.1" {
					return fmt.Errorf("hostname %q conflicts with address %q on hosts line %d", name, fields[0], i+1)
				}
				found = true
			}
		}
	}
	if found {
		return nil
	}
	entry := "127.0.0.1\t" + name + "\n"
	if len(data) > 0 && data[len(data)-1] != '\n' {
		entry = "\n" + entry
	}
	return appendEntry(f, entry)
}

type syncedWriter interface {
	io.Writer
	Sync() error
}

func appendEntry(f syncedWriter, entry string) error {
	n, err := f.Write([]byte(entry))
	if err != nil {
		return fmt.Errorf("append hosts: %w", err)
	}
	if n != len(entry) {
		return fmt.Errorf("append hosts: %w", io.ErrShortWrite)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync hosts: %w", err)
	}
	return nil
}
