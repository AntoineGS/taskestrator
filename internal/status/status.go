// Package status derives a task's display state from its Beads status and
// the live state of its OpenCode session.
package status

// State is what the views show for a task.
type State int

const (
	Queued State = iota
	InProgress
	Waiting
	Review
	Done
	Lost
	Unknown
	Untracked
)

var names = [...]string{"queued", "in progress", "waiting on you", "review", "done", "lost", "unknown", "untracked"}

func (s State) String() string {
	if s >= 0 && int(s) < len(names) {
		return names[s]
	}
	return "invalid"
}

// Snapshot is the live state of a task's session tree.
type Snapshot struct {
	Exists      bool
	Running     bool
	Forms       int
	Permissions int
}

// Display is a derived state plus an optional detail, such as why a task is waiting.
type Display struct {
	State  State
	Detail string
}

func (d Display) String() string {
	if d.Detail == "" {
		return d.State.String()
	}
	return d.State.String() + " (" + d.Detail + ")"
}

// Derive applies the spec's status rules in order. snap is nil when the
// session's live state is unavailable.
func Derive(taskStatus, sessionID string, snap *Snapshot) Display {
	switch {
	case taskStatus == "closed":
		return Display{State: Done}
	case taskStatus == "review":
		return Display{State: Review}
	case sessionID == "" && taskStatus == "open":
		return Display{State: Queued}
	case sessionID == "":
		return Display{State: Untracked, Detail: taskStatus}
	case snap == nil:
		return Display{State: Unknown}
	case !snap.Exists:
		return Display{State: Lost}
	case snap.Forms > 0:
		return Display{State: Waiting, Detail: "question"}
	case snap.Permissions > 0:
		return Display{State: Waiting, Detail: "permission"}
	case snap.Running:
		return Display{State: InProgress}
	default:
		return Display{State: Waiting, Detail: "idle"}
	}
}
