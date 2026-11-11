package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/initcmd"
	"github.com/tektonsoftware/sigdek/internal/rules"
	"github.com/tektonsoftware/sigdek/internal/store"
)

// QA1-SETUP-2 (b), the reviewer's repro with the real recorder: a project set up with the
// sh-and-PowerShell Cursor hooks and a Block rule, and PowerShell given the preToolUse command
// itself with Cursor's input on stdin (no wrapper piping it in). The command must still pass the
// input to launch.ps1, which runs the recorder: the action is denied and recorded once. Under
// Windows PowerShell 5.1 on Windows, and PowerShell 7 wherever it's installed.
func TestCursorDualCommandReadsStdinWithoutCursorsWrapper(t *testing.T) {
	var shells, env []string
	for _, ps := range []string{"powershell", "pwsh"} {
		if _, err := exec.LookPath(ps); err == nil && (ps == "pwsh" || runtime.GOOS == "windows") {
			shells = append(shells, ps)
		}
	}
	if runtime.GOOS == "windows" && (len(shells) == 0 || shells[0] != "powershell") {
		t.Fatal("Windows PowerShell isn't installed; this test must run under it on Windows")
	}
	if len(shells) == 0 {
		t.Skip("no PowerShell installed")
	}
	if _, err := exec.LookPath("powershell"); err != nil {
		// launch.ps1 is run by a command named powershell; here, PowerShell 7 stands in for it.
		shim := t.TempDir()
		os.WriteFile(filepath.Join(shim, "powershell"), []byte("#!/bin/sh\nexec pwsh \"$@\"\n"), 0o755)
		env = append(env, "PATH="+shim+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	for _, ps := range shells {
		root, home := repoDir(t), sigdekHome(t)
		if _, err := initcmd.Run(initcmd.Options{Root: root, Version: "dev", Tools: []string{"cursor"}, CursorForm: initcmd.CursorBoth}); err != nil {
			t.Fatal(err)
		}
		rules.Save(config.RulesPath(home), rules.Set{Version: 1, Rules: []rules.Rule{{ID: "no-rm", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "rm -rf*"}}})
		var hooks struct {
			Hooks map[string][]struct{ Command string } `json:"hooks"`
		}
		b, _ := os.ReadFile(filepath.Join(root, ".cursor", "hooks.json"))
		if err := json.Unmarshal(b, &hooks); err != nil {
			t.Fatal(err)
		}
		u := utf16.Encode([]rune(hooks.Hooks["preToolUse"][0].Command))
		enc := make([]byte, 2*len(u))
		for i, c := range u {
			enc[2*i], enc[2*i+1] = byte(c), byte(c>>8)
		}
		cmd := exec.Command(ps, "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(enc))
		cmd.Dir = root
		cmd.Env = append(append(os.Environ(), env...), "SIGDEK_HOME="+home, "SIGDEK_BIN="+binary)
		cwd, _ := json.Marshal(root)
		cmd.Stdin = strings.NewReader(`{"conversation_id":"c1","generation_id":"g1","hook_event_name":"preToolUse","tool_name":"Shell","tool_input":{"command":"rm -rf build"},"workspace_roots":[` + string(cwd) + `]}`)
		out, err := cmd.Output()
		if err != nil || !strings.Contains(string(out), `"permission":"deny"`) {
			t.Errorf("%s: %v, stdout %q; want the Block rule's denial", ps, err, out)
		}
		if head, _ := store.New(home).Head(); head.Seq != 1 {
			t.Errorf("%s: %d records, want 1", ps, head.Seq)
		}
	}
}
