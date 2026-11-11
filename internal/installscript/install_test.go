// Package installscript tests install.sh, the script at the repository's root that installs the
// sigdek command on macOS and Linux without Node.js. The tests run it as `curl ... | sh` would,
// with sh (and dash and bash where they're installed), against a local server standing in for
// https://github.com/tektonsoftware/sigdek/releases.
package installscript

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
)

const latest = "0.0.99"

var targets = []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64", "windows_amd64", "windows_arm64"}

// shells are the POSIX shells installed here: install.sh must run under each.
func shells(t *testing.T) []string {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is for macOS and Linux")
	}
	var out []string
	for _, sh := range []string{"sh", "dash", "bash"} {
		if p, err := exec.LookPath(sh); err == nil {
			out = append(out, p)
		}
	}
	return out
}

func script(t *testing.T) string {
	p, err := filepath.Abs("../../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// fakeBinary stands in for a release build: a shell script that answers `version` the way
// sigdek does, so the tests can tell which build was installed.
func fakeBinary(version, target string) []byte {
	return []byte(fmt.Sprintf("#!/bin/sh\necho \"sigdek %s (%s)\"\n", version, strings.Replace(target, "_", "/", 1)))
}

// release serves what GitHub does under https://github.com/tektonsoftware/sigdek/releases, at
// base: /latest redirects to /tag/v<latest> (or to the releases page when there's no release),
// and /download/v<version>/ serves each version's binaries and checksums.txt.
type release struct {
	*httptest.Server
	base   string
	latest string
	files  map[string][]byte // path under base -> body
	mu     sync.Mutex
	paths  []string // each request's path under base, in order
}

func newRelease(t *testing.T, versions ...string) *release {
	r := &release{latest: latest, files: map[string][]byte{}}
	for _, v := range versions {
		var sums strings.Builder
		for _, tg := range targets {
			name := "sigdek_" + v + "_" + tg
			if strings.HasPrefix(tg, "windows") {
				name += ".exe"
			}
			body := fakeBinary(v, tg)
			r.files["/download/v"+v+"/"+name] = body
			fmt.Fprintf(&sums, "%x  %s\n", sha256.Sum256(body), name)
		}
		r.files["/download/v"+v+"/checksums.txt"] = []byte(sums.String())
	}
	r.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		p, ok := strings.CutPrefix(req.URL.Path, "/tektonsoftware/sigdek/releases")
		r.mu.Lock()
		r.paths = append(r.paths, p)
		tag := r.latest
		body, found := r.files[p]
		r.mu.Unlock()
		switch {
		case !ok:
			http.NotFound(w, req)
		case p == "/latest" && tag == "":
			http.Redirect(w, req, r.base, http.StatusFound)
		case p == "/latest":
			http.Redirect(w, req, r.base+"/tag/v"+tag, http.StatusFound) // absolute, as GitHub sends it
		case p == "" || strings.HasPrefix(p, "/tag/"):
			fmt.Fprint(w, "<html>a release page</html>")
		case found:
			w.Write(body)
		default:
			http.NotFound(w, req)
		}
	}))
	r.base = "http://" + r.Listener.Addr().String() + "/tektonsoftware/sigdek/releases" // before it serves
	r.Start()
	t.Cleanup(r.Close)
	return r
}

func (r *release) set(path string, body []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if body == nil {
		delete(r.files, path)
	} else {
		r.files[path] = body
	}
}

// requests counts the requests for path, or for anything when path is "".
func (r *release) requests(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, p := range r.paths {
		if p == path || path == "" {
			n++
		}
	}
	return n
}

// hostTarget is the build install.sh should pick on this machine.
func hostTarget() string { return runtime.GOOS + "_" + runtime.GOARCH }

// machine is a user's environment: its own HOME and TMPDIR, a PATH, and a login shell for the
// PATH hint.
type machine struct {
	t     *testing.T
	shell string // the sh that runs install.sh
	rel   *release
	home  string
	tmp   string // TMPDIR, where install.sh makes its temporary folder
	path  string
	env   []string
}

func newMachine(t *testing.T, shell string, rel *release) *machine {
	return &machine{t: t, shell: shell, rel: rel, home: t.TempDir(), tmp: t.TempDir(), path: os.Getenv("PATH")}
}

// defaultDir is where install.sh installs without SIGDEK_INSTALL_DIR.
func (m *machine) defaultDir() string { return filepath.Join(m.home, ".local", "bin") }

type result struct {
	stdout, stderr string
	code           int
}

func (m *machine) install(env ...string) result {
	m.t.Helper()
	cmd := exec.Command(m.shell, script(m.t))
	cmd.Dir = m.home
	cmd.Env = append([]string{
		"HOME=" + m.home, "TMPDIR=" + m.tmp, "PATH=" + m.path, "SHELL=/bin/zsh", "LC_ALL=C",
		"SIGDEK_RELEASE_BASE=" + m.rel.base,
	}, append(m.env, env...)...)
	var out, errb strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		m.t.Fatal(err)
	}
	// Whatever happened, the temporary folder is gone.
	if left, _ := os.ReadDir(m.tmp); len(left) > 0 {
		m.t.Errorf("install.sh left %s in its temporary folder's parent", left[0].Name())
	}
	return result{out.String(), errb.String(), code}
}

// installed checks that dir holds sigdek, mode 755, as the given build, and nothing else (no
// half-written copy), and that it runs.
func installed(t *testing.T, dir, version, target string) {
	t.Helper()
	bin := filepath.Join(dir, "sigdek")
	body, err := os.ReadFile(bin)
	if err != nil {
		t.Fatalf("sigdek isn't installed: %v", err)
	}
	if want := fakeBinary(version, target); string(body) != string(want) {
		t.Fatalf("installed %q, want the %s %s build", body, version, target)
	}
	if fi, _ := os.Stat(bin); fi.Mode().Perm() != 0o755 {
		t.Errorf("mode %v, want 755", fi.Mode().Perm())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 1 {
		t.Errorf("the install folder holds %d entries, want only sigdek", len(entries))
	}
	out, err := exec.Command(bin, "version").Output()
	if err != nil || !strings.Contains(string(out), "sigdek "+version) {
		t.Errorf("installed sigdek doesn't run: %q %v", out, err)
	}
}

func refused(t *testing.T, r result, want string) {
	t.Helper()
	if r.code == 0 {
		t.Fatalf("exit 0, want a failure; stdout:\n%s", r.stdout)
	}
	if !strings.Contains(r.stderr, "SigDek install failed: ") || !strings.Contains(r.stderr, want) {
		t.Errorf("stderr %q, want the reason %q", r.stderr, want)
	}
	if strings.Contains(r.stdout, "SigDek install failed") || strings.Contains(r.stdout, "Next, connect") {
		t.Errorf("stdout %q has the error or the next step", r.stdout)
	}
}

func notCreated(t *testing.T, dir string) {
	t.Helper()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("%s exists after a refused install (%v)", dir, err)
	}
}

func eachShell(t *testing.T, f func(t *testing.T, sh string)) {
	for _, sh := range shells(t) {
		t.Run(filepath.Base(sh), func(t *testing.T) {
			t.Parallel()
			f(t, sh)
		})
	}
}

func TestInstallsLatestIntoLocalBin(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, "0.0.98", latest)
		m := newMachine(t, sh, rel)
		r := m.install()
		if r.code != 0 || r.stderr != "" {
			t.Fatalf("exit %d, stderr %q", r.code, r.stderr)
		}
		dir := m.defaultDir()
		installed(t, dir, latest, hostTarget())
		bin := filepath.Join(dir, "sigdek")
		for _, want := range []string{
			"Installing SigDek " + latest + " (" + strings.Replace(hostTarget(), "_", "/", 1) + ") in " + dir + ".",
			"Checked sigdek_" + latest + "_" + hostTarget() + " against the release's checksums.txt.",
			"Installed " + bin + ": sigdek " + latest,
			// Not on PATH: the one line to add, and the full path meanwhile.
			dir + " isn't on your PATH. To add it, put this line in ~/.zshrc, then open a new terminal:\n" +
				`  export PATH="$HOME/.local/bin:$PATH"` + "\n" +
				"Until then, run sigdek by its full path, " + bin + ".",
			"Next, connect this machine to your team:\n  " + bin + " login\n",
		} {
			if !strings.Contains(r.stdout, want) {
				t.Errorf("stdout lacks %q:\n%s", want, r.stdout)
			}
		}
		if !strings.HasSuffix(r.stdout, bin+" login\n") {
			t.Errorf("stdout doesn't end with the next step:\n%s", r.stdout)
		}
	})
}

func TestInstallsTheVersionAsked(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		for _, v := range []string{"0.0.98", "v0.0.98"} {
			rel := newRelease(t, "0.0.98", latest)
			m := newMachine(t, sh, rel)
			dir := filepath.Join(m.home, "tools")
			if r := m.install("SIGDEK_VERSION="+v, "SIGDEK_INSTALL_DIR="+dir); r.code != 0 {
				t.Fatalf("SIGDEK_VERSION=%s: exit %d, stderr %q", v, r.code, r.stderr)
			}
			installed(t, dir, "0.0.98", hostTarget())
			notCreated(t, m.defaultDir())
			if n := rel.requests("/latest"); n != 0 {
				t.Errorf("SIGDEK_VERSION=%s still asked for the latest release", v)
			}
		}
	})
}

func TestReRunSaysSoOrUpdates(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, "0.0.98", latest)
		m := newMachine(t, sh, rel)
		dir := m.defaultDir()
		bin := "/download/v" + latest + "/sigdek_" + latest + "_" + hostTarget()
		if r := m.install(); r.code != 0 {
			t.Fatalf("first install: exit %d, stderr %q", r.code, r.stderr)
		}
		r := m.install()
		if r.code != 0 || !strings.Contains(r.stdout, "SigDek "+latest+" is already installed there.") {
			t.Fatalf("same version again: exit %d, stdout:\n%s\nstderr %q", r.code, r.stdout, r.stderr)
		}
		if n := rel.requests(bin); n != 1 {
			t.Errorf("the binary was downloaded %d times, want once", n)
		}
		installed(t, dir, latest, hostTarget())

		// An older version asked for replaces it, and a plain re-run updates to the latest again.
		if r := m.install("SIGDEK_VERSION=0.0.98"); r.code != 0 || strings.Contains(r.stdout, "already") {
			t.Fatalf("SIGDEK_VERSION=0.0.98: exit %d, stdout:\n%s", r.code, r.stdout)
		}
		installed(t, dir, "0.0.98", hostTarget())
		if r := m.install(); r.code != 0 {
			t.Fatalf("update: exit %d, stderr %q", r.code, r.stderr)
		}
		installed(t, dir, latest, hostTarget())

		// A copy that isn't the release's build (damaged, or built by hand) is replaced.
		os.WriteFile(filepath.Join(dir, "sigdek"), []byte("#!/bin/sh\necho damaged\n"), 0o755)
		if r := m.install(); r.code != 0 || strings.Contains(r.stdout, "already") {
			t.Fatalf("over a damaged copy: exit %d, stdout:\n%s", r.code, r.stdout)
		}
		installed(t, dir, latest, hostTarget())
	})
}

func TestRefusesAWrongChecksum(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, "0.0.98", latest)
		m := newMachine(t, sh, rel)
		dir := m.defaultDir()
		if r := m.install("SIGDEK_VERSION=0.0.98"); r.code != 0 {
			t.Fatalf("first install: exit %d, stderr %q", r.code, r.stderr)
		}
		// The latest release's binary isn't the one checksums.txt describes.
		rel.set("/download/v"+latest+"/sigdek_"+latest+"_"+hostTarget(), []byte("#!/bin/sh\necho tampered\n"))
		refused(t, m.install(), "doesn't match its SHA-256 in checksums.txt")
		installed(t, dir, "0.0.98", hostTarget()) // the earlier install is untouched

		fresh := newMachine(t, sh, rel)
		refused(t, fresh.install(), "so nothing was installed")
		notCreated(t, fresh.defaultDir())
	})
}

func TestRefusesAMissingChecksum(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)
		sums := "/download/v" + latest + "/checksums.txt"
		var kept []string
		for _, l := range strings.SplitAfter(string(rel.files[sums]), "\n") { // before any request
			if !strings.Contains(l, hostTarget()) {
				kept = append(kept, l)
			}
		}
		for _, body := range []string{strings.Join(kept, ""), "", "not-a-sha256  sigdek_" + latest + "_" + hostTarget() + "\n"} {
			rel.set(sums, []byte(body))
			m := newMachine(t, sh, rel)
			refused(t, m.install(), "has no SHA-256 for sigdek_"+latest+"_"+hostTarget()+", so nothing was installed")
			notCreated(t, m.defaultDir())
		}
		if n := rel.requests("/download/v" + latest + "/sigdek_" + latest + "_" + hostTarget()); n != 0 {
			t.Errorf("the binary was downloaded without a checksum to check it against")
		}
	})
}

func TestRefusesMissingFiles(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		// The binary isn't in the release.
		rel := newRelease(t, latest)
		rel.set("/download/v"+latest+"/sigdek_"+latest+"_"+hostTarget(), nil)
		m := newMachine(t, sh, rel)
		refused(t, m.install(), "couldn't download "+rel.base+"/download/v"+latest+"/sigdek_"+latest+"_"+hostTarget()+", so nothing was installed")
		notCreated(t, m.defaultDir())

		// A version that was never released.
		m = newMachine(t, sh, rel)
		refused(t, m.install("SIGDEK_VERSION=9.9.9"), "Check that v9.9.9 is a SigDek release")
		notCreated(t, m.defaultDir())

		// No release yet: /latest goes to the releases page, which names no version.
		rel.mu.Lock()
		rel.latest = ""
		rel.mu.Unlock()
		m = newMachine(t, sh, rel)
		refused(t, m.install(), "Set SIGDEK_VERSION")
		notCreated(t, m.defaultDir())
	})
}

func TestRefusesABadVersion(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)
		for _, v := range []string{"latest", "v", "1.0/../../x", "0.1.0 ", "0.1.0?x=1"} {
			m := newMachine(t, sh, rel)
			refused(t, m.install("SIGDEK_VERSION="+v), "isn't a SigDek version")
		}
		if n := rel.requests(""); n != 0 {
			t.Errorf("a bad version still made %d requests", n)
		}
	})
}

func TestPathHints(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)

		// On PATH: no hint, and the next step is the plain command.
		m := newMachine(t, sh, rel)
		dir := filepath.Join(m.home, "bin")
		m.path = dir + ":" + m.path
		r := m.install("SIGDEK_INSTALL_DIR=" + dir + "/")
		if r.code != 0 || strings.Contains(r.stdout, "PATH") || !strings.HasSuffix(r.stdout, "\n  sigdek login\n") {
			t.Fatalf("on PATH: exit %d, stdout:\n%s", r.code, r.stdout)
		}
		installed(t, dir, latest, hostTarget())

		// Another sigdek comes first.
		other := filepath.Join(m.home, "npm-global")
		os.MkdirAll(other, 0o755)
		os.WriteFile(filepath.Join(other, "sigdek"), []byte("#!/bin/sh\necho other\n"), 0o755)
		m.path = other + ":" + m.path
		r = m.install("SIGDEK_INSTALL_DIR=" + dir)
		if want := "Another sigdek, in " + other + ", comes before " + dir + " on your PATH"; r.code != 0 || !strings.Contains(r.stdout, want) ||
			!strings.HasSuffix(r.stdout, filepath.Join(dir, "sigdek")+" login\n") {
			t.Fatalf("behind another sigdek: exit %d, stdout:\n%s", r.code, r.stdout)
		}

		// A folder outside HOME is shown as it is.
		m = newMachine(t, sh, rel)
		outside := filepath.Join(t.TempDir(), "bin")
		if r := m.install("SIGDEK_INSTALL_DIR=" + outside); !strings.Contains(r.stdout, `  export PATH="`+outside+`:$PATH"`) {
			t.Fatalf("outside HOME: stdout:\n%s", r.stdout)
		}
	})
}

// fakeTools makes a folder of stand-ins for uname and sysctl that report what the test says,
// so one machine can play every platform.
func fakeTools(t *testing.T, kernel, machineName, translated string) string {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "uname"), []byte(fmt.Sprintf("#!/bin/sh\ncase \"$1\" in -m) echo %s ;; *) echo %s ;; esac\n", machineName, kernel)), 0o755)
	sysctl := "#!/bin/sh\necho 'sysctl: unknown oid' >&2\nexit 1\n"
	if translated != "" {
		sysctl = "#!/bin/sh\n[ \"$*\" = '-n sysctl.proc_translated' ] && echo " + translated + "\n"
	}
	os.WriteFile(filepath.Join(dir, "sysctl"), []byte(sysctl), 0o755)
	return dir
}

func TestPicksTheBuildForThePlatform(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)
		for _, c := range []struct{ kernel, machine, translated, target string }{
			{"Linux", "x86_64", "", "linux_amd64"},
			{"Linux", "amd64", "", "linux_amd64"},
			{"Linux", "aarch64", "", "linux_arm64"},
			{"Linux", "arm64", "", "linux_arm64"},
			{"Darwin", "arm64", "0", "darwin_arm64"},
			{"Darwin", "x86_64", "", "darwin_amd64"},  // an Intel Mac: no such sysctl
			{"Darwin", "x86_64", "0", "darwin_amd64"}, // not translated
			{"Darwin", "x86_64", "1", "darwin_arm64"}, // a shell under Rosetta on Apple silicon
		} {
			m := newMachine(t, sh, rel)
			m.path = fakeTools(t, c.kernel, c.machine, c.translated) + ":" + m.path
			if r := m.install(); r.code != 0 {
				t.Fatalf("%v: exit %d, stderr %q", c, r.code, r.stderr)
			}
			installed(t, m.defaultDir(), latest, c.target)
		}
		for _, c := range []struct{ kernel, machine, want string }{
			{"FreeBSD", "amd64", "installs SigDek on macOS and Linux only, and this machine runs FreeBSD"},
			{"MINGW64_NT-10.0-19045", "x86_64", "installs SigDek on macOS and Linux only"},
			{"Linux", "i686", "there's no SigDek build for i686 processors"},
			{"Linux", "armv7l", "there's no SigDek build for armv7l processors"},
		} {
			m := newMachine(t, sh, rel)
			m.path = fakeTools(t, c.kernel, c.machine, "") + ":" + m.path
			r := m.install()
			refused(t, r, c.want)
			if !strings.Contains(r.stderr, "https://github.com/tektonsoftware/sigdek#getting-the-sigdek-command") {
				t.Errorf("%v: no pointer to the docs: %q", c, r.stderr)
			}
			notCreated(t, m.defaultDir())
		}
		if n := rel.requests("/latest"); n != 8 {
			t.Errorf("%d requests for the latest version, want one per supported platform", n)
		}
	})
}

func TestProfileLinePerShell(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)
		for _, c := range []struct{ kernel, shell, profile, line string }{
			{"Darwin", "/bin/zsh", "~/.zshrc", `export PATH="$HOME/.local/bin:$PATH"`},
			{"Darwin", "/bin/bash", "~/.bash_profile", `export PATH="$HOME/.local/bin:$PATH"`},
			{"Linux", "/usr/bin/bash", "~/.bashrc", `export PATH="$HOME/.local/bin:$PATH"`},
			{"Linux", "/usr/bin/fish", "~/.config/fish/config.fish", `fish_add_path "$HOME/.local/bin"`},
			{"Linux", "/bin/sh", "~/.profile", `export PATH="$HOME/.local/bin:$PATH"`},
			{"Linux", "", "~/.profile", `export PATH="$HOME/.local/bin:$PATH"`},
		} {
			m := newMachine(t, sh, rel)
			m.path = fakeTools(t, c.kernel, "x86_64", "") + ":" + m.path
			r := m.install("SHELL=" + c.shell)
			if want := "put this line in " + c.profile + ", then open a new terminal:\n  " + c.line + "\n"; !strings.Contains(r.stdout, want) {
				t.Errorf("SHELL=%s on %s: stdout lacks %q:\n%s", c.shell, c.kernel, want, r.stdout)
			}
		}
	})
}

// toolsWithout is a PATH folder holding every program on this machine's PATH except the ones
// named, as links.
func toolsWithout(t *testing.T, names ...string) string {
	dir := t.TempDir()
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if !filepath.IsAbs(p) {
			continue
		}
		entries, _ := os.ReadDir(p)
		for _, e := range entries {
			if slices.Contains(names, e.Name()) {
				continue
			}
			os.Symlink(filepath.Join(p, e.Name()), filepath.Join(dir, e.Name())) // the first one on PATH wins
		}
	}
	return dir
}

func TestWithoutCurl(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, "0.0.98", latest)
		m := newMachine(t, sh, rel)
		m.path = toolsWithout(t, "curl", "wget")
		refused(t, m.install(), "this needs curl or wget to download SigDek.")
		notCreated(t, m.defaultDir())

		if _, err := exec.LookPath("wget"); err != nil {
			t.Log("wget isn't installed here, so the wget download isn't tested")
			return
		}
		m.path = toolsWithout(t, "curl")
		if r := m.install(); r.code != 0 {
			t.Fatalf("with wget: exit %d, stderr %q", r.code, r.stderr)
		}
		installed(t, m.defaultDir(), latest, hostTarget())
		if r := m.install("SIGDEK_VERSION=0.0.98"); r.code != 0 {
			t.Fatalf("with wget, SIGDEK_VERSION: exit %d, stderr %q", r.code, r.stderr)
		}
		installed(t, m.defaultDir(), "0.0.98", hostTarget())
		rel.set("/download/v"+latest+"/sigdek_"+latest+"_"+hostTarget(), nil)
		refused(t, m.install(), "couldn't download")
		installed(t, m.defaultDir(), "0.0.98", hostTarget())
	})
}

func TestRefusesAFolderItCantUse(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)
		m := newMachine(t, sh, rel)

		// sigdek is a folder there.
		dir := filepath.Join(m.home, "bin")
		os.MkdirAll(filepath.Join(dir, "sigdek"), 0o755)
		refused(t, m.install("SIGDEK_INSTALL_DIR="+dir), "is a folder")

		if os.Geteuid() == 0 {
			t.Log("running as root, so a read-only folder isn't tested")
			return
		}
		ro := filepath.Join(m.home, "read-only")
		os.MkdirAll(ro, 0o555)
		t.Cleanup(func() { os.Chmod(ro, 0o755) })
		refused(t, m.install("SIGDEK_INSTALL_DIR="+ro), "couldn't write "+filepath.Join(ro, "sigdek"))
		if entries, _ := os.ReadDir(ro); len(entries) != 0 {
			t.Errorf("a refused install left %s", entries[0].Name())
		}
	})
}
