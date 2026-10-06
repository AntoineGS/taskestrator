package discover

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMeta(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".beads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".beads", "metadata.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func project(t *testing.T, dir, db string) {
	t.Helper()
	writeMeta(t, dir, `{"dolt_database":"`+db+`"}`)
}

func real(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestScanFindsProjectsAndSkipsNoise(t *testing.T) {
	root := t.TempDir()
	project(t, filepath.Join(root, "alpha"), "al")
	project(t, filepath.Join(root, "group", "beta"), "be")
	project(t, filepath.Join(root, "alpha", ".worktrees", "al-1"), "al")
	project(t, filepath.Join(root, "node_modules", "pkg"), "nm")
	project(t, filepath.Join(root, "a", "b", "c", "d"), "deep4")
	project(t, filepath.Join(root, "a", "b", "c", "d", "e"), "deep5")

	got, warns := Scan([]string{root}, nil)
	if len(warns) != 0 {
		t.Fatalf("warnings: %v", warns)
	}
	var names []string
	for _, p := range got {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "al,be,deep4" {
		t.Fatalf("names = %v", names)
	}
	if got[0].Repo != real(t, filepath.Join(root, "alpha")) {
		t.Fatalf("Repo = %q", got[0].Repo)
	}
}

func TestScanHidden(t *testing.T) {
	root := t.TempDir()
	project(t, filepath.Join(root, "alpha"), "al")
	project(t, filepath.Join(root, "beta"), "be")
	got, _ := Scan([]string{root}, []string{"al"})
	if len(got) != 1 || got[0].Name != "be" {
		t.Fatalf("got %+v", got)
	}
}

func TestScanBadMetadataWarnsAndContinues(t *testing.T) {
	root := t.TempDir()
	writeMeta(t, filepath.Join(root, "broken"), "{not json")
	project(t, filepath.Join(root, "good"), "gd")
	got, warns := Scan([]string{root}, nil)
	if len(got) != 1 || got[0].Name != "gd" {
		t.Fatalf("got %+v", got)
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "broken") {
		t.Fatalf("warnings = %v", warns)
	}
}

func TestScanMissingRootWarns(t *testing.T) {
	got, warns := Scan([]string{filepath.Join(t.TempDir(), "nope")}, nil)
	if len(got) != 0 || len(warns) != 1 {
		t.Fatalf("got %+v, warnings %v", got, warns)
	}
}

func TestForDir(t *testing.T) {
	projects := []Project{{Name: "outer", Repo: "/src/multi"}, {Name: "inner", Repo: "/src/multi/inner"}}
	cases := map[string]string{
		"/src/multi":                     "outer",
		"/src/multi/inner/.worktrees/x1": "inner",
		"/src/multi/other":               "outer",
		"/src/multiverse":                "",
	}
	for dir, want := range cases {
		p, ok := ForDir(projects, dir)
		if want == "" {
			if ok {
				t.Errorf("%s: matched %s", dir, p.Name)
			}
			continue
		}
		if !ok || p.Name != want {
			t.Errorf("%s: got %q, want %q", dir, p.Name, want)
		}
	}
}

func TestScanHiddenSymlinkedPath(t *testing.T) {
	root := t.TempDir()
	project(t, filepath.Join(root, "alpha"), "al")
	project(t, filepath.Join(root, "beta"), "be")
	link := filepath.Join(t.TempDir(), "alpha-link")
	if err := os.Symlink(filepath.Join(root, "alpha"), link); err != nil {
		t.Fatal(err)
	}
	got, _ := Scan([]string{root}, []string{link})
	if len(got) != 1 || got[0].Name != "be" {
		t.Fatalf("got %+v", got)
	}
}

func TestScanSymlinkedRoot(t *testing.T) {
	target := t.TempDir()
	project(t, filepath.Join(target, "alpha"), "al")
	link := filepath.Join(t.TempDir(), "gits")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	got, warns := Scan([]string{link}, nil)
	if len(warns) != 0 || len(got) != 1 || got[0].Name != "al" {
		t.Fatalf("got %+v, warns %v", got, warns)
	}
}
