package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/xarcdotdev/oss-treadmill/internal/hostsetup"
)

func setupHost(args []string) error {
	return setupHostWith(args, os.Stdout, hostsetup.Ensure, os.Executable)
}

func setupHostWith(args []string, out io.Writer, ensure func(string) error, executable func() (string, error)) error {
	fs := flag.NewFlagSet("setup-host", flag.ContinueOnError)
	name := fs.String("name", "tread.local", "hostname to map to 127.0.0.1")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: tread setup-host [-name tread.local]")
	}
	if err := hostsetup.ValidateName(*name); err != nil {
		return err
	}
	if err := ensure(*name); err != nil {
		if errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("setup-host: %w; %s", err, sudoHint(executable, *name))
		}
		return fmt.Errorf("setup-host: %w", err)
	}
	_, err := fmt.Fprintf(out, "%s maps to 127.0.0.1 in /etc/hosts\n", *name)
	return err
}

// sudoHint tells the user how to rerun setup-host with privileges.
func sudoHint(executable func() (string, error), name string) string {
	path, err := executable()
	if err != nil || !filepath.IsAbs(path) {
		return "cannot determine absolute executable path; rerun with sudo using the absolute path to tread and -name " + shellQuote(name)
	}
	return fmt.Sprintf("rerun explicitly with: sudo -- %s setup-host -name %s", shellQuote(path), shellQuote(name))
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
