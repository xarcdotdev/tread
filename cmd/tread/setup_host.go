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
			path, pathErr := executable()
			if pathErr == nil && filepath.IsAbs(path) {
				return fmt.Errorf("setup-host: %w; rerun explicitly with: sudo -- %s setup-host -name %s", err, shellQuote(path), shellQuote(*name))
			}
			return fmt.Errorf("setup-host: %w; cannot determine absolute executable path; rerun with sudo using the absolute path to tread and -name %s", err, shellQuote(*name))
		}
		return fmt.Errorf("setup-host: %w", err)
	}
	_, err := fmt.Fprintf(out, "%s maps to 127.0.0.1 in /etc/hosts\n", *name)
	return err
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
