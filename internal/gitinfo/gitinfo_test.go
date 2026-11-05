package gitinfo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:Acme/API.git":                 "github.com/Acme/API",
		"https://github.com/acme/api.git":             "github.com/acme/api",
		"https://user:tok3n@github.com/acme/api":      "github.com/acme/api",
		"https://x-access-token:abc@GitHub.com/a/b/":  "github.com/a/b",
		"ssh://git@gitlab.example.com:2222/g/s/r.git": "gitlab.example.com/g/s/r",
		"https://dev.azure.com/org/proj/_git/repo":    "dev.azure.com/org/proj/_git/repo",
		"/Users/dev/src/local-only.git":               "",
		`C:\repos\local-only.git`:                     "",
		"D:/repos/local-only.git":                     "",
		`\\fileserver\git\local-only.git`:             "",
		"~/src/local-only.git":                        "",
		"../sibling":                                  "",
	} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindReadsOriginAndWorktrees(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "api")
	os.MkdirAll(filepath.Join(repo, ".git"), 0o755)
	os.MkdirAll(filepath.Join(repo, "src", "deep"), 0o755)
	os.WriteFile(filepath.Join(repo, ".git", "config"), []byte(`[core]
	bare = false
[remote "upstream"]
	url = git@github.com:other/fork.git
[remote "origin"]
	url = https://token:x@github.com/acme/api.git
	fetch = +refs/heads/*:refs/remotes/origin/*
`), 0o644)

	info := Find(filepath.Join(repo, "src", "deep"))
	if info.Root != repo || info.Remote == nil || *info.Remote != "github.com/acme/api" || len(info.RootHash) != 64 {
		t.Fatalf("got %+v", info)
	}

	// A linked worktree has a .git file pointing into the main repository.
	wt := filepath.Join(root, "api-wt")
	wtGit := filepath.Join(repo, ".git", "worktrees", "api-wt")
	os.MkdirAll(wtGit, 0o755)
	os.WriteFile(filepath.Join(wtGit, "commondir"), []byte("../..\n"), 0o644)
	os.MkdirAll(wt, 0o755)
	os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+wtGit+"\n"), 0o644)
	if info := Find(wt); info.Remote == nil || *info.Remote != "github.com/acme/api" || info.Root != wt {
		t.Fatalf("worktree: got %+v", info)
	}

	plain := filepath.Join(root, "no-repo")
	os.MkdirAll(plain, 0o755)
	if info := Find(plain); info.Remote != nil || info.Root != plain {
		t.Fatalf("no repo: got %+v", info)
	}
}

func TestRelPath(t *testing.T) {
	home, _ := os.UserHomeDir()
	work := t.TempDir() // absolute on every platform
	root := filepath.Join(work, "api")
	if got := RelPath(root, filepath.Join(root, "src", "main.go")); got != "src/main.go" {
		t.Errorf("inside repo: %q", got)
	}
	if got := RelPath(root, filepath.Join(home, ".ssh", "config")); got != "~/.ssh/config" {
		t.Errorf("home: %q", got)
	}
	sibling := filepath.Join(work, "api-other", "x")
	if got := RelPath(root, sibling); got == "x" || got == "../api-other/x" || got == "api-other/x" {
		t.Errorf("sibling prefix must not count as inside: %q", got)
	}
	if got := RelPath(root, filepath.Join("src", "x.go")); got != "src/x.go" {
		t.Errorf("relative: %q", got)
	}
}
