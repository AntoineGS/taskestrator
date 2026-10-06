package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "none.toml"))
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if len(cfg.Roots) != 1 || cfg.Roots[0] != filepath.Join(home, "gits") {
		t.Fatalf("Roots = %v", cfg.Roots)
	}
	if cfg.RereadInterval != 10*time.Second {
		t.Fatalf("RereadInterval = %v", cfg.RereadInterval)
	}
}

func TestLoadParsesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	write(t, path, "roots = [\"~/src\", \"/opt/code\"]\nhidden = [\"se\"]\nreread_interval = \"3s\"\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if cfg.Roots[0] != filepath.Join(home, "src") || cfg.Roots[1] != "/opt/code" {
		t.Fatalf("Roots = %v", cfg.Roots)
	}
	if len(cfg.Hidden) != 1 || cfg.Hidden[0] != "se" {
		t.Fatalf("Hidden = %v", cfg.Hidden)
	}
	if cfg.RereadInterval != 3*time.Second {
		t.Fatalf("RereadInterval = %v", cfg.RereadInterval)
	}
}

func TestLoadRejectsBadInterval(t *testing.T) {
	for _, v := range []string{"soon", "0s", "-1s"} {
		path := filepath.Join(t.TempDir(), "config.toml")
		write(t, path, "reread_interval = \""+v+"\"\n")
		if _, err := Load(path); err == nil {
			t.Fatalf("interval %q: expected an error", v)
		}
	}
}

func TestLoadProjectFile(t *testing.T) {
	repo := t.TempDir()
	pf, err := LoadProjectFile(repo)
	if err != nil || pf != (ProjectFile{}) {
		t.Fatalf("missing file: %+v, %v", pf, err)
	}
	write(t, filepath.Join(repo, ".tsk.toml"), "setup = \"make deps\"\nprompt = \"Do {{.ID}}\"\n")
	pf, err = LoadProjectFile(repo)
	if err != nil {
		t.Fatal(err)
	}
	if pf.Setup != "make deps" || pf.Prompt != "Do {{.ID}}" {
		t.Fatalf("ProjectFile = %+v", pf)
	}
}

func TestDefaultPathUsesXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x")
	if got := DefaultPath(); got != "/x/tsk/config.toml" {
		t.Fatalf("DefaultPath = %q", got)
	}
}
