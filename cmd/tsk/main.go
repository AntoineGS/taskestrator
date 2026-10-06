// Command tsk dispatches Beads tasks to OpenCode agents and shows their live state.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"

	tea "charm.land/bubbletea/v2"

	"github.com/AntoineGS/taskestrator/internal/config"
	"github.com/AntoineGS/taskestrator/internal/discover"
	"github.com/AntoineGS/taskestrator/internal/dispatch"
	"github.com/AntoineGS/taskestrator/internal/execx"
	"github.com/AntoineGS/taskestrator/internal/gitwt"
	"github.com/AntoineGS/taskestrator/internal/herdr"
	"github.com/AntoineGS/taskestrator/internal/opencode"
	"github.com/AntoineGS/taskestrator/internal/tracker"
	"github.com/AntoineGS/taskestrator/internal/ui"
)

const usage = `usage:
  tsk board            global board of dispatched tasks
  tsk project [dir]    one project's tasks, with dispatch (default: current directory)`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tsk:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 || (args[0] != "board" && args[0] != "project") {
		return errors.New(usage)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("bd"); err != nil {
		return fmt.Errorf("bd is not on PATH: %w", err)
	}
	projects, warnings := discover.Scan(cfg.Roots, cfg.Hidden)
	bd := tracker.New()

	// Assign interfaces only on success, so they never hold a typed nil pointer.
	var live ui.Live
	oc, ocErr := opencode.FromService(ctx)
	if ocErr == nil {
		live = oc
	} else {
		warnings = append(warnings, ocErr.Error())
	}

	var h herdr.Herdr
	var jump ui.Jumper
	if _, err := exec.LookPath("herdr"); err == nil {
		cli := herdr.New()
		h = cli
		jump = func(ctx context.Context, t dispatch.Target) error { return dispatch.Jump(ctx, cli, t) }
	} else {
		warnings = append(warnings, "herdr is not on PATH; jumping and dispatch are disabled")
	}

	var model tea.Model
	switch args[0] {
	case "board":
		model = ui.NewBoard(ctx, ui.BoardConfig{
			Projects: projects, Tracker: bd, Live: live, Jump: jump,
			Reread: cfg.RereadInterval, Warnings: warnings,
		})
	case "project":
		dir := "."
		if len(args) > 1 {
			dir = args[1]
		}
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		p, ok := discover.ForDir(projects, abs)
		if !ok {
			return fmt.Errorf("no Beads project under %v contains %s; add its parent to roots in %s", cfg.Roots, abs, config.DefaultPath())
		}
		pf, err := config.LoadProjectFile(p.Repo)
		if err != nil {
			return err
		}
		var dispatcher ui.Dispatcher
		if ocErr == nil && h != nil {
			deps := dispatch.Deps{Tracker: bd, OpenCode: oc, Herdr: h, Git: gitwt.New(), Shell: execx.Command("sh")}
			dispatcher = func(ctx context.Context, req dispatch.Request) (dispatch.Result, error) {
				return dispatch.Run(ctx, deps, req)
			}
		}
		model = ui.NewProject(ctx, ui.ProjectConfig{
			Project: p, File: pf, Tracker: bd, Live: live, Jump: jump, Dispatch: dispatcher,
			Git: gitwt.New(), Reread: cfg.RereadInterval, Warnings: warnings,
		})
	}
	_, err = tea.NewProgram(model, tea.WithContext(ctx)).Run()
	return err
}
