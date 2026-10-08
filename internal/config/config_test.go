package config

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
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

func TestExpandHome(t *testing.T) {
	home, _ := os.UserHomeDir()
	if got := ExpandHome("~/gits"); got != filepath.Join(home, "gits") {
		t.Fatalf("ExpandHome(~/gits) = %q", got)
	}
	if got := ExpandHome("/abs/~x"); got != "/abs/~x" {
		t.Fatalf("ExpandHome(/abs/~x) = %q", got)
	}
}
