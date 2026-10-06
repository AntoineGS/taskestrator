package dispatch

import (
	"fmt"
	"strings"
	"text/template"
)

// DefaultPrompt is used when a project's .tsk.toml sets no prompt.
const DefaultPrompt = `You are working on task {{.ID}}: {{.Title}}
{{if .Description}}
## Description

{{.Description}}
{{end}}{{if .Acceptance}}
## Acceptance criteria

{{.Acceptance}}
{{end}}
## Rules

- Work only in this worktree: {{.Worktree}} (branch {{.Branch}}).
- Commit on this branch with a "Task: {{.ID}}" trailer. Never push, merge or rebase onto another branch.
- If you need input from me, ask with the question tool.
- When the work is done and verified, run:
   bd update {{.ID}} --status review --notes "<one-paragraph summary of what changed and how it was verified>"
`

// PromptData is the data available to prompt templates.
type PromptData struct {
	ID, Title, Description, Acceptance, Worktree, Branch string
}

// RenderPrompt renders tmpl, or DefaultPrompt when tmpl is blank.
func RenderPrompt(tmpl string, data PromptData) (string, error) {
	if strings.TrimSpace(tmpl) == "" {
		tmpl = DefaultPrompt
	}
	t, err := template.New("prompt").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("prompt template: %w", err)
	}
	var b strings.Builder
	if err := t.Execute(&b, data); err != nil {
		return "", fmt.Errorf("prompt template: %w", err)
	}
	return b.String(), nil
}
