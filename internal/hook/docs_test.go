package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// BB-HOOK-7: Block rules see only what the hook reads, so the docs that say they read a command
// whole must give the same limit as MaxStdin.
func TestDocsGiveTheHookInputLimit(t *testing.T) {
	want := fmt.Sprintf("up to %d MB of hook input", MaxStdin>>20)
	for _, rel := range []string{"SECURITY.md", "docs/rules.md", "docs/recording.md"} {
		b, err := os.ReadFile(filepath.Join("..", "..", rel))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), want) {
			t.Errorf("%s doesn't say %q", rel, want)
		}
		if strings.Contains(string(b), "however long") {
			t.Errorf("%s says rules read a command however long; the hook reads at most %d MB", rel, MaxStdin>>20)
		}
	}
}
