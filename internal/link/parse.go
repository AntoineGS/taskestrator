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
	// bd flags (global and of bd update) that take no value; every other
	// flag consumes the next word unless written as --flag=value.
	boolFlags = map[string]bool{
		"--claim": true, "--force": true, "--allow-empty-description": true, "--ephemeral": true,
		"--history": true, "--no-history": true, "--persistent": true, "--stdin": true,
		"-h": true, "--help": true, "--cpu-profile": true, "--ignore-schema-skew": true,
		"--json": true, "--no-color": true, "-q": true, "--quiet": true, "--readonly": true,
		"--sandbox": true, "-v": true, "--verbose": true, "--global": true,
	}
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

type heredoc struct {
	delim string
	strip bool // <<- strips leading tabs from the body and delimiter
}

// segments splits command into simple commands on ;, &, |, and newlines
// outside quotes, and each command into words with quotes and backslash
// escapes removed. Comments and heredoc bodies are skipped, and empty
// commands are dropped.
func segments(command string) [][]string {
	var segs [][]string
	var words []string
	var cur strings.Builder
	var pending []heredoc
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
		case c == '#' && !inWord:
			for i+1 < len(rs) && rs[i+1] != '\n' {
				i++
			}
		case c == '<' && i+1 < len(rs) && rs[i+1] == '<':
			endWord()
			i += 2
			if i < len(rs) && rs[i] == '<' {
				continue // a here-string, not a heredoc
			}
			h := heredoc{}
			if i < len(rs) && rs[i] == '-' {
				h.strip = true
				i++
			}
			for i < len(rs) && (rs[i] == ' ' || rs[i] == '\t') {
				i++
			}
			var d strings.Builder
			for ; i < len(rs) && !strings.ContainsRune(" \t\n;&|<>", rs[i]); i++ {
				if q := rs[i]; q == '\'' || q == '"' {
					for i++; i < len(rs) && rs[i] != q; i++ {
						d.WriteRune(rs[i])
					}
					continue
				}
				d.WriteRune(rs[i])
			}
			i--
			h.delim = d.String()
			pending = append(pending, h)
		case c == '\n':
			endSegment()
			for _, h := range pending {
				i = skipBody(rs, i, h)
			}
			pending = nil
		case c == ';' || c == '&' || c == '|':
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

// skipBody skips a heredoc body starting after the newline at nl, through its
// delimiter line, and returns the index of the newline ending that line (or
// len(rs)).
func skipBody(rs []rune, nl int, h heredoc) int {
	start := nl + 1
	for start <= len(rs) {
		end := start
		for end < len(rs) && rs[end] != '\n' {
			end++
		}
		line := string(rs[start:end])
		if h.strip {
			line = strings.TrimLeft(line, "\t")
		}
		if line == h.delim || end >= len(rs) {
			return end
		}
		start = end + 1
	}
	return len(rs)
}

// claimOf reports the claim made by one simple command, if any. Flags may
// come before or after the subcommand; the values of flags are never taken
// for IDs or claim markers.
func claimOf(dir string, words []string) (Claim, bool) {
	i := 0
	for i < len(words) && assignment.MatchString(words[i]) {
		i++
	}
	if i >= len(words) || filepath.Base(words[i]) != "bd" {
		return Claim{}, false
	}
	sub := ""
	claim := false
	var ids []string
	for i++; i < len(words); i++ {
		w := words[i]
		if !strings.HasPrefix(w, "-") || w == "-" {
			switch {
			case sub == "":
				sub = w
			case idPattern.MatchString(w):
				ids = append(ids, w)
			}
			continue
		}
		name, value, hasValue := strings.Cut(w, "=")
		if otherDB[name] {
			return Claim{}, false
		}
		if boolFlags[name] {
			if name == "--claim" {
				claim = true
			}
			continue
		}
		if !hasValue {
			if i+1 >= len(words) {
				break
			}
			i++
			value = words[i]
		}
		switch name {
		case "-C", "--directory":
			dir = resolve(dir, value)
		case "-s", "--status":
			if value == "in_progress" {
				claim = true
			}
		}
	}
	if sub != "update" || !claim || len(ids) == 0 {
		return Claim{}, false
	}
	return Claim{Dir: dir, IDs: ids}, true
}
