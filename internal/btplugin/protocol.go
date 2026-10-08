// Package btplugin implements tsk's side of the bt plugin protocol: JSON-RPC
// 2.0 over newline-delimited JSON on stdio.
package btplugin

import (
	"encoding/json"
	"time"
)

const (
	ProtocolVersion = 1
	Name            = "tsk"
	Version         = "0.1.0"
)

// Bead is one entry of a beads.sync notification.
type Bead struct {
	DB       string                     `json:"db"`
	ID       string                     `json:"id"`
	Repo     *string                    `json:"repo"`
	Status   string                     `json:"status"`
	Title    string                     `json:"title"`
	Type     string                     `json:"type"`
	Priority int                        `json:"priority"`
	Assignee string                     `json:"assignee"`
	Metadata map[string]json.RawMessage `json:"metadata,omitempty"`
}

type BeadKey struct {
	DB string `json:"db"`
	ID string `json:"id"`
}

type BeadRef struct {
	DB   string  `json:"db"`
	ID   string  `json:"id"`
	Repo *string `json:"repo"`
}

type Scope struct {
	Mode      string   `json:"mode"`
	Databases []string `json:"databases"`
}

type InitializeParams struct {
	ProtocolVersion int            `json:"protocolVersion"`
	Scope           Scope          `json:"scope"`
	Popup           bool           `json:"popup"`
	Options         map[string]any `json:"options,omitempty"`
}

// PluginManifest is the initialize result. The constructor is Manifest.
type PluginManifest struct {
	ProtocolVersion int           `json:"protocolVersion"`
	Name            string        `json:"name"`
	Version         string        `json:"version"`
	Subscribe       Subscribe     `json:"subscribe"`
	Fields          []Field       `json:"fields"`
	Sections        []SectionDecl `json:"sections"`
	Actions         []ActionDecl  `json:"actions"`
}

type Subscribe struct {
	MetadataPrefixes []string `json:"metadata_prefixes,omitempty"`
	Statuses         []string `json:"statuses,omitempty"`
}

type Field struct {
	ID     string       `json:"id"`
	Label  string       `json:"label"`
	Values []FieldValue `json:"values"`
}

type FieldValue struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Badge string `json:"badge,omitempty"`
	Tone  string `json:"tone,omitempty"`
	Lane  *bool  `json:"lane,omitempty"`
}

type SectionDecl struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

type ActionDecl struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Key   string `json:"key,omitempty"`
}

type SyncParams struct {
	Revision int    `json:"revision"`
	Beads    []Bead `json:"beads"`
}

type BeadState struct {
	DB       string                `json:"db"`
	ID       string                `json:"id"`
	Fields   map[string]FieldState `json:"fields,omitempty"`
	Sections map[string]string     `json:"sections,omitempty"`
	Actions  []string              `json:"actions"`
}

type FieldState struct {
	Value string     `json:"value"`
	Since *time.Time `json:"since,omitempty"`
}

type StateSetParams struct {
	Replace bool        `json:"replace"`
	Beads   []BeadState `json:"beads"`
}

type StateClearParams struct {
	Beads []BeadKey `json:"beads"`
}

type InvokeParams struct {
	Action string  `json:"action"`
	Bead   BeadRef `json:"bead"`
	View   string  `json:"view"`
}

type InvokeResult struct {
	Toast   *Toast `json:"toast,omitempty"`
	Dismiss bool   `json:"dismiss,omitempty"`
}

type Toast struct {
	Message string `json:"message"`
	Tone    string `json:"tone,omitempty"`
}

// Manifest returns tsk's plugin manifest.
func Manifest() PluginManifest {
	no := false
	return PluginManifest{
		ProtocolVersion: ProtocolVersion,
		Name:            Name,
		Version:         Version,
		Subscribe: Subscribe{
			MetadataPrefixes: []string{"tsk."},
			Statuses:         []string{"open", "in_progress", "review", "blocked"},
		},
		Fields: []Field{{
			ID:    "state",
			Label: "Agent",
			Values: []FieldValue{
				{ID: "waiting", Label: "Waiting on you", Badge: "WAIT", Tone: "warn"},
				{ID: "running", Label: "In progress", Badge: "RUN", Tone: "accent"},
				{ID: "review", Label: "Review", Badge: "REV", Tone: "ok"},
				{ID: "lost", Label: "Lost", Badge: "LOST", Tone: "error"},
				{ID: "unknown", Label: "Unknown", Badge: "?", Tone: "muted", Lane: &no},
				{ID: "queued", Label: "Queued", Lane: &no},
			},
		}},
		Sections: []SectionDecl{{ID: "agent", Title: "Agent"}},
		Actions: []ActionDecl{
			{ID: "dispatch", Label: "Dispatch to agent", Key: "D"},
			{ID: "jump", Label: "Jump to agent tab", Key: "J"},
		},
	}
}
