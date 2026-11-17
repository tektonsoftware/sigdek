package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// BB-CLI-17: a repository whose .sigdek/README.md still has an older init's wording isn't
// called outdated: status judges the hook commands, not the README's text.
func TestStatusIgnoresAnOlderReadme(t *testing.T) {
	home, dir := sigdekHome(t), repoDir(t)
	run := func(args ...string) string {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "SIGDEK_HOME="+home)
		cmd.Dir = dir
		out, _ := cmd.CombinedOutput()
		return string(out)
	}
	run("init", "--offline")
	readme := filepath.Join(dir, ".sigdek", "README.md")
	b, err := os.ReadFile(readme)
	if err != nil || !strings.Contains(string(b), "(best\neffort)") && !strings.Contains(string(b), "(best effort)") {
		t.Fatalf("init's README (%v):\n%s", err, b)
	}
	before := run("status")
	old := "What is recorded: who ran which agent, which tool, the command or file path, and whether\nit ran or was blocked. Prompt text, file contents and command output are never recorded;\nsecrets in commands are stripped. Nothing leaves your machine until you run `sigdek login`.\n"
	i := strings.Index(string(b), "What is recorded:")
	j := strings.Index(string(b), "Source and documentation:")
	if i < 0 || j < i {
		t.Fatalf("README layout changed:\n%s", b)
	}
	os.WriteFile(readme, []byte(string(b[:i])+old+"\n"+string(b[j:])), 0o644)
	after := run("status")
	if strings.Contains(after, "older sigdek init") || strings.Contains(after, "README") {
		t.Fatalf("status after an older README:\n%s", after)
	}
	norm := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	if norm(before) != norm(after) {
		t.Fatalf("status changed with the README's text:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}
