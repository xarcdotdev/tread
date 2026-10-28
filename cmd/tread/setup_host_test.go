package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSetupHostArguments(t *testing.T) {
	for _, tc := range []struct {
		args []string
		name string
		bad  bool
	}{
		{nil, "tread.local", false},
		{[]string{"-name", "Walk.Local"}, "Walk.Local", false},
		{[]string{"extra"}, "", true},
		{[]string{"-name", "walk.local", "extra"}, "", true},
		{[]string{"-hosts", "/tmp/hosts"}, "", true},
		{[]string{"-name"}, "", true},
		{[]string{"-name", "bad name"}, "", true},
	} {
		var out bytes.Buffer
		called := false
		err := setupHostWith(tc.args, &out, func(name string) error {
			called = true
			if name != tc.name {
				t.Errorf("name = %q, want %q", name, tc.name)
			}
			return nil
		}, os.Executable)
		if (err != nil) != tc.bad || called == tc.bad {
			t.Fatalf("args %q: err %v, called %v", tc.args, err, called)
		}
		if !tc.bad && out.String() != tc.name+" maps to 127.0.0.1 in /etc/hosts\n" {
			t.Fatalf("output %q", out.String())
		}
	}
}

func TestSetupHostPermissionGuidance(t *testing.T) {
	var out bytes.Buffer
	exe := "/home/person's files/.local/bin/tread"
	err := setupHostWith([]string{"-name", "Walk.Local"}, &out, func(string) error {
		return &os.PathError{Op: "open", Path: "/etc/hosts", Err: os.ErrPermission}
	}, func() (string, error) { return exe, nil })
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("got %v", err)
	}
	want := "sudo -- '/home/person'\"'\"'s files/.local/bin/tread' setup-host -name 'Walk.Local'"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("guidance: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("unexpected success: %s", &out)
	}
}

func TestSetupHostEarlyDispatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.toml")
	if err := os.WriteFile(path, []byte("[broken"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TREAD_CONFIG", path)
	for _, args := range [][]string{
		{"setup-host", "-name", "walk.local"},
		{"-config", path, "setup-host", "-name", "walk.local"},
		{"-config", filepath.Join(t.TempDir(), "missing"), "setup-host", "-name", "walk.local"},
	} {
		called := false
		sentinel := errors.New("injected setup")
		err := runWithSetupHost(args, func(got []string) error {
			called = true
			if !reflect.DeepEqual(got, []string{"-name", "walk.local"}) {
				t.Fatalf("args = %q", got)
			}
			return sentinel
		})
		if !called || !errors.Is(err, sentinel) {
			t.Fatalf("dispatch called %v, err %v", called, err)
		}
	}
}
