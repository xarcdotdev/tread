// Package config loads file-backed application settings.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/BurntSushi/toml"
)

type Personal struct {
	WeightKG    float64 `toml:"weight_kg" json:"weight_kg"`
	Age         int     `toml:"age" json:"age"`
	HeightCM    float64 `toml:"height_cm" json:"height_cm"`
	Gender      string  `toml:"gender" json:"gender"`
	StepLengthM float64 `toml:"step_length_m" json:"step_length_m"`
}
type Treadmill struct {
	Driver       string             `toml:"driver" json:"driver"`
	MAC          string             `toml:"mac" json:"mac"`
	Name         string             `toml:"name" json:"name"`
	InitialLevel int                `toml:"initial_level" json:"initial_level"`
	SpeedKMH     map[string]float64 `toml:"speed_kmh" json:"speed_kmh"`
	Calibrated   bool               `toml:"calibrated" json:"calibrated"`
}
type Config struct {
	MAC       string    `toml:"mac" json:"-"` // legacy config
	ServerURL string    `toml:"server_url" json:"server_url"`
	Device    string    `toml:"device" json:"device"`
	Listen    string    `toml:"listen" json:"listen"`
	DBPath    string    `toml:"db_path" json:"db_path"`
	Timezone  string    `toml:"timezone" json:"timezone"`
	Personal  Personal  `toml:"personal" json:"personal"`
	Treadmill Treadmill `toml:"treadmill" json:"treadmill"`
	Path      string    `toml:"-" json:"path"`
}

func Dir() string {
	d, err := os.UserConfigDir()
	if err != nil {
		d = "."
	}
	return filepath.Join(d, "tread")
}
func Default() Config {
	return Config{Device: "dft200", Listen: "127.0.0.1:8080", ServerURL: "http://127.0.0.1:8080", DBPath: filepath.Join(Dir(), "tread.db"), Timezone: "UTC", Treadmill: Treadmill{Driver: "dft200", Name: "Sportstech DFT200", InitialLevel: 1, SpeedKMH: map[string]float64{}}}
}
func Load() (Config, error) {
	path := os.Getenv("TREAD_CONFIG")
	if path == "" {
		path = filepath.Join(Dir(), "config.toml")
	}
	return LoadFile(path)
}
func LoadFile(path string) (Config, error) {
	c := Default()
	c.Path = path
	_, err := toml.DecodeFile(path, &c)
	if errors.Is(err, fs.ErrNotExist) {
		err = nil
	}
	if err != nil {
		return c, err
	}
	if c.Treadmill.MAC == "" {
		c.Treadmill.MAC = c.MAC
	}
	return c, c.Validate()
}
func (c Config) Validate() error {
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return fmt.Errorf("timezone: %w", err)
	}
	if c.Treadmill.Driver != "dft200" {
		return fmt.Errorf("unknown treadmill driver %q", c.Treadmill.Driver)
	}
	if c.Device == "" {
		return errors.New("device must be a stable treadmill identifier")
	}
	if c.Treadmill.InitialLevel < 1 || c.Treadmill.InitialLevel > 8 {
		return errors.New("initial_level must be 1–8")
	}
	for k, v := range c.Treadmill.SpeedKMH {
		n, e := strconv.Atoi(k)
		if e != nil || n < 1 || n > 8 || v <= 0 || v > 20 || math.IsNaN(v) || math.IsInf(v, 0) {
			return fmt.Errorf("invalid speed_kmh entry %q", k)
		}
	}
	if c.Treadmill.Calibrated && len(c.Treadmill.SpeedKMH) != 8 {
		return errors.New("calibrated requires speed_kmh entries for all 8 levels")
	}
	p := c.Personal
	if p.WeightKG < 0 || p.WeightKG > 500 || p.Age < 0 || p.Age > 120 || p.HeightCM < 0 || p.HeightCM > 250 || p.StepLengthM < 0 || p.StepLengthM > 2 {
		return errors.New("personal metrics out of range")
	}
	for _, v := range []float64{p.WeightKG, p.HeightCM, p.StepLengthM} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("personal metrics must be finite")
		}
	}
	if c.ServerURL != "" {
		u, e := url.Parse(c.ServerURL)
		if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return errors.New("server_url must be an http(s) URL")
		}
	}
	return nil
}
func (c Config) Location() *time.Location { l, _ := time.LoadLocation(c.Timezone); return l }
func (c Config) QueuePath() string        { return filepath.Join(filepath.Dir(c.Path), "queue.jsonl") }
