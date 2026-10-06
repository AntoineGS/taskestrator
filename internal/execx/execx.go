// Package execx runs external commands for the adapter packages.
package execx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Runner runs a command in dir (the current directory when empty) and
// returns its standard output.
type Runner func(ctx context.Context, dir string, args ...string) ([]byte, error)

// Command returns a Runner for bin.
func Command(bin string) Runner {
	return func(ctx context.Context, dir string, args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = dir
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return out, &Error{
				Cmd:    strings.TrimSpace(bin + " " + strings.Join(args, " ")),
				Stderr: strings.TrimSpace(stderr.String()),
				Err:    err,
			}
		}
		return out, nil
	}
}

// Error describes a failed command.
type Error struct {
	Cmd    string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s: %v: %s", e.Cmd, e.Err, e.Stderr)
	}
	return fmt.Sprintf("%s: %v", e.Cmd, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// ExitCode returns the exit code carried by err, or -1 when there is none.
func ExitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
