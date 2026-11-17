package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/store"
)

// BB-NET-1 (replacing the status half of CONTRACTS2 3): a 200 that isn't ingest's answer, or a
// redirect, isn't an upload. flush says so and fails, the records stay queued, status doesn't
// say uploads work, and they all go up once ingest answers.
func TestFlushAndStatusWhenSomethingElseAnswers(t *testing.T) {
	var mu sync.Mutex // the binary's requests reach the handler on its own goroutine
	var mode string
	var got []int
	set := func(m string) { mu.Lock(); mode = m; mu.Unlock() }
	ingested := func() string { mu.Lock(); defer mu.Unlock(); return fmt.Sprint(got) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch mode {
		case "html":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html>Caution: this site is new</html>"))
		case "redirect":
			http.Redirect(w, r, "/landing", http.StatusTemporaryRedirect)
		default:
			var req struct {
				Records []json.RawMessage `json:"records"`
			}
			json.NewDecoder(r.Body).Decode(&req)
			got = append(got, len(req.Records))
			w.Write([]byte(`{"accepted":` + fmt.Sprint(len(req.Records)) + `}`))
		}
	}))
	defer srv.Close()
	home, dir := sigdekHome(t), repoDir(t)
	config.SaveDevice(home, config.Device{ID: "dev_net", Token: "tok", User: "dev@example.com", OrgName: "Tekton", IngestURL: srv.URL})
	// No background upload from the hooks: this test runs each one by hand.
	os.MkdirAll(config.StateDir(home), 0o700)
	os.WriteFile(filepath.Join(config.StateDir(home), "flush.stamp"), nil, 0o600)
	set("html")
	for i := 0; i < 3; i++ {
		runHook(t, home, []string{"claude-code"}, cc(dir, "PostToolUse", `,"tool_name":"Bash","tool_input":{"command":"ls"}`))
	}
	waitForUploader(t, home)
	cli := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Env = append(os.Environ(), "SIGDEK_HOME="+home)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	for m, says := range map[string]string{
		"html":     "wasn't SigDek's; a proxy or captive portal may be in the way",
		"redirect": "the server answered with a redirect to " + srv.URL + "/landing",
	} {
		set(m)
		out, err := cli("flush")
		if err == nil || !strings.Contains(out, says) || strings.Contains(out, "Uploaded") {
			t.Errorf("%s: flush (%v):\n%s", m, err, out)
		}
		if seq := store.New(home).LoadCursor().Seq; seq != 0 {
			t.Errorf("%s: the cursor moved to %d", m, seq)
		}
		if out, _ := cli("status"); strings.Contains(out, "Uploads work") || !strings.Contains(out, says) {
			t.Errorf("%s: status:\n%s", m, out)
		}
	}
	if g := ingested(); g != "[]" {
		t.Fatalf("ingest got %v before it answered", g)
	}
	set("")
	if out, err := cli("flush"); err != nil || !strings.Contains(out, "Uploaded 3 records") || ingested() != "[3]" {
		t.Fatalf("flush (%v): %s, ingest got %v", err, out, ingested())
	}
	if out, _ := cli("status"); !strings.Contains(out, "[x] Uploads work") {
		t.Errorf("status after a good upload:\n%s", out)
	}
}
