package link

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/tracker"
)

// Entry is the outcome of linking one claimed task.
type Entry struct {
	Dir     string
	ID      string
	Outcome Outcome
	Err     error
}

// Claims links every task in claims to session.
func (l Linker) Claims(ctx context.Context, session string, claims []Claim) []Entry {
	var entries []Entry
	for _, c := range claims {
		for _, id := range c.IDs {
			out, err := l.Link(ctx, c.Dir, id, session)
			entries = append(entries, Entry{Dir: c.Dir, ID: id, Outcome: out, Err: err})
		}
	}
	return entries
}

// LogPath is where tsk link records its decisions.
func LogPath() (string, error) {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "tsk", "link.log"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "tsk", "link.log"), nil
}

// WriteLog writes one line per entry: time, session, directory, task, and
// the result (with the root session when linked) or the error.
func WriteLog(w io.Writer, at time.Time, session string, entries []Entry) error {
	for _, e := range entries {
		what := string(e.Outcome.Result)
		switch {
		case e.Err != nil:
			what = "error: " + strings.ReplaceAll(e.Err.Error(), "\n", "; ")
		case e.Outcome.Result == Linked || e.Outcome.Result == Same:
			what += " " + e.Outcome.Root
		}
		if _, err := fmt.Fprintf(w, "%s %s %s %s %s\n", at.UTC().Format(time.RFC3339), session, e.Dir, e.ID, what); err != nil {
			return err
		}
	}
	return nil
}

// Deps are what tsk link uses from the outside world.
type Deps struct {
	Tracker Tracker
	Connect func(ctx context.Context) (OpenCode, error)
	Log     func() (io.WriteCloser, error)
	Now     func() time.Time
}

// Default returns the real dependencies.
func Default() Deps {
	return Deps{
		Tracker: tracker.New(),
		Connect: func(ctx context.Context) (OpenCode, error) {
			c, err := opencode.FromService(ctx)
			if err != nil {
				return nil, err
			}
			return c, nil
		},
		Log: openLog,
		Now: time.Now,
	}
}

func openLog() (io.WriteCloser, error) {
	p, err := LogPath()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

const (
	usage      = "usage: tsk link --session <id> [--workdir <dir>] --command <command>"
	runTimeout = time.Minute
)

// Main runs tsk link with the arguments after "link". It returns 0 when every
// claimed task got a result, 1 when any got an error, and 2 on bad arguments.
func Main(ctx context.Context, args []string, stderr io.Writer, d Deps) int {
	fs := flag.NewFlagSet("link", flag.ContinueOnError)
	fs.SetOutput(stderr)
	session := fs.String("session", "", "OpenCode session that ran the command")
	workdir := fs.String("workdir", "", "workdir of the shell call, if it had one")
	command := fs.String("command", "", "the shell command")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *session == "" || *command == "" || fs.NArg() > 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	// Most commands claim nothing; those never contact OpenCode.
	if len(ParseClaims("/", *command)) == 0 {
		return 0
	}

	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	entries := run(ctx, d, *session, *workdir, *command)
	code := 0
	for _, e := range entries {
		if e.Err != nil {
			code = 1
		}
	}
	w, err := d.Log()
	if err != nil {
		fmt.Fprintf(stderr, "tsk link: open log: %v\n", err)
		w = nopCloser{stderr}
	}
	defer w.Close()
	if err := WriteLog(w, d.Now(), *session, entries); err != nil {
		fmt.Fprintf(stderr, "tsk link: write log: %v\n", err)
	}
	return code
}

// run links the claims in command. The base directory is the session's own;
// workdir, when set, replaces it (absolute) or is joined to it (relative).
func run(ctx context.Context, d Deps, session, workdir, command string) []Entry {
	fail := func(err error) []Entry {
		base := "-"
		if filepath.IsAbs(workdir) {
			base = workdir
		}
		var entries []Entry
		for _, c := range ParseClaims(base, command) {
			for _, id := range c.IDs {
				entries = append(entries, Entry{Dir: c.Dir, ID: id, Err: err})
			}
		}
		return entries
	}
	oc, err := d.Connect(ctx)
	if err != nil {
		return fail(err)
	}
	s, err := oc.GetSession(ctx, session)
	if err != nil {
		return fail(fmt.Errorf("session %s: %w", session, err))
	}
	dir := s.Location.Directory
	if workdir != "" {
		dir = resolve(dir, workdir)
	}
	l := Linker{Tracker: d.Tracker, OpenCode: oc}
	return l.Claims(ctx, session, ParseClaims(dir, command))
}
