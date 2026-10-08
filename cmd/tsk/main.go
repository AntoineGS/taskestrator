// Command tsk is a bt plugin that dispatches Beads tasks to OpenCode agents and shows their live state.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/AntoineGS/taskestrator/internal/btplugin"
)

const usage = `tsk is a bt plugin; bt starts it. Add it to ~/.config/bt/config.yaml:

  plugins:
    - name: tsk
      command: ["tsk", "plugin"]
      options:
        roots: ["~/gits"]
        model: "anthropic/claude-opus-5-5"   # optional

usage:
  tsk plugin    speak the bt plugin protocol on stdin/stdout (started by bt)`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tsk:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) != 1 || args[0] != "plugin" {
		return errors.New(usage)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	return btplugin.Main(ctx, os.Stdin, os.Stdout)
}
