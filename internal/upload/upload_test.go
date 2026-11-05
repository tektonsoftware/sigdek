package upload

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/event"
	"github.com/tektonsoftware/sigdek/internal/lockfile"
	"github.com/tektonsoftware/sigdek/internal/rules"
	"github.com/tektonsoftware/sigdek/internal/store"
)

func setup(t *testing.T, n int, url string) string {
	t.Helper()
	home := t.TempDir()
	config.SaveDevice(home, config.Device{ID: "dev_up", Token: "tok_secret", IngestURL: url})
	st := store.New(home)
	for i := 0; i < n; i++ {
		now := time.Now()
		st.Append(&event.Record{V: 1, ID: event.NewID(now), TS: event.Timestamp(now), Device: "dev_up", Tool: "claude-code",
			Session: "s", Repo: event.Repo{RootHash: strings.Repeat("a", 64)}, Event: "session_start"})
	}
	return home
}

type fakeIngest struct {
	mu      sync.Mutex
	batches [][]json.RawMessage
	status  int
	rules   *rules.Set
}

func (f *fakeIngest) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/v1/events" || r.Header.Get("Authorization") != "Bearer tok_secret" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if f.status != 0 {
		w.WriteHeader(f.status)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Device  string            `json:"device"`
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(body, &req); err != nil || req.Device != "dev_up" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	f.batches = append(f.batches, req.Records)
	json.NewEncoder(w).Encode(Response{Accepted: len(req.Records), Rules: f.rules})
}

func TestFlushSendsEverythingOnceInBatches(t *testing.T) {
	ing := &fakeIngest{rules: &rules.Set{Version: 4, Rules: []rules.Rule{{ID: "team-rule", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "x"}}}}
	srv := httptest.NewServer(ing)
	defer srv.Close()
	home := setup(t, BatchRecords+20, srv.URL)

	sum, err := Flush(home, "test", nil)
	if err != nil || sum.Sent != BatchRecords+20 || sum.Batches != 2 || sum.Accepted != sum.Sent {
		t.Fatalf("sum %+v err %v", sum, err)
	}
	var seqs []int64
	for _, b := range ing.batches {
		for _, raw := range b {
			var r event.Record
			json.Unmarshal(raw, &r)
			seqs = append(seqs, r.Seq)
			if h, _ := event.HashJSON(raw); h != r.Hash {
				t.Fatalf("record %d altered in transit", r.Seq)
			}
		}
	}
	for i, s := range seqs {
		if s != int64(i+1) {
			t.Fatalf("out of order at %d: %d", i, s)
		}
	}
	if got := rules.Load(config.RulesPath(home)); got.Version != 4 || got.Rules[0].ID != "team-rule" {
		t.Fatalf("rules not cached: %+v", got)
	}
	// Nothing new: no request at all.
	before := len(ing.batches)
	if sum, err := Flush(home, "test", nil); err != nil || sum.Sent != 0 || len(ing.batches) != before {
		t.Fatalf("second flush: %+v %v", sum, err)
	}
}

func TestFlushKeepsCursorWhenServerFails(t *testing.T) {
	ing := &fakeIngest{status: http.StatusServiceUnavailable}
	srv := httptest.NewServer(ing)
	defer srv.Close()
	home := setup(t, 3, srv.URL)
	if _, err := Flush(home, "test", nil); err == nil {
		t.Fatal("expected an error")
	}
	if c := store.New(home).LoadCursor(); c.Seq != 0 {
		t.Fatalf("cursor moved on failure: %+v", c)
	}
	ing.status = 0
	if sum, err := Flush(home, "test", nil); err != nil || sum.Sent != 3 {
		t.Fatalf("retry: %+v %v", sum, err)
	}
}

func TestFlushReportsRevokedToken(t *testing.T) {
	srv := httptest.NewServer(&fakeIngest{})
	defer srv.Close()
	home := setup(t, 1, srv.URL)
	config.SaveDevice(home, config.Device{ID: "dev_up", Token: "tok_revoked", IngestURL: srv.URL})
	if _, err := Flush(home, "test", nil); !errors.Is(err, ErrRevoked) {
		t.Fatalf("got %v", err)
	}
	// QA REC-16: remembered for `sigdek status`, and cleared by the next good upload.
	if _, err := os.Stat(config.TokenRejected(home)); err != nil {
		t.Fatal("rejection not remembered")
	}
	config.SaveDevice(home, config.Device{ID: "dev_up", Token: "tok_secret", IngestURL: srv.URL})
	if _, err := Flush(home, "test", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(config.TokenRejected(home)); err == nil {
		t.Fatal("rejection marker kept after a good upload")
	}
}

// QA REC-16: the server's invalid count and error messages reach the user.
func TestFlushReportsInvalidAndServerErrors(t *testing.T) {
	var status int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != 0 {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":"batch too large"}`))
			return
		}
		w.Write([]byte(`{"accepted":1,"invalid":2}`))
	}))
	defer srv.Close()
	home := setup(t, 3, srv.URL)
	status = 400
	if _, err := Flush(home, "test", nil); err == nil || !strings.Contains(err.Error(), "batch too large") {
		t.Fatalf("got %v", err)
	}
	status = 0
	if sum, err := Flush(home, "test", nil); err != nil || sum.Invalid != 2 {
		t.Fatalf("%+v %v", sum, err)
	}
}

func TestFlushNeedsSignIn(t *testing.T) {
	home := t.TempDir()
	if _, err := Flush(home, "test", nil); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("got %v", err)
	}
}

func seqsOf(batches [][]json.RawMessage) []int64 {
	var out []int64
	for _, b := range batches {
		for _, raw := range b {
			var r struct{ Seq int64 }
			json.Unmarshal(raw, &r)
			out = append(out, r.Seq)
		}
	}
	return out
}

// CONTRACTS2 2: a batch the server finds too large is split; one record that is still too
// large is skipped, logged, and the cursor moves past it.
func TestTooLargeBatchesAreSplit(t *testing.T) {
	var mu sync.Mutex
	var got [][]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Records []json.RawMessage }
		json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		defer mu.Unlock()
		for _, s := range seqsOf([][]json.RawMessage{req.Records}) {
			if s == 4 || len(req.Records) > 2 {
				w.WriteHeader(413)
				return
			}
		}
		got = append(got, req.Records)
		w.Write([]byte(`{"accepted":` + strconv.Itoa(len(req.Records)) + `}`))
	}))
	defer srv.Close()
	home := setup(t, 7, srv.URL)
	var logged []error
	sum, err := FlushWith(home, "test", Options{Log: func(e error) { logged = append(logged, e) }})
	if err != nil || sum.Skipped != 1 || sum.Sent != 6 {
		t.Fatalf("sum %+v err %v", sum, err)
	}
	if s := fmt.Sprint(seqsOf(got)); s != "[1 2 3 5 6 7]" {
		t.Fatalf("sent %s", s)
	}
	if len(logged) != 1 || !strings.Contains(logged[0].Error(), "record 4") {
		t.Fatalf("logged %v", logged)
	}
	if c := store.New(home).LoadCursor(); c.Seq != 7 {
		t.Fatalf("cursor %+v", c)
	}
	if h := LoadHealth(home); h.Skipped != 1 || h.LastError != "" {
		t.Fatalf("health %+v", h)
	}
}

// CONTRACTS2 2: after the server rejects the token, background uploads try again only
// hourly, until sign-in or a flush by hand.
func TestRejectedTokenRetriesHourly(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(401) }))
	defer srv.Close()
	home := setup(t, 2, srv.URL)
	now := time.Now()
	clock := func() time.Time { return now }
	if _, err := FlushWith(home, "t", Options{Background: true, Now: clock}); !errors.Is(err, ErrRevoked) {
		t.Fatalf("got %v", err)
	}
	if paused, why := Paused(home, now.Add(59*time.Minute)); !paused || !strings.Contains(why, "token") {
		t.Fatalf("paused %v %q", paused, why)
	}
	if _, err := FlushWith(home, "t", Options{Background: true, Now: clock}); !errors.Is(err, ErrPaused) || hits != 1 {
		t.Fatalf("background retried at once: %v, %d requests", err, hits)
	}
	if _, err := Flush(home, "t", nil); !errors.Is(err, ErrRevoked) || hits != 2 {
		t.Fatalf("a flush by hand should try: %v, %d requests", err, hits)
	}
	old := time.Now().Add(-61 * time.Minute)
	os.Chtimes(config.TokenRejected(home), old, old)
	if paused, _ := Paused(home, time.Now()); paused {
		t.Fatal("an hour later, background uploads should try again")
	}
}

// CONTRACTS2 2: rate limits and server errors back off, honouring Retry-After, growing
// otherwise, capped at an hour; a good upload clears it.
func TestServerErrorsBackOff(t *testing.T) {
	var status int
	var retry string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if retry != "" {
			w.Header().Set("Retry-After", retry)
		}
		if status != 0 {
			w.WriteHeader(status)
			w.Write([]byte(`{"error":"slow down"}`))
			return
		}
		w.Write([]byte(`{"accepted":2}`))
	}))
	defer srv.Close()
	home := setup(t, 2, srv.URL)
	now := time.Now()
	o := Options{Background: true, Now: func() time.Time { return now }}

	status, retry = 429, "120"
	FlushWith(home, "t", o)
	if h := LoadHealth(home); h.RetryAfter.Sub(now) != 2*time.Minute || !strings.Contains(h.LastError, "slow down") {
		t.Fatalf("Retry-After not honoured: %+v", h)
	}
	if _, err := FlushWith(home, "t", o); !errors.Is(err, ErrPaused) {
		t.Fatalf("got %v", err)
	}
	status, retry = 503, ""
	var waits []time.Duration
	for i := 0; i < 9; i++ {
		now = now.Add(2 * time.Hour)
		FlushWith(home, "t", o)
		waits = append(waits, LoadHealth(home).RetryAfter.Sub(now))
	}
	if fmt.Sprint(waits) != "[2m0s 4m0s 8m0s 16m0s 32m0s 1h0m0s 1h0m0s 1h0m0s 1h0m0s]" { // the 429 was the first failure
		t.Fatalf("waits %v", waits)
	}
	status = 0
	now = now.Add(2 * time.Hour)
	if sum, err := FlushWith(home, "t", o); err != nil || sum.Sent != 2 {
		t.Fatalf("%+v %v", sum, err)
	}
	if h := LoadHealth(home); !h.RetryAfter.IsZero() || h.Fails != 0 || h.LastError != "" || !h.LastOK.Equal(now) {
		t.Fatalf("not cleared: %+v", h)
	}
}

// CONTRACTS2 3: a 200 means the server took the batch, even when its answer can't be read;
// up to 4 MB of answer is read.
func TestA200AlwaysMovesTheCursor(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
	defer srv.Close()
	home := setup(t, 3, srv.URL)
	rules.Save(config.RulesPath(home), rules.Set{Version: 2, Rules: []rules.Rule{{ID: "keep", Effect: rules.Flag, Command: "x"}}})
	body = "<html>proxy login</html>"
	var logged []error
	sum, err := FlushWith(home, "t", Options{Log: func(e error) { logged = append(logged, e) }})
	if err != nil || sum.Sent != 3 || store.New(home).LoadCursor().Seq != 3 || len(logged) != 1 || !strings.Contains(logged[0].Error(), "couldn't be read") {
		t.Fatalf("sum %+v err %v logged %v", sum, err, logged)
	}
	if got := rules.Load(config.RulesPath(home)); got.Version != 2 {
		t.Fatalf("rules changed: %+v", got)
	}
	// The largest rule set the service allows (200 rules, each field at its cap) is over the
	// old 1 MB read limit, and arrives whole.
	big := rules.Set{Version: 3}
	for i := 0; i < 200; i++ {
		big.Rules = append(big.Rules, rules.Rule{ID: fmt.Sprintf("r%d", i), Effect: rules.Flag, Label: strings.Repeat("l", 120), Message: strings.Repeat("m", 500),
			Kinds: []string{"shell", "file_read", "file_edit", "web", "mcp", "other"}, Tool: strings.Repeat("t", 1000),
			Command: strings.Repeat("c", 1000), Path: strings.Repeat("p", 1000), URL: strings.Repeat("u", 1000), MCP: strings.Repeat("s", 1000)})
	}
	b, _ := json.Marshal(Response{Accepted: 1, Rules: &big})
	if len(b) < 1<<20 {
		t.Fatalf("test rule set only %d bytes", len(b))
	}
	body = string(b)
	setupMore(t, home, 1)
	if _, err := Flush(home, "t", nil); err != nil {
		t.Fatal(err)
	}
	if got := rules.Load(config.RulesPath(home)); got.Version != 3 || len(got.Rules) != 200 {
		t.Fatalf("large rule set: version %d, %d rules", got.Version, len(got.Rules))
	}
}

func setupMore(t *testing.T, home string, n int) {
	st := store.New(home)
	for i := 0; i < n; i++ {
		now := time.Now()
		st.Append(&event.Record{V: 1, ID: event.NewID(now), TS: event.Timestamp(now), Device: "dev_up", Tool: "claude-code",
			Session: "s", Repo: event.Repo{RootHash: strings.Repeat("a", 64)}, Event: "session_start"})
	}
}

// A rule edited in the dashboard reaches the machine even when the version and the number of
// rules stay the same; a rule the recorder can't compile is logged when it arrives.
func TestEditedRulesAreSaved(t *testing.T) {
	set := rules.Set{Version: 9, Rules: []rules.Rule{{ID: "no-push", Effect: rules.Deny, Kinds: []string{"shell"}, Command: "git push*"}}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(Response{Accepted: 1, Rules: &set})
	}))
	defer srv.Close()
	home := setup(t, 1, srv.URL)
	Flush(home, "t", nil)
	set.Rules = []rules.Rule{{ID: "no-push", Effect: rules.Flag, Kinds: []string{"shell"}, Command: "re:git push(?!x)"}}
	setupMore(t, home, 1)
	var logged []error
	FlushWith(home, "t", Options{Log: func(e error) { logged = append(logged, e) }})
	if got := rules.Load(config.RulesPath(home)); got.Rules[0].Effect != rules.Flag {
		t.Fatalf("edit not saved: %+v", got)
	}
	if len(logged) != 1 || !strings.Contains(logged[0].Error(), "no-push") {
		t.Fatalf("logged %v", logged)
	}
	setupMore(t, home, 1)
	logged = nil
	FlushWith(home, "t", Options{Log: func(e error) { logged = append(logged, e) }})
	if len(logged) != 0 {
		t.Fatalf("logged again for unchanged rules: %v", logged)
	}
}

// A server or proxy that answers 413 to everything gets no record skipped: the upload backs
// off instead, and resumes once requests go through.
func TestTooLargeForEverythingSkipsNothing(t *testing.T) {
	refuse := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Records []json.RawMessage }
		json.NewDecoder(r.Body).Decode(&req)
		if refuse {
			w.WriteHeader(413)
			return
		}
		w.Write([]byte(`{"accepted":` + strconv.Itoa(len(req.Records)) + `}`))
	}))
	defer srv.Close()
	home := setup(t, 4, srv.URL)
	sum, err := Flush(home, "t", nil)
	if err == nil || sum.Skipped != 0 || store.New(home).LoadCursor().Seq != 0 {
		t.Fatalf("sum %+v err %v cursor %+v", sum, err, store.New(home).LoadCursor())
	}
	if h := LoadHealth(home); h.RetryAfter.IsZero() {
		t.Fatalf("no back-off: %+v", h)
	}
	refuse = false
	if sum, err := Flush(home, "t", nil); err != nil || sum.Sent != 4 {
		t.Fatalf("%+v %v", sum, err)
	}
}

// The first record is the oversized one: the next, sent alone, proves the server takes
// records, so the first is skipped and the rest go up.
func TestOversizedFirstRecordIsSkipped(t *testing.T) {
	var sent []int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Records []json.RawMessage }
		json.NewDecoder(r.Body).Decode(&req)
		for _, s := range seqsOf([][]json.RawMessage{req.Records}) {
			if s == 1 {
				w.WriteHeader(413)
				return
			}
		}
		sent = append(sent, seqsOf([][]json.RawMessage{req.Records})...)
		w.Write([]byte(`{"accepted":` + strconv.Itoa(len(req.Records)) + `}`))
	}))
	defer srv.Close()
	home := setup(t, 3, srv.URL)
	sum, err := Flush(home, "t", nil)
	if err != nil || sum.Skipped != 1 || store.New(home).LoadCursor().Seq != 3 || fmt.Sprint(sent) != "[2 2 3]" {
		t.Fatalf("sum %+v err %v sent %v", sum, err, sent)
	}
}

// QA3-REC-5: an upload with a time limit gives up in time, whether the server is slow or
// another upload holds the lock, and waits for that upload when it finishes in time.
func TestUploadWithinAGivenTime(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body) // then the server notices when the client hangs up
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer slow.Close()
	home := setup(t, 2, slow.URL)
	start := time.Now()
	if _, err := FlushWith(home, "test", Options{Background: true, Within: 300 * time.Millisecond}); err == nil {
		t.Fatal("a server that never answers should fail the upload")
	}
	if el := time.Since(start); el > 2*time.Second {
		t.Fatalf("gave up after %v", el)
	}

	var got int
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got++
		w.Write([]byte(`{"accepted":2}`))
	}))
	defer ok.Close()
	config.SaveDevice(home, config.Device{ID: "dev_up", Token: "tok", IngestURL: ok.URL})
	held, _ := lockfile.Acquire(config.FlushLock(home))
	start = time.Now()
	if _, err := FlushWith(home, "test", Options{Background: true, Within: 200 * time.Millisecond}); !errors.Is(err, ErrBusy) || time.Since(start) > time.Second {
		t.Fatalf("lock held throughout: %v after %v", err, time.Since(start))
	}
	go func() { time.Sleep(100 * time.Millisecond); held.Release() }()
	if sum, err := FlushWith(home, "test", Options{Background: true, Within: 2 * time.Second}); err != nil || sum.Sent != 2 || got != 1 {
		t.Fatalf("after the other upload finished: %+v %v (requests %d)", sum, err, got)
	}
}
