// Package config loads ~/.config/tread/config.toml.
package config

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	MAC       string `toml:"mac"`        // treadmill address
	ServerURL string `toml:"server_url"` // e.g. https://home.example.ts.net
	Device    string `toml:"device"`     // name of this controller in events
	Listen    string `toml:"listen"`     // tread serve address
	DBPath    string `toml:"db_path"`
}

func Dir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "tread")
}

// Load reads the config file. A missing file is not an error.
func Load() (Config, error) {
	host, _ := os.Hostname()
	c := Config{
		Device: host,
		Listen: ":8080",
		DBPath: filepath.Join(Dir(), "tread.db"),
	}
	_, err := toml.DecodeFile(filepath.Join(Dir(), "config.toml"), &c)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	return c, err
}
