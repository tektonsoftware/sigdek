// Package gitinfo finds the repository a directory belongs to by reading .git directly,
// without running git, so the hook path stays fast.
package gitinfo

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"

	"github.com/tektonsoftware/sigdek/internal/event"
)

type Info struct {
	Root     string  // repository root, or the directory itself when not in a repository
	Remote   *string // normalized origin remote, nil when there is none
	RootHash string
}

// Find walks up from dir to the nearest repository.
func Find(dir string) Info {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	dir = filepath.Clean(dir)
	for d := dir; ; {
		gitPath := filepath.Join(d, ".git")
		if st, err := os.Stat(gitPath); err == nil {
			gitDir := gitPath
			if !st.IsDir() {
				gitDir = resolveGitFile(d, gitPath)
			}
			info := Info{Root: d, RootHash: event.SHA256Hex([]byte(d))}
			if url := originURL(gitDir); url != "" {
				if n := Normalize(url); n != "" {
					info.Remote = &n
				}
			}
			return info
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return Info{Root: dir, RootHash: event.SHA256Hex([]byte(dir))}
}

// resolveGitFile follows a worktree or submodule ".git" file ("gitdir: <path>") to the
// directory that holds the shared config.
func resolveGitFile(root, gitFile string) string {
	b, err := os.ReadFile(gitFile)
	if err != nil {
		return gitFile
	}
	line := strings.TrimSpace(string(b))
	p := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	if c, err := os.ReadFile(filepath.Join(p, "commondir")); err == nil {
		common := strings.TrimSpace(string(c))
		if !filepath.IsAbs(common) {
			common = filepath.Join(p, common)
		}
		return filepath.Clean(common)
	}
	return p
}

// originURL reads [remote "origin"] url from the config, falling back to the first remote.
func originURL(gitDir string) string {
	f, err := os.Open(filepath.Join(gitDir, "config"))
	if err != nil {
		return ""
	}
	defer f.Close()
	var section, first, origin string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if !strings.HasPrefix(section, `[remote "`) {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) != "url" {
			continue
		}
		v = strings.TrimSpace(v)
		if first == "" {
			first = v
		}
		if section == `[remote "origin"]` {
			origin = v
		}
	}
	if origin != "" {
		return origin
	}
	return first
}

// Normalize turns any remote URL form into host/path with no scheme, credentials, port or .git:
//
//	git@github.com:Acme/API.git        → github.com/Acme/API
//	https://user:tok@github.com/a/b    → github.com/a/b
//	ssh://git@gitlab.com:2222/g/s/r.git → gitlab.com/g/s/r
func Normalize(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
		if at := strings.LastIndex(strings.SplitN(s, "/", 2)[0], "@"); at >= 0 {
			s = s[at+1:]
		}
		host, path, _ := strings.Cut(s, "/")
		if h, _, ok := strings.Cut(host, ":"); ok {
			host = h
		}
		s = strings.ToLower(host) + "/" + path
	} else if at := strings.Index(s, "@"); at >= 0 && strings.Contains(s[at:], ":") {
		// scp-like: user@host:path
		rest := s[at+1:]
		host, path, _ := strings.Cut(rest, ":")
		s = strings.ToLower(host) + "/" + strings.TrimPrefix(path, "/")
	} else if isLocalPath(s) {
		return "" // local path remotes reveal the filesystem; leave them out
	}
	s = strings.TrimSuffix(strings.TrimRight(s, "/"), ".git")
	return event.Clean(s, 512)
}

// isLocalPath recognizes path remotes whatever OS wrote them: /abs, \\server\share, C:\dir,
// C:/dir, ./rel, ../rel, ~/dir and file: URLs. filepath.IsAbs alone misses Unix paths on Windows.
func isLocalPath(s string) bool {
	if strings.HasPrefix(s, "/") || strings.HasPrefix(s, `\`) || strings.HasPrefix(s, ".") || strings.HasPrefix(s, "~") || strings.HasPrefix(s, "file:") {
		return true
	}
	if len(s) >= 3 && s[1] == ':' && (s[2] == '\\' || s[2] == '/') && ((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= 'A' && s[0] <= 'Z')) {
		return true
	}
	return filepath.IsAbs(s)
}

// RelPath makes p relative to root when it is inside, and replaces the home directory
// with ~ otherwise, so recorded paths don't expose the machine's layout.
func RelPath(root, p string) string {
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		return filepath.ToSlash(filepath.Clean(p))
	}
	if rel, err := filepath.Rel(root, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return filepath.ToSlash(rel)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rel, err := filepath.Rel(home, p); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "~/" + filepath.ToSlash(rel)
		}
	}
	return filepath.ToSlash(p)
}
