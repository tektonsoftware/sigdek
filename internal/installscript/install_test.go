// Package installscript tests install.sh, the script at the repository's root that installs the
// sigdek command on macOS and Linux without Node.js. The tests run it as `curl ... | sh` would,
// with sh (and dash, bash and zsh where they're installed), against a local server standing in
// for https://github.com/tektonsoftware/sigdek/releases.
package installscript

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io/fs"
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

// shells are the shells installed here that install.sh must run under: POSIX shells, and zsh
// in its own mode, for people who type `| zsh`.
func shells(t *testing.T) []string {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh is for macOS and Linux")
	}
	var out []string
	for _, sh := range []string{"sh", "dash", "bash", "zsh"} {
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

	drops       map[string]int   // path -> how many more requests get half the body, then a closed connection
	holds       map[string]bool  // path -> send all but the last byte, then wait until the client goes away
	answers     map[string][]int // path -> HTTP statuses to answer its next requests with, one each
	lowerHeader bool             // send /latest's redirect as a raw "location:" header
}

func newRelease(t *testing.T, versions ...string) *release {
	r := &release{latest: latest, files: map[string][]byte{}, drops: map[string]int{}, holds: map[string]bool{}, answers: map[string][]int{}}
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
		drop := r.drops[p] > 0
		if drop {
			r.drops[p]--
		}
		hold, lower := r.holds[p], r.lowerHeader
		status := 0
		if q := r.answers[p]; len(q) > 0 {
			status, r.answers[p] = q[0], q[1:]
		}
		r.mu.Unlock()
		switch {
		case !ok:
			http.NotFound(w, req)
		case status != 0:
			http.Error(w, http.StatusText(status), status)
		case p == "/latest" && tag == "":
			http.Redirect(w, req, r.base, http.StatusFound)
		case p == "/latest" && lower:
			raw(w, "HTTP/1.1 302 Found\r\nlocation: "+r.base+"/tag/v"+tag+"\r\ncontent-length: 0\r\n\r\n")
		case p == "/latest":
			http.Redirect(w, req, r.base+"/tag/v"+tag, http.StatusFound) // absolute, as GitHub sends it
		case p == "" || strings.HasPrefix(p, "/tag/"):
			fmt.Fprint(w, "<html>a release page</html>")
		case found && drop:
			raw(w, fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%s", len(body), body[:len(body)/2]))
		case found && hold:
			w.Header().Set("Content-Length", fmt.Sprint(len(body)))
			w.Write(body[:len(body)-1])
			w.(http.Flusher).Flush()
			<-req.Context().Done()
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

// raw writes response as it is, then closes the connection.
func raw(w http.ResponseWriter, response string) {
	conn, buf, err := w.(http.Hijacker).Hijack()
	if err != nil {
		panic(err)
	}
	buf.WriteString(response)
	buf.Flush()
	conn.Close()
}

// drop makes the next n requests for path get half the body, then a closed connection.
func (r *release) drop(path string, n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.drops[path] = n
}

// answer makes the next requests for path get these HTTP statuses, one each.
func (r *release) answer(path string, statuses ...int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.answers[path] = statuses
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
	// prelude, when set, runs in /bin/sh before it execs the shell on install.sh, so it can use
	// the $$ that install.sh will have.
	prelude string
}

func newMachine(t *testing.T, shell string, rel *release) *machine {
	return &machine{t: t, shell: shell, rel: rel, home: realTempDir(t), tmp: realTempDir(t), path: os.Getenv("PATH")}
}

// realTempDir is a new temporary folder, by its physical path: install.sh prints the folder it
// installs into that way, and on macOS t.TempDir is under /var, a link to /private/var.
func realTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
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
	if m.prelude != "" {
		cmd = exec.Command("/bin/sh", "-c", m.prelude+`; exec "$0" "$1"`, m.shell, script(m.t))
	}
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
		// It says only that it's already there: not "Installing", "Checked" or "Installed".
		if r.code != 0 || !strings.HasPrefix(r.stdout, "sigdek at "+filepath.Join(dir, "sigdek")+" is already "+latest+".\n") ||
			strings.Contains(r.stdout, "Install") || strings.Contains(r.stdout, "Checked") {
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
		// The binary isn't in the release: an HTTP error, tried 3 times.
		rel := newRelease(t, latest)
		bin := "/download/v" + latest + "/sigdek_" + latest + "_" + hostTarget()
		rel.set(bin, nil)
		m := newMachine(t, sh, rel)
		r := m.install()
		refused(t, r, "couldn't download "+rel.base+bin+" (curl: (22) ")
		if !strings.HasSuffix(r.stderr, "404), so nothing was installed.\n") {
			t.Errorf("stderr %q doesn't end by saying nothing was installed", r.stderr)
		}
		if n := rel.requests(bin); n != 3 {
			t.Errorf("the binary was asked for %d times, want 3", n)
		}
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
		refused(t, m.install(), "Set SIGDEK_VERSION to the version you want, after the pipe: curl -fsSL https://sigdek.com/install.sh | SIGDEK_VERSION=0.1.0 sh")
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
		outside := filepath.Join(realTempDir(t), "bin")
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
		const export = `export PATH="$HOME/.local/bin:$PATH"`
		const csh = `set path = ( "$HOME/.local/bin" $path )`
		for _, c := range []struct {
			kernel, shell string
			files         []string // already in HOME
			profile, line string
		}{
			{"Darwin", "/bin/zsh", nil, "~/.zshrc", export},
			{"Darwin", "/bin/bash", nil, "~/.bash_profile", export},
			// A login bash reads only the first of ~/.bash_profile, ~/.bash_login and ~/.profile.
			{"Darwin", "/bin/bash", []string{".profile"}, "~/.profile", export},
			{"Darwin", "/bin/bash", []string{".bash_login", ".profile"}, "~/.bash_login", export},
			{"Darwin", "/bin/bash", []string{".bash_profile", ".profile"}, "~/.bash_profile", export},
			{"Linux", "/usr/bin/bash", []string{".profile"}, "~/.bashrc", export},
			{"Linux", "/usr/bin/fish", nil, "~/.config/fish/config.fish", `fish_add_path "$HOME/.local/bin"`},
			{"Linux", "/bin/sh", nil, "~/.profile", export},
			{"Linux", "", nil, "~/.profile", export},
			{"Linux", "/bin/csh", nil, "~/.cshrc", csh},
			{"Darwin", "/bin/csh", nil, "~/.cshrc", csh},
			{"Linux", "/usr/bin/tcsh", nil, "~/.cshrc", csh},
			{"Darwin", "/bin/tcsh", []string{".cshrc"}, "~/.cshrc", csh},
			{"Darwin", "/bin/tcsh", []string{".tcshrc", ".cshrc"}, "~/.tcshrc", csh},
		} {
			m := newMachine(t, sh, rel)
			m.path = fakeTools(t, c.kernel, "x86_64", "") + ":" + m.path
			for _, f := range c.files {
				os.WriteFile(filepath.Join(m.home, f), []byte("# set up\n"), 0o644)
			}
			r := m.install("SHELL=" + c.shell)
			if want := "put this line in " + c.profile + ", then open a new terminal:\n  " + c.line + "\n"; !strings.Contains(r.stdout, want) {
				t.Errorf("SHELL=%s on %s with %v: stdout lacks %q:\n%s", c.shell, c.kernel, c.files, want, r.stdout)
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
		noCurl := toolsWithout(t, "curl")
		m.path = noCurl
		if r := m.install(); r.code != 0 {
			t.Fatalf("with wget: exit %d, stderr %q", r.code, r.stderr)
		}
		installed(t, m.defaultDir(), latest, hostTarget())
		if r := m.install("SIGDEK_VERSION=0.0.98"); r.code != 0 {
			t.Fatalf("with wget, SIGDEK_VERSION: exit %d, stderr %q", r.code, r.stderr)
		}
		installed(t, m.defaultDir(), "0.0.98", hostTarget())

		// A dropped connection is tried again (GNU wget itself tries only once).
		fresh := newMachine(t, sh, rel)
		fresh.path = noCurl
		bin98 := "/download/v0.0.98/sigdek_0.0.98_" + hostTarget()
		before := rel.requests(bin98)
		rel.drop(bin98, 1)
		if r := fresh.install("SIGDEK_VERSION=0.0.98"); r.code != 0 || !strings.Contains(r.stderr, "Trying again.") {
			t.Fatalf("with wget, a dropped connection: exit %d, stderr %q", r.code, r.stderr)
		}
		installed(t, fresh.defaultDir(), "0.0.98", hostTarget())
		if n := rel.requests(bin98) - before; n != 2 {
			t.Errorf("with wget, a dropped connection: %d requests, want 2", n)
		}

		// The latest version's redirect, with its header in lower case.
		rel.mu.Lock()
		rel.lowerHeader = true
		rel.mu.Unlock()
		fresh = newMachine(t, sh, rel)
		fresh.path = noCurl
		if r := fresh.install(); r.code != 0 {
			t.Fatalf("with wget, a lower-case location header: exit %d, stderr %q", r.code, r.stderr)
		}
		installed(t, fresh.defaultDir(), latest, hostTarget())
		rel.mu.Lock()
		rel.lowerHeader = false
		rel.mu.Unlock()

		bin := "/download/v" + latest + "/sigdek_" + latest + "_" + hostTarget()
		rel.set(bin, nil)
		r := m.install()
		refused(t, r, "couldn't download")
		if !strings.HasSuffix(r.stderr, "so nothing was installed.\n") {
			t.Errorf("with wget, a missing binary: stderr %q", r.stderr)
		}
		installed(t, m.defaultDir(), "0.0.98", hostTarget())

		// No server to ask for the latest version: a network failure, not a missing release.
		gone := httptest.NewServer(http.NotFoundHandler())
		gone.Close()
		fresh = newMachine(t, sh, rel)
		fresh.path = noCurl
		refused(t, fresh.install("SIGDEK_RELEASE_BASE="+gone.URL+"/releases"), "couldn't reach "+gone.URL+"/releases/latest to find the latest version (")

		// An HTTP error from /latest itself: tried again, then named. One from the release page
		// it redirects to doesn't count.
		rel.set(bin, fakeBinary(latest, hostTarget())) // back, after the missing-binary case
		before = rel.requests("/latest")
		rel.answer("/latest", 503, 503, 503)
		fresh = newMachine(t, sh, rel)
		fresh.path = noCurl
		refused(t, fresh.install(), "couldn't find the latest version: "+rel.base+"/latest answered with HTTP status 503, 3 times.")
		if n := rel.requests("/latest") - before; n != 3 {
			t.Errorf("with wget, /latest answering 503: %d requests, want 3", n)
		}
		rel.answer("/tag/v"+latest, 503)
		fresh = newMachine(t, sh, rel)
		fresh.path = noCurl
		if r := fresh.install(); r.code != 0 {
			t.Fatalf("with wget, the release page answering 503: exit %d, stderr %q", r.code, r.stderr)
		}
		installed(t, fresh.defaultDir(), latest, hostTarget())

		// Through localhost, which wget may try at ::1 first (refused: the server is on
		// 127.0.0.1), a dropped download's reason is the drop, not that refusal.
		local := strings.Replace(rel.base, "127.0.0.1", "localhost", 1)
		rel.drop(bin98, 3)
		fresh = newMachine(t, sh, rel)
		fresh.path = noCurl
		r = fresh.install("SIGDEK_RELEASE_BASE="+local, "SIGDEK_VERSION=0.0.98")
		refused(t, r, "the download of "+local+"/download/v0.0.98/sigdek_0.0.98_"+hostTarget()+" failed (Connection closed at byte ")
		if strings.Contains(r.stderr, "refused") {
			t.Errorf("with wget through localhost, a drop is reported as a refused connection: %q", r.stderr)
		}
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

// tree lists every file, folder and link under root (but not under skip), with its mode, size
// and contents or link target, so a test can tell whether anything there changed.
func tree(t *testing.T, root, skip string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if p == skip {
			return filepath.SkipDir
		}
		fi, _ := os.Lstat(p)
		v := fi.Mode().String()
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			l, _ := os.Readlink(p)
			v += " -> " + l
		case fi.Mode().IsRegular():
			b, _ := os.ReadFile(p)
			v += fmt.Sprintf(" %x", sha256.Sum256(b))
		}
		out[p] = v
		return nil
	})
	return out
}

func TestLeavesPlantedFilesAlone(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)
		for _, existing := range []bool{false, true} {
			m := newMachine(t, sh, rel)
			dir := m.defaultDir()
			if existing {
				if r := m.install("SIGDEK_VERSION=" + latest); r.code != 0 {
					t.Fatalf("first install: exit %d, stderr %q", r.code, r.stderr)
				}
				os.WriteFile(filepath.Join(dir, "sigdek"), []byte("#!/bin/sh\necho older\n"), 0o755)
			}
			os.MkdirAll(dir, 0o755)
			// Files outside the install folder that someone who can write in it points at.
			victims := filepath.Join(m.home, "victims")
			os.MkdirAll(victims, 0o700)
			os.WriteFile(filepath.Join(victims, "profile"), []byte("keep me\n"), 0o600)
			os.MkdirAll(filepath.Join(victims, "folder"), 0o700)
			// Links at the old predictable name (with the PID install.sh will have), at names
			// shaped like the new ones, and at the template itself; and a folder.
			m.prelude = `ln -s "$HOME/victims/profile" "$HOME/.local/bin/.sigdek.install.$$" && mkdir "$HOME/.local/bin/.sigdek.dir.$$"`
			planted := []string{".sigdek.XXXXXX", ".sigdek.install", ".sigdek."}
			for i := 0; i < 200; i++ {
				planted = append(planted, fmt.Sprintf(".sigdek.%06d", i), fmt.Sprintf(".sigdek.%c%c%c%c%c%c", 'a'+i%26, 'A'+i%26, 'a'+i/26, '0'+i%10, 'z'-i%26, 'Z'-i/26))
			}
			for i, name := range planted {
				target := filepath.Join(victims, "profile")
				switch i % 3 {
				case 1:
					target = filepath.Join(victims, "folder")
				case 2:
					target = filepath.Join(victims, "missing")
				}
				os.Symlink(target, filepath.Join(dir, name))
			}
			before := tree(t, m.home, filepath.Join(dir, "sigdek"))
			if r := m.install(); r.code != 0 {
				t.Fatalf("existing copy %v: exit %d, stderr %q", existing, r.code, r.stderr)
			}
			after := tree(t, m.home, filepath.Join(dir, "sigdek"))
			for p, v := range before {
				if after[p] != v {
					t.Errorf("existing copy %v: %s was %q, now %q", existing, p, v, after[p])
				}
			}
			for p, v := range after {
				if _, ok := before[p]; ok {
					continue
				}
				// What the prelude planted, under install.sh's PID, must be as it was made.
				base := filepath.Base(p)
				if strings.HasPrefix(base, ".sigdek.install.") && strings.HasPrefix(v, "L") && strings.HasSuffix(v, " -> "+filepath.Join(victims, "profile")) ||
					strings.HasPrefix(base, ".sigdek.dir.") && strings.HasPrefix(v, "d") {
					continue
				}
				t.Errorf("existing copy %v: install.sh left %s (%s)", existing, p, v)
			}
			bin := filepath.Join(dir, "sigdek")
			if fi, err := os.Lstat(bin); err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o755 {
				t.Fatalf("existing copy %v: sigdek is %v (%v), want a file of mode 755", existing, fi.Mode(), err)
			}
			if body, _ := os.ReadFile(bin); string(body) != string(fakeBinary(latest, hostTarget())) {
				t.Errorf("existing copy %v: installed %q", existing, body)
			}
		}
	})
}

func TestBackslashesInPaths(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		paths := map[string]string{"default": ""}
		if _, err := exec.LookPath("shasum"); err == nil {
			paths["shasum"] = toolsWithout(t, "sha256sum")
		}
		for tool, path := range paths {
			rel := newRelease(t, latest)
			m := newMachine(t, sh, rel)
			if path != "" {
				m.path = path
			}
			m.tmp = filepath.Join(t.TempDir(), `t\mp`)
			os.MkdirAll(m.tmp, 0o700)
			dir := filepath.Join(m.home, `my\bin`)
			for i := 0; i < 2; i++ {
				r := m.install("SIGDEK_INSTALL_DIR=" + dir)
				if r.code != 0 {
					t.Fatalf("%s, run %d: exit %d, stderr %q", tool, i+1, r.code, r.stderr)
				}
				if i == 1 && !strings.Contains(r.stdout, " is already "+latest+".") {
					t.Errorf("%s: the second run doesn't say it's already installed:\n%s", tool, r.stdout)
				}
			}
			installed(t, dir, latest, hostTarget())
			if n := rel.requests("/download/v" + latest + "/sigdek_" + latest + "_" + hostTarget()); n != 1 {
				t.Errorf("%s: the binary was downloaded %d times, want once", tool, n)
			}
		}
	})
}

func TestTriesAFailedDownloadAgain(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)
		sums := "/download/v" + latest + "/checksums.txt"
		bin := "/download/v" + latest + "/sigdek_" + latest + "_" + hostTarget()

		// Each dropped once (curl's own --retry gives up on a cut-short body): both recover.
		rel.drop(sums, 1)
		rel.drop(bin, 1)
		m := newMachine(t, sh, rel)
		r := m.install()
		if r.code != 0 {
			t.Fatalf("dropped once: exit %d, stderr %q", r.code, r.stderr)
		}
		installed(t, m.defaultDir(), latest, hostTarget())
		for _, p := range []string{sums, bin} {
			if n := rel.requests(p); n != 2 {
				t.Errorf("%s asked for %d times, want 2", p, n)
			}
			if want := "Couldn't download " + rel.base + p + " (curl: (18) "; !strings.Contains(r.stderr, want) || !strings.Contains(r.stderr, "Trying again.") {
				t.Errorf("stderr %q doesn't say it's trying %s again", r.stderr, p)
			}
		}

		// Dropped every time: 3 tries, then a network failure, not a missing release.
		rel.drop(bin, 3)
		m = newMachine(t, sh, rel)
		r = m.install()
		refused(t, r, "the download of "+rel.base+bin+" failed (curl: (18) ")
		if !strings.Contains(r.stderr, "so nothing was installed. Check the network connection and run this again.") || strings.Contains(r.stderr, "SigDek release") {
			t.Errorf("stderr %q", r.stderr)
		}
		if n := rel.requests(bin); n != 2+3 {
			t.Errorf("the binary was asked for %d more times, want 3", n-2)
		}
		notCreated(t, m.defaultDir())

		rel.drop(sums, 3)
		m = newMachine(t, sh, rel)
		r = m.install()
		refused(t, r, "the download of "+rel.base+sums+" failed (curl: (18) ")
		if strings.Contains(r.stderr, "SigDek release") {
			t.Errorf("a network failure is reported as a missing release: %q", r.stderr)
		}

		// Nothing answering for the latest version.
		gone := httptest.NewServer(http.NotFoundHandler())
		gone.Close()
		m = newMachine(t, sh, rel)
		refused(t, m.install("SIGDEK_RELEASE_BASE="+gone.URL+"/releases"), "couldn't reach "+gone.URL+"/releases/latest to find the latest version (curl: (7) ")
	})
}

func TestTildeInInstallFolder(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)
		m := newMachine(t, sh, rel)
		r := m.install(`SIGDEK_INSTALL_DIR=~/tools`)
		if r.code != 0 {
			t.Fatalf("~/tools: exit %d, stderr %q", r.code, r.stderr)
		}
		installed(t, filepath.Join(m.home, "tools"), latest, hostTarget())
		if _, err := os.Lstat(filepath.Join(m.home, "~")); !os.IsNotExist(err) {
			t.Errorf("made a folder named ~ (%v)", err)
		}
		if !strings.Contains(r.stdout, `  export PATH="$HOME/tools:$PATH"`) {
			t.Errorf("~/tools: stdout:\n%s", r.stdout)
		}

		// A lone ~ is the home folder itself.
		m = newMachine(t, sh, rel)
		if r := m.install(`SIGDEK_INSTALL_DIR=~`); r.code != 0 {
			t.Fatalf("~: exit %d, stderr %q", r.code, r.stderr)
		}
		installed(t, m.home, latest, hostTarget())

		m = newMachine(t, sh, rel)
		refused(t, m.install(`SIGDEK_INSTALL_DIR=~/tools`, "HOME="), "HOME isn't set, so the ~ in SIGDEK_INSTALL_DIR can't be expanded.")

		// Any other ~ is refused, before any request, rather than made into a folder named ~root
		// (which rm -rf ~root, to remove it, would take as root's home folder).
		before := rel.requests("")
		for _, d := range []string{`~root/bin`, `~nosuchuser-5f19`, `~~/bin`, `~-/bin`} {
			m = newMachine(t, sh, rel)
			refused(t, m.install("SIGDEK_INSTALL_DIR="+d), "SIGDEK_INSTALL_DIR starts with a ~ that this script can't expand (only ~ and ~/ are). Write the full path, or use $HOME.")
			if left, _ := os.ReadDir(m.home); len(left) > 0 {
				t.Errorf("%s: made %s", d, left[0].Name())
			}
		}
		if n := rel.requests("") - before; n != 0 {
			t.Errorf("a ~user folder made %d requests before it was refused", n)
		}
	})
}

// The paths install.sh prints are the folder it installed into, with ., .. and symlinks
// resolved, and HOME still shown as $HOME in the profile line.
func TestPrintedPathsAreTheFolder(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)

		m := newMachine(t, sh, rel) // runs in m.home
		r := m.install("SIGDEK_INSTALL_DIR=.")
		installed(t, m.home, latest, hostTarget())
		for _, want := range []string{
			"Installing SigDek " + latest + " (" + strings.Replace(hostTarget(), "_", "/", 1) + ") in " + m.home + ".\n",
			"Installed " + filepath.Join(m.home, "sigdek") + ": ",
			`  export PATH="$HOME:$PATH"` + "\n",
		} {
			if r.code != 0 || !strings.Contains(r.stdout, want) {
				t.Errorf("SIGDEK_INSTALL_DIR=.: stdout lacks %q:\n%s", want, r.stdout)
			}
		}
		if strings.Contains(r.stdout, m.home+"/.") {
			t.Errorf("SIGDEK_INSTALL_DIR=.: stdout has a /.:\n%s", r.stdout)
		}

		// .. in a part that doesn't exist yet, as mkdir -p makes it.
		m = newMachine(t, sh, rel)
		r = m.install("SIGDEK_INSTALL_DIR=" + m.home + "/new/./../bin/")
		installed(t, filepath.Join(m.home, "bin"), latest, hostTarget())
		if want := " in " + filepath.Join(m.home, "bin") + ".\n"; r.code != 0 || !strings.Contains(r.stdout, want) ||
			!strings.Contains(r.stdout, `  export PATH="$HOME/bin:$PATH"`) || strings.Contains(r.stdout, "..") {
			t.Errorf("new/./../bin: stdout:\n%s", r.stdout)
		}

		// HOME through a symlink: the physical folder, and $HOME in the profile line.
		m = newMachine(t, sh, rel)
		link := filepath.Join(realTempDir(t), "home")
		os.Symlink(m.home, link)
		r = m.install("HOME=" + link)
		installed(t, m.defaultDir(), latest, hostTarget())
		if r.code != 0 || !strings.Contains(r.stdout, "Installed "+filepath.Join(m.defaultDir(), "sigdek")+": ") ||
			!strings.Contains(r.stdout, `  export PATH="$HOME/.local/bin:$PATH"`) {
			t.Errorf("HOME through a symlink: stdout:\n%s", r.stdout)
		}
	})
}

// A folder whose name a shell would read as code gets a profile line that sets PATH to the
// folder, and runs nothing, in sh, csh and fish.
func TestProfileLineQuotesTheFolder(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)
		for _, name := range []string{"a\"b$(touch pwned)`touch pwned`", `it's`, `back\slash\`, `$HOME`, `a!b`} {
			for _, under := range []bool{true, false} {
				m := newMachine(t, sh, rel)
				parent := m.home
				if !under {
					parent = realTempDir(t)
				}
				dir := filepath.Join(parent, name)
				for _, c := range []struct{ shell, run string }{
					{"/bin/zsh", "sh"},
					{"/bin/csh", "csh"},
					{"/usr/bin/fish", "fish"},
				} {
					r := m.install("SIGDEK_INSTALL_DIR="+dir, "SHELL="+c.shell)
					if r.code != 0 {
						t.Fatalf("%q: exit %d, stderr %q", dir, r.code, r.stderr)
					}
					_, after, _ := strings.Cut(r.stdout, "then open a new terminal:\n  ")
					line, _, _ := strings.Cut(after, "\n")
					if !under && strings.Contains(line, "$HOME") && name != "$HOME" {
						t.Errorf("%q: the line uses $HOME: %s", dir, line)
					}
					shell, err := exec.LookPath(c.run)
					if err != nil {
						continue
					}
					// Run the line in that shell, in a folder of its own, and read PATH back.
					runDir := realTempDir(t)
					args := []string{"-c", line + "; /usr/bin/env printenv PATH"}
					switch c.run {
					case "csh":
						args = append([]string{"-f"}, args...)
					case "fish":
						args = append([]string{"--no-config"}, args...)
					}
					cmd := exec.Command(shell, args...)
					cmd.Dir = runDir
					cmd.Env = []string{"HOME=" + m.home, "PATH=/usr/bin:/bin", "XDG_CONFIG_HOME=" + runDir, "XDG_DATA_HOME=" + runDir}
					out, err := cmd.Output()
					got, _, _ := strings.Cut(strings.TrimSpace(string(out)), ":")
					if err != nil || got != dir {
						t.Errorf("%s ran %s and set PATH's first folder to %q (%v), want %q", c.run, line, got, err, dir)
					}
					if _, err := os.Stat(filepath.Join(runDir, "pwned")); err == nil {
						t.Errorf("%s ran a command in the folder's name: %s", c.run, line)
					}
				}
			}
		}
	})
}

func TestOnPathSpelledDifferently(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)
		onPath := func(what string, m *machine, env ...string) {
			t.Helper()
			r := m.install(env...)
			if r.code != 0 || strings.Contains(r.stdout, "PATH") || !strings.HasSuffix(r.stdout, "\n  sigdek login\n") {
				t.Errorf("%s: exit %d, stdout:\n%s", what, r.code, r.stdout)
			}
		}

		m := newMachine(t, sh, rel)
		dir := filepath.Join(m.home, "bin")
		m.path = dir + ":" + m.path
		onPath("install folder with //", m, "SIGDEK_INSTALL_DIR="+dir+"//")
		installed(t, dir, latest, hostTarget())

		m = newMachine(t, sh, rel)
		m.path = dir + "//:" + m.path
		onPath("PATH entry with //", m, "SIGDEK_INSTALL_DIR="+dir)

		// A PATH entry through a symlink, and the other way round.
		m = newMachine(t, sh, rel)
		real := filepath.Join(m.home, "real")
		os.MkdirAll(filepath.Join(real, "bin"), 0o755)
		os.Symlink(real, filepath.Join(m.home, "link"))
		m.path = filepath.Join(m.home, "link", "bin") + ":" + m.path
		onPath("PATH through a symlink", m, "SIGDEK_INSTALL_DIR="+filepath.Join(real, "bin"))
		m.path = filepath.Join(real, "bin") + ":" + os.Getenv("PATH")
		onPath("install folder through a symlink", m, "SIGDEK_INSTALL_DIR="+filepath.Join(m.home, "link", "bin"))

		// HOME with a trailing slash, and the default folder on PATH.
		m = newMachine(t, sh, rel)
		m.path = m.defaultDir() + ":" + m.path
		onPath("HOME with a trailing /", m, "HOME="+m.home+"/")
		installed(t, m.defaultDir(), latest, hostTarget())

		// Not on PATH, with HOME ending in /: still shown under $HOME.
		m = newMachine(t, sh, rel)
		if r := m.install("HOME=" + m.home + "/"); !strings.Contains(r.stdout, `  export PATH="$HOME/.local/bin:$PATH"`) {
			t.Errorf("HOME with a trailing /, not on PATH: stdout:\n%s", r.stdout)
		}
	})
}

func TestRefusesTheRootFolder(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)
		for _, d := range []string{"/", "//", "///"} {
			m := newMachine(t, sh, rel)
			refused(t, m.install("SIGDEK_INSTALL_DIR="+d), "SIGDEK_INSTALL_DIR can't be the root folder, /.")
		}
		if n := rel.requests(""); n != 0 {
			t.Errorf("made %d requests before refusing", n)
		}
	})
}

// A script cut short in download, as `curl | sh` runs it when the connection drops, does
// nothing: it prints nothing (in particular, a cut at `set` doesn't list the environment),
// downloads nothing and writes nothing.
func TestCutShortScriptDoesNothing(t *testing.T) {
	full, err := os.ReadFile(script(t))
	if err != nil {
		t.Fatal(err)
	}
	last := bytes.LastIndex(full, []byte("\nmain \"$@\"\n")) + 1
	if last <= 0 || last+len("main \"$@\"\n") != len(full) {
		t.Fatal(`install.sh doesn't end with main "$@"`)
	}
	// Where to cut: after each line, after each letter of each word that starts a top-level
	// line (a cut there leaves that word as a command), and after "set".
	var cuts []int
	for i, b := range full[:last] {
		if b == '\n' {
			cuts = append(cuts, i+1)
		}
	}
	for i := 0; i < last+len("mai"); {
		end := i
		for end < len(full) && (full[end] == '_' || 'a' <= full[end] && full[end] <= 'z' || 'A' <= full[end] && full[end] <= 'Z') {
			end++
			if end <= last+len("mai") {
				cuts = append(cuts, end)
			}
		}
		if nl := bytes.IndexByte(full[i:], '\n'); nl >= 0 {
			i += nl + 1
		} else {
			break
		}
	}
	set := bytes.Index(full, []byte("set -eu"))
	if set < 0 {
		t.Fatal("install.sh has no set -eu")
	}
	cuts = append(cuts, set+len("set"))
	const marker = "SIGDEK_TEST_MARKER=marker-ab5f19"
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)
		m := newMachine(t, sh, rel)
		for _, n := range cuts {
			cmd := exec.Command(sh)
			cmd.Dir = m.home
			cmd.Stdin = bytes.NewReader(full[:n])
			cmd.Env = []string{"HOME=" + m.home, "TMPDIR=" + m.tmp, "PATH=" + m.path, "SHELL=/bin/zsh", "LC_ALL=C", marker, "SIGDEK_RELEASE_BASE=" + rel.base}
			var out, errb strings.Builder
			cmd.Stdout, cmd.Stderr = &out, &errb
			cmd.Run()
			if out.Len() > 0 || strings.Contains(errb.String(), "marker-ab5f19") {
				t.Fatalf("cut after byte %d (%q): stdout %q, stderr %q", n, full[max(0, n-20):n], out.String(), errb.String())
			}
		}
		if n := rel.requests(""); n != 0 {
			t.Errorf("a cut-short script made %d requests", n)
		}
		if left, _ := os.ReadDir(m.home); len(left) > 0 {
			t.Errorf("a cut-short script left %s in HOME", left[0].Name())
		}
		if left, _ := os.ReadDir(m.tmp); len(left) > 0 {
			t.Errorf("a cut-short script left %s in TMPDIR", left[0].Name())
		}
	})
}
