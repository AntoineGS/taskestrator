package btplugin

import "testing"

func TestManifestDeclaresLink(t *testing.T) {
	m := Manifest()
	var untracked *FieldValue
	for i, v := range m.Fields[0].Values {
		if v.ID == "untracked" {
			untracked = &m.Fields[0].Values[i]
		}
	}
	if untracked == nil || untracked.Label != "Not linked" || untracked.Badge != "" || untracked.Lane == nil || *untracked.Lane {
		t.Fatalf("untracked value = %+v", untracked)
	}
	found := false
	for _, a := range m.Actions {
		if a == (ActionDecl{ID: "link", Label: "Link agent session", Key: "K"}) {
			found = true
		}
	}
	if !found {
		t.Fatalf("actions = %+v", m.Actions)
	}
}
