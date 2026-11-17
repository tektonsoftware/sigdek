package rules

import (
	"testing"

	"github.com/tektonsoftware/sigdek/internal/event"
)

// MatchAny tries rules in order against every form of the action, so the first rule that
// matches any form wins, as Match does for one form: "folder" matches only the second form
// and "env" only the first, and "folder" comes first.
func TestMatchAnyKeepsRuleOrder(t *testing.T) {
	s := Set{Rules: []Rule{
		{ID: "folder", Effect: Deny, Path: "re:^secrets$"},
		{ID: "env", Effect: Deny, Path: "**/.env"},
		{ID: "setup", Effect: Flag, Path: "secrets/**"},
		{ID: "sigdek-setup-x", Effect: Flag, Path: "**/.env"},
	}}
	joined := &event.Action{Kind: "file_read", Name: "Grep", Path: "secrets/.env"}
	folder := &event.Action{Kind: "file_read", Name: "Grep", Path: "secrets"}
	if r := s.MatchAny([]*event.Action{joined, folder}, Deny); r == nil || r.ID != "folder" {
		t.Fatalf("got %+v", r)
	}
	if r := s.MatchAny([]*event.Action{{Kind: "file_read", Path: "src/*.ts"}, {Kind: "file_read", Path: "src"}}, Deny); r != nil {
		t.Fatalf("got %+v", r)
	}
	if r := s.MatchAny([]*event.Action{nil, joined}, Deny); r == nil || r.ID != "env" {
		t.Fatalf("nil form: %+v", r)
	}
	if r := s.MatchAny(nil, Deny); r != nil {
		t.Fatalf("no forms: %+v", r)
	}
	if r := s.MatchSetupAny([]*event.Action{folder, joined}); r == nil || r.ID != "sigdek-setup-x" {
		t.Fatalf("setup: %+v", r)
	}
}
