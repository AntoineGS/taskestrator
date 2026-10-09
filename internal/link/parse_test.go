package link

import (
	"reflect"
	"testing"
)

func TestParseClaims(t *testing.T) {
	one := func(dir string, ids ...string) []Claim { return []Claim{{Dir: dir, IDs: ids}} }
	tests := []struct {
		name, cmd string
		want      []Claim
	}{
		{"claim", "bd update tsk-a1b --claim", one("/r", "tsk-a1b")},
		{"status flag", "bd update tsk-a1b --status in_progress", one("/r", "tsk-a1b")},
		{"status equals", "bd update tsk-a1b --status=in_progress", one("/r", "tsk-a1b")},
		{"short status", "bd update tsk-a1b -s in_progress", one("/r", "tsk-a1b")},
		{"quoted status", `bd update tsk-a1b --status "in_progress"`, one("/r", "tsk-a1b")},
		{"several and dotted ids", "bd update tsk-a1b tsk-c2d.1 --claim", one("/r", "tsk-a1b", "tsk-c2d.1")},
		{"chain", "git pull && bd update tsk-a1b --claim; echo ok", one("/r", "tsk-a1b")},
		{"pipe and or", "true || bd update tsk-a1b --claim | cat", one("/r", "tsk-a1b")},
		{"newline", "echo hi\nbd update tsk-a1b --claim", one("/r", "tsk-a1b")},
		{"cd relative", "cd sub && bd update tsk-a1b --claim", one("/r/sub", "tsk-a1b")},
		{"cd absolute", "cd /x/y && bd update tsk-a1b --claim", one("/x/y", "tsk-a1b")},
		{"dash C", "bd -C ../other update tsk-a1b --claim", one("/other", "tsk-a1b")},
		{"directory equals", "bd --directory=/x update tsk-a1b --claim", one("/x", "tsk-a1b")},
		{"global value flag", "bd --actor alice update tsk-a1b --claim", one("/r", "tsk-a1b")},
		{"assignment and path", "BD_X=1 /usr/local/bin/bd update tsk-a1b --claim", one("/r", "tsk-a1b")},
		{"quoted operators", `bd update tsk-a1b --claim --notes "a && b; c | d"`, one("/r", "tsk-a1b")},
		{"flag value looks like an id", "bd update tsk-a1b --claim --notes fix-this", one("/r", "tsk-a1b", "fix-this")},
		{"duplicate", "bd update tsk-a1b --claim && bd update tsk-a1b --claim", one("/r", "tsk-a1b")},
		{"two dirs", "bd update tsk-a1b --claim && cd sub && bd update tsk-a1b --claim", []Claim{
			{Dir: "/r", IDs: []string{"tsk-a1b"}}, {Dir: "/r/sub", IDs: []string{"tsk-a1b"}},
		}},
		{"show", "bd show tsk-a1b", nil},
		{"close", "bd close tsk-a1b", nil},
		{"claim only in a quoted note", `bd update tsk-a1b --notes "--claim later"`, nil},
		{"echo", "echo bd update tsk-a1b --claim", nil},
		{"other status", "bd update tsk-a1b -s open", nil},
		{"no id", "bd update --claim", nil},
		{"other database", "bd --db other update tsk-a1b --claim", nil},
		{"global database", "bd update tsk-a1b --claim --global", nil},
		{"lookalike command", "bdx update tsk-a1b --claim", nil},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ParseClaims("/r", tt.cmd); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseClaims(%q) = %#v, want %#v", tt.cmd, got, tt.want)
			}
		})
	}
}
