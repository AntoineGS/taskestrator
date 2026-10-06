package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// FromService connects to the OpenCode background service shared by every
// OpenCode window on this machine.
func FromService(ctx context.Context) (*Client, error) {
	out, err := exec.CommandContext(ctx, "opencode", "service", "status").Output()
	if err != nil {
		return nil, fmt.Errorf("opencode service status: %w (start it with: opencode service start)", err)
	}
	base := parseServiceURL(string(out))
	if base == "" {
		return nil, errors.New("the OpenCode service is not running (start it with: opencode service start)")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, err
	}
	pw, err := readPassword(filepath.Join(dir, "opencode", "service.json"))
	if err != nil {
		return nil, err
	}
	return &Client{BaseURL: base, Password: pw}, nil
}

func parseServiceURL(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "http://") || strings.HasPrefix(line, "https://") {
			return strings.TrimRight(line, "/")
		}
	}
	return ""
}

func readPassword(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read OpenCode service config: %w", err)
	}
	var cfg struct {
		Password string `json:"password"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", fmt.Errorf("parse %s: %v", path, err)
	}
	return cfg.Password, nil
}
