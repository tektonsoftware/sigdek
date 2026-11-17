package initcmd

import (
	"strings"
	"testing"

	"github.com/tektonsoftware/sigdek/internal/event"
)

// BB-CLI-17, BB-SITE-2: .sigdek/README.md says redaction is best effort, and that text written
// inside a command (a heredoc) is recorded with it, up to the record's 4 KB limit.
func TestReadmeStatesTheLimits(t *testing.T) {
	if event.MaxCommand != 4096 {
		t.Fatalf("the README says commands are kept up to 4 KB, and MaxCommand is %d", event.MaxCommand)
	}
	for _, cursor := range []string{"", CursorSh, CursorBoth, CursorPowerShell} {
		text := strings.Join(strings.Fields(Readme(true, cursor)), " ")
		for _, want := range []string{
			"Prompt text, file contents and command output are never recorded, but a command is kept as the agent sent it (up to 4 KB), so text written inside one, such as a heredoc, is in the record.",
			"Secrets in common formats are stripped from commands (best effort).",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("cursor form %q: missing %q in\n%s", cursor, want, Readme(true, cursor))
			}
		}
		if strings.Contains(text, "secrets in commands are stripped.") {
			t.Errorf("cursor form %q: the old claim is still there", cursor)
		}
	}
}
