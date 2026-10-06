package status

import "testing"

func TestDerive(t *testing.T) {
	running := &Snapshot{Exists: true, Running: true}
	idle := &Snapshot{Exists: true}
	cases := []struct {
		name, taskStatus, session string
		snap                      *Snapshot
		want                      Display
	}{
		{"closed wins over everything", "closed", "ses_1", running, Display{State: Done}},
		{"review wins over live state", "review", "ses_1", &Snapshot{Exists: true, Forms: 1}, Display{State: Review}},
		{"open without session is queued", "open", "", nil, Display{State: Queued}},
		{"untracked", "in_progress", "", nil, Display{State: Untracked, Detail: "in_progress"}},
		{"unreachable", "in_progress", "ses_1", nil, Display{State: Unknown}},
		{"lost", "in_progress", "ses_1", &Snapshot{}, Display{State: Lost}},
		{"question", "in_progress", "ses_1", &Snapshot{Exists: true, Running: true, Forms: 1}, Display{State: Waiting, Detail: "question"}},
		{"permission", "in_progress", "ses_1", &Snapshot{Exists: true, Running: true, Permissions: 2}, Display{State: Waiting, Detail: "permission"}},
		{"running", "in_progress", "ses_1", running, Display{State: InProgress}},
		{"idle", "in_progress", "ses_1", idle, Display{State: Waiting, Detail: "idle"}},
	}
	for _, c := range cases {
		if got := Derive(c.taskStatus, c.session, c.snap); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestDisplayString(t *testing.T) {
	if s := (Display{State: Waiting, Detail: "idle"}).String(); s != "waiting on you (idle)" {
		t.Fatalf("got %q", s)
	}
	if s := (Display{State: InProgress}).String(); s != "in progress" {
		t.Fatalf("got %q", s)
	}
}
