// Package goals loads optional calendar targets and reports progress using stats.
package goals

import (
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/xarcdotdev/oss-treadmill/internal/config"
	"gopkg.in/yaml.v3"
)

// Config has optional targets for each calendar period. Zero values mean omitted.
// Constructed configs must use positive, finite targets, as enforced by LoadFile.
type Config struct {
	Daily   Targets
	Weekly  Targets
	Monthly Targets
	Yearly  Targets
}

type Targets struct {
	Steps           int64
	Calories        float64
	Time            time.Duration
	AverageSpeedKMH float64
}

// Path returns goals.yaml beside the application config, or in config.Dir().
func Path(cfg config.Config) string {
	dir := config.Dir()
	if cfg.Path != "" {
		dir = filepath.Dir(cfg.Path)
	}
	return filepath.Join(dir, "goals.yaml")
}

// LoadFile accepts one strict YAML mapping. Missing or empty files mean no goals.
// Null values, aliases, unknown/duplicate fields and coercion from strings are rejected.
func LoadFile(path string) (Config, error) {
	c, err := loadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("goals %s: %w", path, err)
	}
	return c, nil
}

func loadFile(path string) (Config, error) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return Config{}, nil
	}
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	dec := yaml.NewDecoder(f)
	var doc yaml.Node
	if err := dec.Decode(&doc); err != nil {
		if errors.Is(err, io.EOF) {
			return Config{}, nil
		}
		return Config{}, err
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err != nil {
			return Config{}, err
		}
		return Config{}, errors.New("multiple YAML documents are not supported")
	}
	var c Config
	periods := map[string]*Targets{"daily": &c.Daily, "weekly": &c.Weekly, "monthly": &c.Monthly, "yearly": &c.Yearly}
	err = mapping(doc.Content[0], "goals", func(key string, n *yaml.Node) error {
		target, ok := periods[key]
		if !ok {
			return fmt.Errorf("unknown period %q", key)
		}
		return mapping(n, key, func(metric string, v *yaml.Node) error {
			field := key + "." + metric
			switch metric {
			case "steps":
				if v.Kind != yaml.ScalarNode || v.Tag != "!!int" {
					return fmt.Errorf("%s must be a positive integer", field)
				}
				if err := v.Decode(&target.Steps); err != nil {
					return fmt.Errorf("%s: %w", field, err)
				}
				if target.Steps <= 0 {
					return fmt.Errorf("%s must be positive", field)
				}
			case "calories", "average_speed_kmh":
				if v.Kind != yaml.ScalarNode || (v.Tag != "!!int" && v.Tag != "!!float") {
					return fmt.Errorf("%s must be a positive finite number", field)
				}
				var value float64
				if err := v.Decode(&value); err != nil {
					return fmt.Errorf("%s: %w", field, err)
				}
				if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
					return fmt.Errorf("%s must be a positive finite number", field)
				}
				if metric == "calories" {
					target.Calories = value
				} else {
					target.AverageSpeedKMH = value
				}
			case "time":
				if v.Kind != yaml.ScalarNode || v.Tag != "!!str" {
					return fmt.Errorf("%s must be a duration string (e.g. 30m)", field)
				}
				d, err := time.ParseDuration(v.Value)
				if err != nil {
					return fmt.Errorf("%s: %w", field, err)
				}
				if d <= 0 {
					return fmt.Errorf("%s must be positive", field)
				}
				target.Time = d
			default:
				return fmt.Errorf("unknown field %s", field)
			}
			return nil
		})
	})
	return c, err
}

// Validate the schema explicitly on nodes so YAML cannot silently coerce types
// (notably nulls, quoted numbers and fractional integers).
func mapping(n *yaml.Node, field string, visit func(string, *yaml.Node) error) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("%s must be a mapping (line %d)", field, n.Line)
	}
	seen := map[string]bool{}
	for i := 0; i < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		if k.Kind != yaml.ScalarNode || k.Tag != "!!str" {
			return fmt.Errorf("%s keys must be strings (line %d)", field, k.Line)
		}
		if seen[k.Value] {
			return fmt.Errorf("duplicate field %s.%s (line %d)", field, k.Value, k.Line)
		}
		seen[k.Value] = true
		if err := visit(k.Value, v); err != nil {
			return fmt.Errorf("line %d: %w", k.Line, err)
		}
	}
	return nil
}
