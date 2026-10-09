package tracker

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type fakeRun struct {
	calls []string
	dirs  []string
	out   map[string]string
	err   map[string]error
}

func (f *fakeRun) run(_ context.Context, dir string, args ...string) ([]byte, error) {
	k := strings.Join(args, " ")
	f.calls = append(f.calls, k)
	f.dirs = append(f.dirs, dir)
	if e := f.err[k]; e != nil {
		return []byte(f.out[k]), e
	}
	return []byte(f.out[k]), nil
}

func newBD(f *fakeRun) BD { return BD{Run: f.run} }

const listJSON = `[{"id":"se-1","title":"One","description":"d","acceptance_criteria":"ac","status":"in_progress","priority":1,
"metadata":{"tsk.session":"ses_a","tsk.worktree":"/r/.worktrees/se-1","n":3},"updated_at":"2026-10-06T17:51:07Z"},
{"id":"se-2","title":"Two","status":"closed","priority":2,"closed_at":"2026-10-06T18:00:00Z","updated_at":"2026-10-06T18:00:00Z"}]`

func TestListParsesTasks(t *testing.T) {
	f := &fakeRun{out: map[string]string{"list --json --all -n 0": listJSON}}
	tasks, err := newBD(f).List(context.Background(), "/r")
	if err != nil {
		t.Fatal(err)
	}
	if f.dirs[0] != "/r" {
		t.Fatalf("dir = %q", f.dirs[0])
	}
	if len(tasks) != 2 || tasks[0].Acceptance != "ac" || tasks[1].ClosedAt == nil {
		t.Fatalf("tasks = %+v", tasks)
	}
	if tasks[0].Meta(MetaSession) != "ses_a" || tasks[0].Meta("n") != "" || tasks[1].Meta(MetaSession) != "" {
		t.Fatal("Meta returned the wrong values")
	}
}

func TestReadyArgs(t *testing.T) {
	f := &fakeRun{out: map[string]string{"ready --json -n 0": "[]"}}
	if _, err := newBD(f).Ready(context.Background(), "/r"); err != nil {
		t.Fatal(err)
	}
}

func TestWriteArgs(t *testing.T) {
	f := &fakeRun{}
	bd := newBD(f)
	ctx := context.Background()
	_ = bd.Claim(ctx, "/r", "se-1")
	_ = bd.Unclaim(ctx, "/r", "se-1")
	_ = bd.SetMetadata(ctx, "/r", "se-1", map[string]string{MetaWorktree: "/w", MetaSession: "ses_a"})
	_ = bd.UnsetMetadata(ctx, "/r", "se-1", MetaSession, MetaBranch)
	want := []string{
		"update se-1 --claim",
		"update se-1 --status open --assignee ",
		"update se-1 --set-metadata tsk.session=ses_a --set-metadata tsk.worktree=/w",
		"update se-1 --unset-metadata tsk.session --unset-metadata tsk.branch",
	}
	if strings.Join(f.calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("calls:\n%s", strings.Join(f.calls, "\n"))
	}
}

func TestHasReviewStatus(t *testing.T) {
	cases := map[string]bool{"": false, "review:wip": true, "qa:wip, review:active": true, "reviewer:wip": false}
	for value, want := range cases {
		f := &fakeRun{out: map[string]string{"config get status.custom --json": `{"key":"status.custom","value":"` + value + `"}`}}
		got, err := newBD(f).HasReviewStatus(context.Background(), "/r")
		if err != nil || got != want {
			t.Errorf("%q: got %v, %v", value, got, err)
		}
	}
}

func TestAddReviewStatusKeepsExisting(t *testing.T) {
	f := &fakeRun{out: map[string]string{"config get status.custom --json": `{"value":"qa:wip"}`}}
	if err := newBD(f).AddReviewStatus(context.Background(), "/r"); err != nil {
		t.Fatal(err)
	}
	if last := f.calls[len(f.calls)-1]; last != "config set status.custom qa:wip,review:wip" {
		t.Fatalf("last call = %q", last)
	}
}

func TestAddReviewStatusNoopWhenPresent(t *testing.T) {
	f := &fakeRun{out: map[string]string{"config get status.custom --json": `{"value":"review:wip"}`}}
	if err := newBD(f).AddReviewStatus(context.Background(), "/r"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestErrorsPropagate(t *testing.T) {
	boom := errors.New("boom")
	f := &fakeRun{err: map[string]error{"update se-1 --claim": boom}}
	if err := newBD(f).Claim(context.Background(), "/r", "se-1"); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}

func TestShow(t *testing.T) {
	f := &fakeRun{
		out: map[string]string{
			"show se-1 --json": `[{"id":"se-1","title":"One","status":"open","metadata":{"tsk.session":"ses_a"}}]`,
			"show se-9 --json": `[]`,
			"show bad --json":  `{`,
		},
		err: map[string]error{"show boom --json": errors.New("bd down")},
	}
	bd := newBD(f)
	task, err := bd.Show(context.Background(), "/r", "se-1")
	if err != nil || task.ID != "se-1" || task.Meta("tsk.session") != "ses_a" || f.dirs[0] != "/r" {
		t.Fatalf("task = %+v, err = %v, dirs = %v", task, err, f.dirs)
	}
	if _, err := bd.Show(context.Background(), "/r", "se-9"); err == nil || err.Error() != "bd show se-9: not found" {
		t.Fatalf("empty array err = %v", err)
	}
	if _, err := bd.Show(context.Background(), "/r", "bad"); err == nil || !strings.Contains(err.Error(), "unexpected output") {
		t.Fatalf("bad json err = %v", err)
	}
	if _, err := bd.Show(context.Background(), "/r", "boom"); err == nil || !strings.Contains(err.Error(), "bd down") {
		t.Fatalf("run err = %v", err)
	}
}

func TestShowNotFound(t *testing.T) {
	k := "show tsk-zz --json"
	f := &fakeRun{
		out: map[string]string{k: `{"error":"no issues found matching the provided IDs","schema_version":1}`},
		err: map[string]error{k: errors.New("exit status 1")},
	}
	_, err := newBD(f).Show(context.Background(), "/r", "tsk-zz")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	f = &fakeRun{out: map[string]string{"show tsk-zz --json": `[]`}}
	if _, err := newBD(f).Show(context.Background(), "/r", "tsk-zz"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty array err = %v, want ErrNotFound", err)
	}
}

func TestShowOtherFailureIsNotNotFound(t *testing.T) {
	k := "show tsk-zz --json"
	f := &fakeRun{err: map[string]error{k: errors.New("no beads database found")}}
	_, err := newBD(f).Show(context.Background(), "/r", "tsk-zz")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
}
