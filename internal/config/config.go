// Package config loads tsk's per-repository files.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

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
