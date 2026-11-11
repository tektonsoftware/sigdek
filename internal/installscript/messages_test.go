package installscript

// What install.sh says when it can't download: the HTTP status of /latest itself, the reason
// curl or wget gave, and a certificate that couldn't be verified.

import (
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// An HTTP error from /latest itself is a failed try: tried again, and named at the end. One
// from the page /latest redirects to isn't, since the redirect already named the version.
func TestLatestAnsweringWithAnError(t *testing.T) {
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)
		for _, status := range []int{503, 429, 404} {
			before := rel.requests("/latest")
			rel.answer("/latest", status, status, status)
			m := newMachine(t, sh, rel)
			r := m.install()
			refused(t, r, fmt.Sprintf("couldn't find the latest version: %s/latest answered with HTTP status %d, 3 times. Run this again in a few minutes.", rel.base, status))
			if n := rel.requests("/latest") - before; n != 3 {
				t.Errorf("%d: /latest asked for %d times, want 3", status, n)
			}
			if n := strings.Count(r.stderr, "Couldn't download "+rel.base+fmt.Sprintf("/latest (HTTP status %d). Trying again.", status)); n != 2 {
				t.Errorf("%d: %d tries again, want 2: %q", status, n, r.stderr)
			}
			if strings.Contains(r.stderr, "SIGDEK_VERSION") {
				t.Errorf("%d: an error at /latest sends people to pin a version: %q", status, r.stderr)
			}
			notCreated(t, m.defaultDir())
		}

		// Once: the next try finds it.
		rel.answer("/latest", 503)
		m := newMachine(t, sh, rel)
		if r := m.install(); r.code != 0 || !strings.Contains(r.stderr, "/latest (HTTP status 503). Trying again.") {
			t.Fatalf("a 503 once: exit %d, stderr %q", r.code, r.stderr)
		}
		installed(t, m.defaultDir(), latest, hostTarget())

		// The release page that /latest redirects to fails: the redirect still named the version.
		before := rel.requests("/latest")
		rel.answer("/tag/v"+latest, 503, 503, 503)
		m = newMachine(t, sh, rel)
		if r := m.install(); r.code != 0 || r.stderr != "" {
			t.Fatalf("the release page answers 503: exit %d, stderr %q", r.code, r.stderr)
		}
		installed(t, m.defaultDir(), latest, hostTarget())
		if n := rel.requests("/latest") - before; n != 1 {
			t.Errorf("the release page answering 503 made %d requests for /latest, want 1", n)
		}
	})
}

// A server certificate that can't be verified ends the install at once, without trying again,
// says that checking must stay on, and never passes on wget's advice to turn it off.
func TestUnverifiedCertificate(t *testing.T) {
	tools := map[string]string{"curl": ""}
	if _, err := exec.LookPath("wget"); err == nil {
		tools["wget"] = toolsWithout(t, "curl")
	}
	eachShell(t, func(t *testing.T, sh string) {
		var conns atomic.Int32
		srv := httptest.NewUnstartedServer(http.NotFoundHandler())
		srv.Config.ErrorLog = log.New(io.Discard, "", 0) // the handshake errors are the point
		srv.Config.ConnState = func(_ net.Conn, s http.ConnState) {
			if s == http.StateNew {
				conns.Add(1)
			}
		}
		srv.StartTLS() // with a certificate nothing here trusts
		t.Cleanup(srv.Close)
		rel := newRelease(t, latest)
		base := srv.URL + "/releases"
		for tool, path := range tools {
			for _, env := range [][]string{nil, {"SIGDEK_VERSION=" + latest}} {
				m := newMachine(t, sh, rel)
				if path != "" {
					m.path = path
				}
				before := conns.Load()
				r := m.install(append([]string{"SIGDEK_RELEASE_BASE=" + base}, env...)...)
				url := base + "/latest"
				if env != nil {
					url = base + "/download/v" + latest + "/checksums.txt"
				}
				advice := "Certificate checking must stay on: fix the system's certificates, then run this again."
				if tool == "wget" {
					advice = "Certificate checking must stay on: use curl, or fix the system's certificates, then run this again."
				}
				refused(t, r, "the certificate of the server for "+url+" couldn't be verified (")
				if !strings.Contains(r.stderr, "), so nothing was installed. "+advice) {
					t.Errorf("%s %v: stderr %q", tool, env, r.stderr)
				}
				for _, bad := range []string{"Trying again", "no-check-certificate", "insecure"} {
					if strings.Contains(r.stderr, bad) {
						t.Errorf("%s %v: stderr has %q: %q", tool, env, bad, r.stderr)
					}
				}
				if n := conns.Load() - before; n != 1 {
					t.Errorf("%s %v: %d connections, want 1 (no retry)", tool, env, n)
				}
				notCreated(t, m.defaultDir())
			}
		}
	})
}

// fakeTool is a folder holding a stand-in for curl or wget that writes stderr and exits with
// code, counting its runs in the file "runs" beside it. The wget one says it's GNU Wget.
func fakeTool(t *testing.T, name, stderr string, code int) string {
	t.Helper()
	dir := realTempDir(t)
	script := "#!/bin/sh\n"
	if name == "wget" {
		script += `case "$1" in --version) echo "GNU Wget 1.21.4 built on linux-gnu."; exit 0 ;; --help) echo "       --hsts-file=FILENAME"; exit 0 ;; esac` + "\n"
	}
	script += fmt.Sprintf("echo run >>\"${0%%/*}/runs\"\ncat \"${0%%/*}/stderr\" >&2\nexit %d\n", code)
	os.WriteFile(filepath.Join(dir, "stderr"), []byte(stderr), 0o644)
	os.WriteFile(filepath.Join(dir, name), []byte(script), 0o755)
	return dir
}

// The reason given for a failed download is the one curl or wget gave: curl's error line, or
// wget's last error after its last successful connection (not a failed try at another
// address), without its timestamp, and never its advice to turn certificate checking off.
func TestFailureReasons(t *testing.T) {
	plain := toolsWithout(t, "curl", "wget")
	const ipv6Drop = `--2026-11-10 19:56:13--  http://localhost:18532/x
Resolving localhost (localhost)... ::1, 127.0.0.1
Connecting to localhost (localhost)|::1|:18532... failed: Connection refused.
Connecting to localhost (localhost)|127.0.0.1|:18532... connected.
HTTP request sent, awaiting response... 200 OK
Length: 7146386 (6.8M)
Saving to: 'x'

     0K                                      2% 2.36G=0s

2026-11-10 19:56:13 (2.36 GB/s) - Connection closed at byte 200000. Giving up.

`
	const wgetCert = `--2026-11-10 19:56:13--  https://127.0.0.1:18533/x
Connecting to 127.0.0.1:18533... connected.
ERROR: cannot verify 127.0.0.1's certificate, issued by 'CN=127.0.0.1':
  Unable to locally verify the issuer's authority.
To connect to 127.0.0.1 insecurely, use ` + "`--no-check-certificate'." + `
`
	const curlCert = `curl: (60) SSL certificate problem: self signed certificate
More details here: https://curl.se/docs/sslcerts.html

curl failed to verify the legitimacy of the server and therefore could not
establish a secure connection to it. To learn more about this situation and
how to fix it, please visit the web page mentioned above.
`
	cases := []struct {
		name, tool, stderr string
		code, runs         int
		want               string
	}{
		{"wget drop after an IPv6 try", "wget", ipv6Drop, 4, 3, "failed (Connection closed at byte 200000. Giving up.), so nothing was installed. Check the network connection"},
		{"wget 404", "wget", "http://x/checksums.txt:\n2026-11-10 19:12:40 ERROR 404: Not Found.\n", 8, 3, "(ERROR 404: Not Found.). Check that v" + latest + " is a SigDek release"},
		{"wget silent", "wget", "", 4, 3, "failed (wget: exit 4), so nothing was installed."},
		{"wget only its advice", "wget", "To connect to x insecurely, use `--no-check-certificate'.\n", 4, 3, "failed (wget: exit 4), so nothing was installed."},
		{"wget certificate", "wget", wgetCert, 5, 1, "couldn't be verified (ERROR: cannot verify 127.0.0.1's certificate, issued by 'CN=127.0.0.1':), so nothing was installed. Certificate checking must stay on: use curl, or"},
		{"curl certificate", "curl", curlCert, 60, 1, "couldn't be verified (curl: (60) SSL certificate problem: self signed certificate), so nothing was installed. Certificate checking must stay on: fix the system's certificates"},
		{"curl handshake", "curl", "curl: (35) LibreSSL SSL_connect: SSL_ERROR_SYSCALL in connection to x:443\n", 35, 1, "couldn't be verified (curl: (35) LibreSSL SSL_connect: SSL_ERROR_SYSCALL in connection to x:443)"},
		{"curl silent", "curl", "", 7, 3, "failed (curl: exit 7), so nothing was installed."},
	}
	eachShell(t, func(t *testing.T, sh string) {
		rel := newRelease(t, latest)
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				t.Parallel()
				fake := fakeTool(t, c.tool, c.stderr, c.code)
				m := newMachine(t, sh, rel)
				m.path = fake + ":" + plain
				r := m.install("SIGDEK_VERSION=" + latest)
				refused(t, r, c.want)
				for _, bad := range []string{"Connection refused", "no-check-certificate", "insecurely", "2026-11-10"} {
					if strings.Contains(r.stderr, bad) {
						t.Errorf("stderr has %q: %q", bad, r.stderr)
					}
				}
				runs, _ := os.ReadFile(filepath.Join(fake, "runs"))
				if n := strings.Count(string(runs), "run"); n != c.runs {
					t.Errorf("%s ran %d times, want %d", c.tool, n, c.runs)
				}
				if n := strings.Count(r.stderr, "Trying again."); n != c.runs-1 {
					t.Errorf("%d tries again, want %d: %q", n, c.runs-1, r.stderr)
				}
			})
		}
	})
}
