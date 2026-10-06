// Package config loads tsk's user configuration and per-repository files.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the user configuration from ~/.config/tsk/config.toml.
type Config struct {
	Roots          []string
	Hidden         []string
	RereadInterval time.Duration
}

type fileConfig struct {
	Roots          []string `toml:"roots"`
	Hidden         []string `toml:"hidden"`
	RereadInterval string   `toml:"reread_interval"`
}

// Default returns the configuration used when no file exists.
func Default() Config {
	return Config{Roots: []string{ExpandHome("~/gits")}, RereadInterval: 10 * time.Second}
}

// DefaultPath returns the user configuration file path.
func DefaultPath() string {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "tsk", "config.toml")
	}
	return ExpandHome("~/.config/tsk/config.toml")
}

// Load reads the configuration at path, falling back to defaults when it does not exist.
func Load(path string) (Config, error) {
	cfg := Default()
	var fc fileConfig
	_, err := toml.DecodeFile(path, &fc)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	if len(fc.Roots) > 0 {
		cfg.Roots = nil
		for _, r := range fc.Roots {
			cfg.Roots = append(cfg.Roots, ExpandHome(r))
		}
	}
	for _, h := range fc.Hidden {
		cfg.Hidden = append(cfg.Hidden, ExpandHome(h))
	}
	if fc.RereadInterval != "" {
		d, err := time.ParseDuration(fc.RereadInterval)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("%s: reread_interval %q is not a positive duration", path, fc.RereadInterval)
		}
		cfg.RereadInterval = d
	}
	return cfg, nil
}

// ProjectFile is a repository's optional .tsk.toml.
type ProjectFile struct {
	Setup  string `toml:"setup"`
	Prompt string `toml:"prompt"`
}

// LoadProjectFile reads repo/.tsk.toml, returning a zero value when it does not exist.
func LoadProjectFile(repo string) (ProjectFile, error) {
	var pf ProjectFile
	path := filepath.Join(repo, ".tsk.toml")
	_, err := toml.DecodeFile(path, &pf)
	if errors.Is(err, fs.ErrNotExist) {
		return ProjectFile{}, nil
	}
	if err != nil {
		return ProjectFile{}, fmt.Errorf("read %s: %w", path, err)
	}
	return pf, nil
}

// ExpandHome replaces a leading ~ with the user's home directory.
func ExpandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~"))
}
