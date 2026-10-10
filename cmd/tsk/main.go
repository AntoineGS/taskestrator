// Command tsk is a bt plugin that dispatches Beads tasks to OpenCode agents and shows their live state.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/AntoineGS/taskestrator/internal/btplugin"
	"github.com/AntoineGS/taskestrator/internal/link"
)

const usage = `tsk is a bt plugin; bt starts it. Add it to ~/.config/bt/config.yaml:

  plugins:
    - name: tsk
      command: ["tsk", "plugin"]
      options:
        roots: ["~/gits"]
        model: "anthropic/claude-opus-5-5"   # optional

usage:
  tsk plugin    speak the bt plugin protocol on stdin/stdout (started by bt)
  tsk link      link the tasks a shell command claimed to its OpenCode session
                (run by the tsk OpenCode plugin; see the README)`

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "link" {
		code := link.Main(ctx, args[1:], os.Stderr, link.Default())
		cancel()
		os.Exit(code)
	}
	err := run(ctx, args)
	cancel()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tsk:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "plugin" {
		return errors.New(usage)
	}
	return btplugin.Main(ctx, os.Stdin, os.Stdout)
}
