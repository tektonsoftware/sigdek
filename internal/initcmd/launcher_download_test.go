package initcmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// RR-8: on a link too slow for the hook's 8-second download, launch.sh hands the download to a
// background process that outlives the hook, and the next event after it lands records;
// launch.ps1 keeps what arrived and resumes it on the next events, with no background process.

func TestMain(m *testing.M) {
	// On Windows the launchers need a real .exe as the recorder; this test binary is that recorder
	// (the same as fakeRecorder's script) when the launcher runs it with this variable set.
	switch os.Getenv("SIGDEK_TEST_RECORDER") {
	case "1":
		fmt.Println("recorder ran: " + strings.Join(os.Args[1:], " "))
		io.Copy(os.Stdout, os.Stdin)
		os.Exit(0)
	case "answer":
		// A recorder that keeps its input in SIGDEK_TEST_STDIN and answers with a denial naming
		// its arguments, as a Block rule would.
		in, _ := io.ReadAll(os.Stdin)
		os.WriteFile(os.Getenv("SIGDEK_TEST_STDIN"), in, 0o644)
		fmt.Printf("{\"permission\":\"deny\",\"user_message\":\"recorder ran: %s\"}\n", strings.Join(os.Args[1:], " "))
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// slowServer serves one file at a fixed rate in bytes a second, like a slow link. It answers
// "Range: bytes=N-" with 206 or 416, as GitHub's release downloads do, unless told to ignore it,
// and keeps each request's Range header.
type slowServer struct {
	*httptest.Server
	hits        atomic.Int32
	rate        atomic.Int64
	ignoreRange atomic.Bool
	mu          sync.Mutex
	ranges      []string
}

var rangeHeader = regexp.MustCompile(`^bytes=(\d+)-$`)

func newSlowServer(t *testing.T, name string, body []byte, rate int) *slowServer {
	s := &slowServer{}
	s.rate.Store(int64(rate))
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+name {
			w.WriteHeader(404)
			return
		}
		s.hits.Add(1)
		s.mu.Lock()
		s.ranges = append(s.ranges, r.Header.Get("Range"))
		s.mu.Unlock()
		from, status := 0, 200
		if m := rangeHeader.FindStringSubmatch(r.Header.Get("Range")); m != nil && !s.ignoreRange.Load() {
			from, _ = strconv.Atoi(m[1])
			if from >= len(body) {
				w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", len(body)))
				w.WriteHeader(416)
				return
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(body)-1, len(body)))
			status = 206
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(body)-from))
		w.WriteHeader(status)
		rate := int(s.rate.Load())
		start, chunk := time.Now(), max(1024, rate/20)
		for sent := 0; from+sent < len(body); {
			n := min(chunk, len(body)-from-sent)
			if _, err := w.Write(body[from+sent : from+sent+n]); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			sent += n
			if d := time.Duration(float64(sent)/float64(rate)*float64(time.Second)) - time.Since(start); d > 0 {
				select {
				case <-time.After(d):
				case <-r.Context().Done():
					return
				}
			}
		}
	}))
	t.Cleanup(func() { s.CloseClientConnections(); s.Close() })
	return s
}

func (s *slowServer) rangesSeen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.ranges...)
}

// slowSeconds is how long the whole file takes at the slow server's rate: well past the hook's
// 8 s, so a hook that waits for the download, or isn't limited, takes far longer than hookLimit.
const slowSeconds = 14

// download is one launcher's first use of a pinned recorder, against a slow server.
type download struct {
	t     *testing.T
	dir   string // the plugin's scripts folder: launchers and recorder.lock
	cache string // the version's cache folder
	bin   string // the recorder, once it lands
	want  string // its SHA-256, as pinned
	srv   *slowServer
	env   []string
	run   func(stdin string, args ...string) (stdout, stderr string, code int, took time.Duration)
}

// recorderFor gives a recorder this machine can run: a shell script padded to 2 MB on macOS and
// Linux (exit 0 stops sh before the padding), or this test binary on Windows. Served in
// slowSeconds, the script goes at about 1.1 Mbit/s.
func recorderFor(t *testing.T) (body []byte, env []string) {
	if runtime.GOOS != "windows" {
		script := "#!/bin/sh\necho \"recorder ran: $*\"\ncat\nexit 0\n"
		return []byte(script + strings.Repeat("#", 2_000_000)), nil
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	body, err = os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	return body, []string{"SIGDEK_TEST_RECORDER=1"}
}

// cleanEnv is this process's environment without SigDek's and the agent tools' variables:
// Windows programs need SYSTEMROOT and the like, which the sh tests' minimal environment lacks.
func cleanEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		k = strings.ToUpper(k)
		if strings.HasPrefix(k, "SIGDEK_") || strings.HasPrefix(k, "CLAUDE_") || strings.HasPrefix(k, "CURSOR_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

func runHook(t *testing.T, name string, args []string, env []string, stdin string) (string, string, int, time.Duration) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb // pipes: Wait returns only once nothing holds them open
	start := time.Now()
	err := cmd.Run()
	took := time.Since(start)
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return out.String(), errb.String(), code, took
}

// gitBash is the sh Claude Code and Cursor use on Windows.
func gitBash(t *testing.T) string {
	if p := `C:\Program Files\Git\bin\sh.exe`; fileExists(p) {
		return p
	}
	if p, err := exec.LookPath("sh"); err == nil {
		return p
	}
	t.Skip("Git Bash isn't installed")
	return ""
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// newDownload sets up a launcher (launch.sh run by shell, or launch.ps1 when shell is
// "powershell") pinned to body's checksum, or to want if it isn't empty, with its release on a
// slow server.
func newDownload(t *testing.T, shell string, body []byte, want string, recEnv []string) *download {
	osName, ext := runtime.GOOS, ""
	if osName == "windows" {
		ext = ".exe"
	}
	if want == "" {
		sum := sha256.Sum256(body)
		want = hex.EncodeToString(sum[:])
	}
	name := fmt.Sprintf("sigdek_7.0.0_%s_%s%s", osName, runtime.GOARCH, ext)
	srv := newSlowServer(t, name, body, len(body)/slowSeconds)
	dir := filepath.Join(t.TempDir(), "plugin", "scripts")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "launch.sh"), launchSh, 0o644)
	os.WriteFile(filepath.Join(dir, "launch.ps1"), launchPs1, 0o644)
	os.WriteFile(filepath.Join(dir, "recorder.lock"), []byte(fmt.Sprintf("version=7.0.0\nurl=%s\n%s_%s=%s\n", srv.URL, osName, runtime.GOARCH, want)), 0o644)
	cache := t.TempDir()
	d := &download{t: t, dir: dir, srv: srv, want: want, cache: filepath.Join(cache, "7.0.0"), bin: filepath.Join(cache, "7.0.0", "sigdek"+ext)}
	// Let a background download finish before the temporary folders go (Windows can't remove a
	// file another process has open).
	t.Cleanup(func() { d.settled(3 * slowSeconds * time.Second) })
	d.env = cleanEnv(append([]string{"SIGDEK_CACHE=" + filepath.ToSlash(cache), "SIGDEK_HOME=" + filepath.ToSlash(t.TempDir())}, recEnv...)...)
	d.run = func(stdin string, args ...string) (string, string, int, time.Duration) {
		if shell == "powershell" {
			return runHook(t, "powershell", append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", filepath.Join(dir, "launch.ps1")}, args...), d.env, stdin)
		}
		// As the agents run it: `sh -c '<hook command>'`; Cursor's goes through `| grep`, which
		// waits for everything that holds the launcher's stdout.
		launcher := filepath.ToSlash(filepath.Join(dir, "launch.sh"))
		command := fmt.Sprintf("sh %q %s; exit 0", launcher, strings.Join(args, " "))
		if len(args) > 2 && args[1] == "cursor" {
			command = fmt.Sprintf("sh %q %s | grep '^{' || echo '%s'", launcher, strings.Join(args, " "), strings.TrimSpace(cursorSafe(args[2])))
		}
		return runHook(t, shell, []string{"-c", command}, d.env, stdin)
	}
	return d
}

// retarget points the lock at another release server.
func (d *download) retarget(url string) {
	p := filepath.Join(d.dir, "recorder.lock")
	b, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(b), "url="+d.srv.URL, "url="+url, 1)), 0o644)
}

func cursorSafe(event string) string {
	switch event {
	case "preToolUse", "subagentStart", "beforeShellExecution", "beforeMCPExecution", "beforeReadFile":
		return `{"permission":"allow"}`
	case "beforeSubmitPrompt":
		return `{"continue":true}`
	}
	return "{}"
}

// settled waits for the background download to finish: the marker gone, and no temporary file.
func (d *download) settled(limit time.Duration) bool {
	for end := time.Now().Add(limit); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
		if !fileExists(filepath.Join(d.cache, ".downloading")) && len(d.temps()) == 0 {
			return true
		}
	}
	return false
}

func (d *download) temps() []string {
	m, _ := filepath.Glob(filepath.Join(d.cache, "*.download"))
	return m
}

// hookLimit is the most an event on a slow link may take: Claude Code and Cursor give a hook 10
// seconds. launch.sh's curl has 8 s (6 s on Windows, where Git Bash's own process starts have
// taken up to 1.9 s on a loaded CI runner); launch.ps1 stops 7 s after it started.
const hookLimit = 9500 * time.Millisecond

// shortest is the least a slow first use of launch.sh takes: curl's whole limit.
var shortest = map[bool]time.Duration{false: 7500 * time.Millisecond, true: 5500 * time.Millisecond}[runtime.GOOS == "windows"]

// shForms are the shells to run launch.sh with: every POSIX shell installed, or Git Bash's sh
// on Windows.
func shForms(t *testing.T) []string {
	if runtime.GOOS == "windows" {
		return []string{gitBash(t)}
	}
	if _, err := exec.LookPath("curl"); err != nil {
		t.Skip("curl not installed")
	}
	return shells(t)
}

// launcherForms adds Windows PowerShell's launch.ps1 on Windows.
func launcherForms(t *testing.T) []string {
	if runtime.GOOS == "windows" {
		return append(shForms(t), "powershell")
	}
	return shForms(t)
}

func label(shell string) string { return strings.TrimSuffix(filepath.Base(shell), ".exe") }

func TestLauncherFinishesASlowDownloadInTheBackground(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about 20 seconds")
	}
	t.Parallel()
	body, recEnv := recorderFor(t)
	for _, sh := range shForms(t) {
		t.Run(label(sh), func(t *testing.T) {
			t.Parallel()
			d := newDownload(t, sh, body, "", recEnv)

			// First use: the hook gives up on the download within its limit and answers as usual.
			out, errOut, code, took := d.run("{}", "hook", "cursor", "preToolUse")
			t.Logf("first hook: %v", took)
			if code != 0 || out != "{\"permission\":\"allow\"}\n" || took > hookLimit || took < shortest {
				t.Fatalf("first hook: exit %d after %v, stdout %q, stderr %q", code, took, out, errOut)
			}
			if fileExists(d.bin) {
				t.Fatal("the download should not have fitted in the hook")
			}
			landing := time.Now()

			// During the background download, another event leaves the network to it.
			out, errOut, code, took = d.run(`{"hook_event_name":"Stop"}`, "hook", "claude-code")
			if code != 0 || out != "" || took > 4*time.Second || fileExists(d.bin) {
				t.Fatalf("second hook: exit %d after %v, stdout %q, stderr %q", code, took, out, errOut)
			}

			if !d.settled(3 * slowSeconds * time.Second) {
				t.Fatalf("the background download didn't finish: %v", d.temps())
			}
			t.Logf("landed %v after the first hook returned", time.Since(landing).Round(100*time.Millisecond))
			if !fileExists(d.bin) || fileExists(filepath.Join(d.cache, ".download-failed")) {
				entries, _ := os.ReadDir(d.cache)
				t.Fatalf("not cached: %v", entries)
			}
			if got, _ := os.ReadFile(d.bin); !bytes.Equal(got, body) {
				t.Fatal("the cached recorder isn't the file served")
			}

			// The next event runs the recorder, from the cache.
			out, _, code, _ = d.run(`{"hook_event_name":"Stop"}`, "hook", "claude-code")
			if code != 0 || !strings.Contains(out, "recorder ran: hook claude-code") || !strings.Contains(out, `"hook_event_name":"Stop"`) {
				t.Fatalf("third hook: exit %d, stdout %q", code, out)
			}
			if n := d.srv.hits.Load(); n != 2 {
				t.Fatalf("%d downloads, want 2: the hook's and one in the background", n)
			}
		})
	}
}

func TestLauncherRefusesATamperedSlowDownload(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about 20 seconds")
	}
	t.Parallel()
	body, recEnv := recorderFor(t)
	good := sha256.Sum256(body)
	bad := append(bytes.Clone(body), 'x')
	for _, sh := range shForms(t) {
		t.Run(label(sh), func(t *testing.T) {
			t.Parallel()
			d := newDownload(t, sh, bad, hex.EncodeToString(good[:]), recEnv)
			out, errOut, code, took := d.run("{}", "hook", "claude-code")
			if code != 0 || out != "" || took > hookLimit {
				t.Fatalf("first hook: exit %d after %v, stdout %q, stderr %q", code, took, out, errOut)
			}
			if !d.settled(3 * slowSeconds * time.Second) {
				t.Fatalf("the background download didn't finish: %v", d.temps())
			}
			if fileExists(d.bin) {
				t.Fatal("a recorder with the wrong checksum was cached")
			}
			stamp, _ := os.ReadFile(filepath.Join(d.cache, ".download-failed"))
			if strings.TrimSpace(string(stamp)) != "1 "+d.want {
				t.Fatalf(".download-failed is %q; the background failure must count once", stamp)
			}
			// The failure backs off as before: the next event doesn't download.
			out, _, code, _ = d.run("{}", "hook", "cursor", "preToolUse")
			if code != 0 || out != "{\"permission\":\"allow\"}\n" || d.srv.hits.Load() != 2 {
				t.Fatalf("after the failure: exit %d, stdout %q, %d downloads", code, out, d.srv.hits.Load())
			}
		})
	}
}

// Two events at once on a slow link: each hook tries, and one download carries on.
func TestLauncherStartsOneBackgroundDownload(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about 20 seconds")
	}
	t.Parallel()
	body, recEnv := recorderFor(t)
	for _, sh := range shForms(t)[:1] {
		d := newDownload(t, sh, body, "", recEnv)
		var wg sync.WaitGroup
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				out, errOut, code, took := d.run("{}", "hook", "claude-code")
				t.Logf("hook: %v", took)
				if code != 0 || out != "" || took > hookLimit {
					t.Errorf("hook: exit %d after %v, stdout %q, stderr %q", code, took, out, errOut)
				}
			}()
		}
		wg.Wait()
		if !d.settled(3*slowSeconds*time.Second) || !fileExists(d.bin) {
			t.Fatalf("not cached: %v", d.temps())
		}
		if n := d.srv.hits.Load(); n != 3 {
			t.Fatalf("%d downloads, want 3: each hook's and one in the background", n)
		}
	}
}

// The marker of a background download keeps other events off the network while it's fresh, and
// stops counting after 6 minutes (a download that was killed).
func TestLauncherDownloadMarker(t *testing.T) {
	body, recEnv := recorderFor(t)
	for _, sh := range launcherForms(t) {
		t.Run(label(sh), func(t *testing.T) {
			d := newDownload(t, sh, body, "", recEnv)
			fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { d.srv.hits.Add(1); w.Write(body) }))
			defer fast.Close()
			d.retarget(fast.URL)
			os.MkdirAll(d.cache, 0o755)
			mark := filepath.Join(d.cache, ".downloading")
			os.WriteFile(mark, nil, 0o644)
			if out, _, code, _ := d.run("{}", "hook", "cursor", "preToolUse"); code != 0 || out != "{\"permission\":\"allow\"}\n" || d.srv.hits.Load() != 0 {
				t.Fatalf("fresh marker: exit %d, stdout %q, %d downloads", code, out, d.srv.hits.Load())
			}
			old := time.Now().Add(-10 * time.Minute)
			os.Chtimes(mark, old, old)
			if out, _, code, _ := d.run("{}", "hook", "claude-code"); code != 0 || !strings.Contains(out, "recorder ran") {
				t.Fatalf("stale marker: exit %d, stdout %q", code, out)
			}
			os.Remove(mark) // launch.sh replaces a stale marker only when it starts a background download
		})
	}
}

// launch.ps1 starts no other process: it downloads in the hook until 7 s after it started, keeps
// what arrived in a partial file, and resumes it with a Range request on the next event.

func ps1(t *testing.T) {
	if testing.Short() {
		t.Skip("takes about 30 seconds")
	}
	if runtime.GOOS != "windows" {
		t.Skip("launch.ps1 runs on Windows")
	}
	if _, err := exec.LookPath("powershell"); err != nil {
		t.Skip("Windows PowerShell isn't installed")
	}
}

func (d *download) partial() string {
	return filepath.Join(d.cache, "sigdek.exe."+d.want[:16]+".partial")
}

func size(p string) int64 {
	st, err := os.Stat(p)
	if err != nil {
		return -1
	}
	return st.Size()
}

// untilRecorded runs Claude Code events until one runs the recorder, checking that every event
// stays within the hook limit and that each one before it adds to the partial file.
func (d *download) untilRecorded(t *testing.T, max int) (events int) {
	t.Helper()
	before := size(d.partial())
	for events = 1; events <= max; events++ {
		out, errOut, code, took := d.run(`{"hook_event_name":"Stop"}`, "hook", "claude-code")
		t.Logf("event: %v, partial %d bytes", took.Round(10*time.Millisecond), size(d.partial()))
		if code != 0 || took > hookLimit {
			t.Fatalf("event %d: exit %d after %v, stdout %q, stderr %q", events, code, took, out, errOut)
		}
		if strings.Contains(out, "recorder ran: hook claude-code") {
			if !strings.Contains(out, `"hook_event_name":"Stop"`) {
				t.Fatalf("the recorder didn't get the event: %q", out)
			}
			return events
		}
		if out != "" {
			t.Fatalf("event %d printed %q", events, out)
		}
		if now := size(d.partial()); now <= before {
			t.Fatalf("event %d added nothing: %d -> %d bytes", events, before, now)
		} else {
			before = now
		}
	}
	t.Fatalf("the recorder didn't run in %d events", max)
	return 0
}

func (d *download) checkCached(t *testing.T, body []byte) {
	t.Helper()
	if got, _ := os.ReadFile(d.bin); !bytes.Equal(got, body) {
		t.Fatal("the cached recorder isn't the file served")
	}
	if fileExists(d.partial()) || fileExists(filepath.Join(d.cache, ".download-failed")) {
		entries, _ := os.ReadDir(d.cache)
		t.Fatalf("left behind: %v", entries)
	}
}

func TestLaunchPs1ResumesASlowDownload(t *testing.T) {
	ps1(t)
	t.Parallel()
	body, recEnv := recorderFor(t)
	d := newDownload(t, "powershell", body, "", recEnv)

	// First use: the hook stops within its limit, answers as usual and keeps what arrived.
	out, errOut, code, took := d.run("{}", "hook", "cursor", "preToolUse")
	t.Logf("first event: %v, partial %d of %d bytes", took.Round(10*time.Millisecond), size(d.partial()), len(body))
	if code != 0 || out != "{\"permission\":\"allow\"}\n" || took > hookLimit {
		t.Fatalf("first event: exit %d after %v, stdout %q, stderr %q", code, took, out, errOut)
	}
	if size(d.partial()) <= 0 || fileExists(d.bin) {
		t.Fatalf("after the first event: partial %d bytes, cached %v", size(d.partial()), fileExists(d.bin))
	}

	// The next events add to it, and the one that completes it runs the recorder.
	events := 1 + d.untilRecorded(t, 6)
	t.Logf("the recorder ran in event %d", events)
	d.checkCached(t, body)
	ranges := d.srv.rangesSeen()
	if len(ranges) != events || ranges[0] != "" {
		t.Fatalf("requests' Range headers: %q", ranges)
	}
	for _, r := range ranges[1:] {
		if !rangeHeader.MatchString(r) {
			t.Fatalf("a later event asked for the whole file again: %q", ranges)
		}
	}
}

func TestLaunchPs1RefusesATamperedDownload(t *testing.T) {
	ps1(t)
	t.Parallel()
	body, recEnv := recorderFor(t)
	good := sha256.Sum256(body)
	d := newDownload(t, "powershell", append(bytes.Clone(body), 'x'), hex.EncodeToString(good[:]), recEnv)
	stamp := filepath.Join(d.cache, ".download-failed")
	for i := 1; !fileExists(stamp); i++ {
		if i > 6 {
			t.Fatal("not refused in 6 events")
		}
		out, errOut, code, took := d.run("{}", "hook", "cursor", "preToolUse")
		if code != 0 || out != "{\"permission\":\"allow\"}\n" || took > hookLimit {
			t.Fatalf("event %d: exit %d after %v, stdout %q, stderr %q", i, code, took, out, errOut)
		}
	}
	if fileExists(d.bin) || fileExists(d.partial()) {
		t.Fatal("a recorder with the wrong checksum was kept")
	}
	if b, _ := os.ReadFile(stamp); strings.TrimSpace(string(b)) != "1 "+d.want {
		t.Fatalf(".download-failed is %q", b)
	}
	// The failure backs off as before: the next event doesn't download.
	hits := d.srv.hits.Load()
	if out, _, code, _ := d.run("{}", "hook", "claude-code"); code != 0 || out != "" || d.srv.hits.Load() != hits {
		t.Fatalf("after the refusal: exit %d, stdout %q, %d downloads", code, out, d.srv.hits.Load())
	}
}

// A server (or proxy) that ignores Range sends the whole file again: launch.ps1 starts over.
func TestLaunchPs1StartsOverWhenTheServerIgnoresRange(t *testing.T) {
	ps1(t)
	t.Parallel()
	body, recEnv := recorderFor(t)
	d := newDownload(t, "powershell", body, "", recEnv)
	d.srv.ignoreRange.Store(true)
	d.srv.rate.Store(int64(len(body) / 3)) // fits in one event
	os.MkdirAll(d.cache, 0o755)
	os.WriteFile(d.partial(), bytes.Repeat([]byte{'j'}, 1000), 0o644)
	out, errOut, code, _ := d.run(`{"hook_event_name":"Stop"}`, "hook", "claude-code")
	if code != 0 || !strings.Contains(out, "recorder ran: hook claude-code") {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	d.checkCached(t, body)
	if r := d.srv.rangesSeen(); len(r) != 1 || r[0] != "bytes=1000-" {
		t.Fatalf("requests' Range headers: %q", r)
	}
}

// Two events at once: one appends, the other leaves it alone, and the file comes out whole.
func TestLaunchPs1AppendsOneEventAtATime(t *testing.T) {
	ps1(t)
	t.Parallel()
	body, recEnv := recorderFor(t)
	d := newDownload(t, "powershell", body, "", recEnv)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, errOut, code, took := d.run("{}", "hook", "cursor", "preToolUse")
			t.Logf("event at once: %v", took.Round(10*time.Millisecond))
			if code != 0 || out != "{\"permission\":\"allow\"}\n" || took > hookLimit {
				t.Errorf("exit %d after %v, stdout %q, stderr %q", code, took, out, errOut)
			}
		}()
	}
	wg.Wait()
	if n := d.srv.hits.Load(); n != 1 {
		t.Fatalf("%d downloads at once, want 1", n)
	}
	d.untilRecorded(t, 6)
	d.checkCached(t, body)
}
