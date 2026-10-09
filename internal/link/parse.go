// Package link links OpenCode sessions to the Beads tasks their agents claim.
package link

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Claim is a bd update that claims tasks, with the directory it ran in.
type Claim struct {
	Dir string
	IDs []string
}

var (
	idPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_]*-[A-Za-z0-9._]+$`)
	assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	// bd global flags that take a value, other than -C/--directory.
	valueFlags = map[string]bool{"--actor": true, "--dolt-auto-commit": true, "--mem-profile": true}
	// bd global flags that point bd at another database.
	otherDB = map[string]bool{"--db": true, "--database": true, "--global": true}
)

// ParseClaims finds the bd update commands in command that claim tasks.
// dir is the directory the command starts in. Each (directory, ID) appears
// once.
func ParseClaims(dir, command string) []Claim {
	var claims []Claim
	seen := map[string]bool{}
	for _, words := range segments(command) {
		if words[0] == "cd" {
			if len(words) > 1 {
				dir = resolve(dir, words[1])
			}
			continue
		}
		c, ok := claimOf(dir, words)
		if !ok {
			continue
		}
		var ids []string
		for _, id := range c.IDs {
			if k := c.Dir + "\x00" + id; !seen[k] {
				seen[k] = true
				ids = append(ids, id)
			}
		}
		if len(ids) > 0 {
			claims = append(claims, Claim{Dir: c.Dir, IDs: ids})
		}
	}
	return claims
}

// resolve returns p relative to dir, expanding a leading ~.
func resolve(dir, p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(dir, p)
}

// segments splits command into simple commands on ;, &, |, and newlines
// outside quotes, and each command into words with quotes and backslash
// escapes removed. Empty commands are dropped.
func segments(command string) [][]string {
	var segs [][]string
	var words []string
	var cur strings.Builder
	inWord := false
	endWord := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	endSegment := func() {
		endWord()
		if len(words) > 0 {
			segs = append(segs, words)
			words = nil
		}
	}
	rs := []rune(command)
	for i := 0; i < len(rs); i++ {
		switch c := rs[i]; {
		case c == '\'':
			inWord = true
			for i++; i < len(rs) && rs[i] != '\''; i++ {
				cur.WriteRune(rs[i])
			}
		case c == '"':
			inWord = true
			for i++; i < len(rs) && rs[i] != '"'; i++ {
				if rs[i] == '\\' && i+1 < len(rs) && strings.ContainsRune("\"\\$`", rs[i+1]) {
					i++
				}
				cur.WriteRune(rs[i])
			}
		case c == '\\':
			if i+1 < len(rs) {
				i++
				if rs[i] != '\n' {
					cur.WriteRune(rs[i])
					inWord = true
				}
			}
		case c == ';' || c == '&' || c == '|' || c == '\n':
			endSegment()
		case c == ' ' || c == '\t' || c == '\r':
			endWord()
		default:
			cur.WriteRune(c)
			inWord = true
		}
	}
	endSegment()
	return segs
}

// claimOf reports the claim made by one simple command, if any.
func claimOf(dir string, words []string) (Claim, bool) {
	i := 0
	for i < len(words) && assignment.MatchString(words[i]) {
		i++
	}
	if i >= len(words) || filepath.Base(words[i]) != "bd" {
		return Claim{}, false
	}
	// bd's global flags come before the subcommand.
	for i++; i < len(words) && strings.HasPrefix(words[i], "-"); i++ {
		name, value, hasValue := strings.Cut(words[i], "=")
		switch {
		case otherDB[name]:
			return Claim{}, false
		case name == "-C" || name == "--directory":
			if !hasValue {
				if i+1 >= len(words) {
					return Claim{}, false
				}
				i++
				value = words[i]
			}
			dir = resolve(dir, value)
		case valueFlags[name] && !hasValue:
			i++
		}
	}
	if i >= len(words) || words[i] != "update" {
		return Claim{}, false
	}
	rest := words[i+1:]
	claim := false
	var ids []string
	for j := 0; j < len(rest); j++ {
		w := rest[j]
		name, _, _ := strings.Cut(w, "=")
		switch {
		case w == "--claim", w == "--status=in_progress", w == "-s=in_progress":
			claim = true
		case (w == "--status" || w == "-s") && j+1 < len(rest):
			j++
			if rest[j] == "in_progress" {
				claim = true
			}
		case otherDB[name]:
			return Claim{}, false
		case !strings.HasPrefix(w, "-") && idPattern.MatchString(w):
			ids = append(ids, w)
		}
	}
	if !claim || len(ids) == 0 {
		return Claim{}, false
	}
	return Claim{Dir: dir, IDs: ids}, true
}
