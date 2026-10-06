// Package discover finds Beads projects under the configured roots.
package discover

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Project is a repository with a Beads database.
type Project struct {
	Name string // Beads database name, e.g. "se"
	Repo string // absolute repository path, symlinks resolved
}

const maxDepth = 4

var skipDirs = map[string]bool{"node_modules": true, "vendor": true}

// Scan walks each root up to four levels deep and returns the projects found,
// sorted by name, plus warnings for anything it could not read.
func Scan(roots, hidden []string) ([]Project, []string) {
	hide := map[string]bool{}
	for _, h := range hidden {
		hide[h] = true
	}
	seen := map[string]bool{}
	var out []Project
	var warns []string
	for _, root := range roots {
		root = filepath.Clean(root)
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if path == root {
					return err
				}
				warns = append(warns, err.Error())
				return nil
			}
			if !d.IsDir() {
				return nil
			}
			if path != root {
				name := d.Name()
				if skipDirs[name] || strings.HasPrefix(name, ".") || depth(root, path) > maxDepth {
					return fs.SkipDir
				}
			}
			p, ok, err := readProject(path)
			if err != nil {
				warns = append(warns, err.Error())
				return nil
			}
			if ok && !hide[p.Name] && !hide[p.Repo] && !seen[p.Repo] {
				seen[p.Repo] = true
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			warns = append(warns, fmt.Sprintf("scan %s: %v", root, err))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, warns
}

func depth(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return maxDepth + 1
	}
	return len(strings.Split(rel, string(filepath.Separator)))
}

func readProject(dir string) (Project, bool, error) {
	path := filepath.Join(dir, ".beads", "metadata.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Project{}, false, nil
	}
	if err != nil {
		return Project{}, false, err
	}
	var meta struct {
		Database string `json:"dolt_database"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return Project{}, false, fmt.Errorf("%s: %v", path, err)
	}
	if meta.Database == "" {
		return Project{}, false, nil
	}
	return Project{Name: meta.Database, Repo: resolve(dir)}, true, nil
}

func resolve(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// ForDir returns the project whose repository contains dir, preferring the deepest match.
func ForDir(projects []Project, dir string) (Project, bool) {
	dir = resolve(dir)
	var best Project
	found := false
	for _, p := range projects {
		if dir == p.Repo || strings.HasPrefix(dir, p.Repo+string(filepath.Separator)) {
			if !found || len(p.Repo) > len(best.Repo) {
				best, found = p, true
			}
		}
	}
	return best, found
}
