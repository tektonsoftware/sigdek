package login

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/event"
	"github.com/tektonsoftware/sigdek/internal/store"
	"github.com/tektonsoftware/sigdek/internal/upload"
)

// fakeApp imitates Better Auth's device authorization endpoints and SigDek's device registration.
func fakeApp(t *testing.T, pendingPolls int32, deny bool) (*httptest.Server, *map[string]any) {
	var polls atomic.Int32
	registered := map[string]any{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/device/code", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["client_id"] != ClientID {
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"device_code": "dc_1", "user_code": "ABCDWXYZ", "verification_uri": "/device",
			"verification_uri_complete": "/device?user_code=ABCDWXYZ", "interval": 5, "expires_in": 600})
	})
	mux.HandleFunc("/api/auth/device/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "urn:ietf:params:oauth:grant-type:device_code" || body["device_code"] != "dc_1" {
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		n := polls.Add(1)
		switch {
		case deny:
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "access_denied"})
		case n <= pendingPolls:
			w.WriteHeader(400)
			json.NewEncoder(w).Encode(map[string]string{"error": "authorization_pending"})
		default:
			json.NewEncoder(w).Encode(map[string]string{"access_token": "session_abc"})
		}
	})
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer session_abc" {
			w.WriteHeader(401)
			return
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &registered)
		json.NewEncoder(w).Encode(Registration{DeviceID: registered["device_id"].(string), Token: "sdt_device_token",
			IngestURL: "https://ingest.example.test", User: "dev@example.com", OrgID: "org_1", OrgName: "Tekton"})
	})
	mux.HandleFunc("/api/auth/sign-out", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	mux.HandleFunc("/api/devices/self/revoke", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &registered
}

func TestLoginRegistersDeviceAndAnchorsChain(t *testing.T) {
	srv, registered := fakeApp(t, 2, false)
	home := t.TempDir()
	config.SaveDevice(home, config.Device{ID: "dev_local", AppURL: srv.URL})
	st := store.New(home)
	for i := 0; i < 3; i++ { // records from before sign-in
		now := time.Now()
		st.Append(&event.Record{V: 1, ID: event.NewID(now), TS: event.Timestamp(now), Device: "dev_local", Tool: "cursor",
			Session: "s", Repo: event.Repo{RootHash: strings.Repeat("a", 64)}, Event: "session_start"})
	}
	var out strings.Builder
	var slept []time.Duration
	reg, err := Login(Options{Home: home, Version: "1.0.0", Out: &out, Sleep: func(d time.Duration) { slept = append(slept, d) }})
	if err != nil {
		t.Fatal(err)
	}
	if reg.Token != "sdt_device_token" || len(slept) != 3 {
		t.Fatalf("reg %+v slept %v", reg, slept)
	}
	if !strings.Contains(out.String(), "ABCD-WXYZ") || !strings.Contains(out.String(), srv.URL+"/device?user_code=ABCDWXYZ") {
		t.Fatalf("prompt: %s", out.String())
	}
	head := (*registered)["chain_head"].(map[string]any)
	if (*registered)["device_id"] != "dev_local" || head["seq"].(float64) != 3 {
		t.Fatalf("registration body: %+v", *registered)
	}
	dev, _ := config.LoadDevice(home)
	if dev.Token != "sdt_device_token" || dev.User != "dev@example.com" || dev.Ingest() != "https://ingest.example.test" || dev.ID != "dev_local" {
		t.Fatalf("device: %+v", dev)
	}
	if c := st.LoadCursor(); c.Seq != 3 || c.File == "" || c.Offset == 0 {
		t.Fatalf("pre-sign-in records should stay local: cursor %+v", c)
	}

	res, err := Logout(home, nil)
	if err != nil || !res.WasSignedIn || res.RevokeErr != nil {
		t.Fatalf("logout: %+v %v", res, err)
	}
	if dev, _ := config.LoadDevice(home); dev.SignedIn() || dev.ID != "dev_local" {
		t.Fatalf("after logout: %+v", dev)
	}
	if res, _ := Logout(home, nil); res.WasSignedIn {
		t.Fatal("a second logout should report it wasn't signed in")
	}
}

func TestLoginDenied(t *testing.T) {
	srv, _ := fakeApp(t, 0, true)
	home := t.TempDir()
	config.SaveDevice(home, config.Device{ID: "dev_local", AppURL: srv.URL})
	_, err := Login(Options{Home: home, Out: io.Discard, Sleep: func(time.Duration) {}})
	if err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("got %v", err)
	}
	if dev, _ := config.LoadDevice(home); dev.SignedIn() {
		t.Fatal("must not be signed in")
	}
}

// scriptedApp answers the device-token poll with the given statuses in turn, then a token,
// and registers with regStatus/regBody.
func scriptedApp(t *testing.T, polls []int, regStatus int, regBody string) *httptest.Server {
	var n atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/auth/device/code", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"device_code": "dc", "user_code": "ABCDWXYZ", "verification_uri": "/device", "interval": 5})
	})
	mux.HandleFunc("/api/auth/device/token", func(w http.ResponseWriter, r *http.Request) {
		i := int(n.Add(1)) - 1
		if i < len(polls) {
			w.WriteHeader(polls[i])
			if polls[i] == 400 {
				w.Write([]byte(`{"error":"authorization_pending"}`))
			} else {
				w.Write([]byte(`{"error":"internal"}`))
			}
			return
		}
		w.Write([]byte(`{"access_token":"session_abc"}`))
	})
	mux.HandleFunc("/api/devices", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(regStatus)
		w.Write([]byte(regBody))
	})
	mux.HandleFunc("/api/auth/sign-out", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("/api/devices/self/revoke", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		w.Write([]byte(`{"error":"database unavailable"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func signIn(t *testing.T, srv *httptest.Server) (string, error) {
	home := t.TempDir()
	config.SaveDevice(home, config.Device{ID: "dev_local", AppURL: srv.URL})
	_, err := Login(Options{Home: home, Out: io.Discard, Sleep: func(time.Duration) {}})
	return home, err
}

// QA REC-16: one server error while the user is approving in the browser doesn't end sign-in;
// several in a row do, with the server's explanation.
func TestLoginRidesOutAServerError(t *testing.T) {
	ok := `{"device_id":"dev_local","token":"sdt_x","user":"dev@example.com","org_id":"o"}`
	if _, err := signIn(t, scriptedApp(t, []int{400, 500, 400, 502}, 200, ok)); err != nil {
		t.Fatalf("a transient error ended sign-in: %v", err)
	}
	_, err := signIn(t, scriptedApp(t, []int{500, 500, 500, 500, 500, 500}, 200, ok))
	if err == nil || !strings.Contains(err.Error(), "internal") {
		t.Fatalf("got %v", err)
	}
}

// QA REC-16: the server's error message is shown, not just a status code.
func TestServerErrorBodiesAreShown(t *testing.T) {
	_, err := signIn(t, scriptedApp(t, nil, 402, `{"error":"Your plan allows 5 machines; remove one in the dashboard"}`))
	if err == nil || !strings.Contains(err.Error(), "Your plan allows 5 machines") {
		t.Fatalf("got %v", err)
	}
}

// QA REC-16: logout says when the server didn't revoke the token.
func TestLogoutReportsAFailedRevoke(t *testing.T) {
	srv := scriptedApp(t, nil, 200, "")
	home := t.TempDir()
	config.SaveDevice(home, config.Device{ID: "dev_local", Token: "sdt_x", AppURL: srv.URL})
	res, err := Logout(home, nil)
	if err != nil || !res.WasSignedIn || res.RevokeErr == nil || !strings.Contains(res.RevokeErr.Error(), "database unavailable") {
		t.Fatalf("%+v %v", res, err)
	}
	if dev, _ := config.LoadDevice(home); dev.SignedIn() {
		t.Fatal("the token must be forgotten locally even when the server can't revoke it")
	}
}

// QA2-REC-2: a machine signing in again (its token was revoked, or it signed out) uploads what
// it recorded in between, when the server's head is a record this machine holds.
func TestLoginResumesAfterTheServersHead(t *testing.T) {
	setup := func(t *testing.T) (string, *store.Store, []string) {
		home := t.TempDir()
		st := store.New(home)
		for i := 0; i < 5; i++ {
			now := time.Now()
			st.Append(&event.Record{V: 1, ID: event.NewID(now), TS: event.Timestamp(now), Device: "dev_local", Tool: "cursor",
				Session: "s", Repo: event.Repo{RootHash: strings.Repeat("a", 64)}, Event: "session_start"})
		}
		var hashes []string
		st.ReadFrom(store.Cursor{}, func(l store.Line) bool {
			var h struct{ Hash string }
			json.Unmarshal(l.Raw, &h)
			hashes = append(hashes, h.Hash)
			return true
		})
		// Signed in before, uploaded up to record 2, then the token was revoked.
		var two store.Line
		st.ReadFrom(store.Cursor{Seq: 1}, func(l store.Line) bool { two = l; return false })
		st.SaveCursor(store.Cursor{Seq: 2, File: two.File, Offset: two.Offset})
		return home, st, hashes
	}
	next := func(st *store.Store) int64 {
		var seq int64
		st.ReadFrom(st.LoadCursor(), func(l store.Line) bool { seq = l.Seq; return false })
		return seq
	}
	ok := `{"device_id":"dev_local","token":"sdt_x","user":"dev@example.com","org_id":"o"`
	for name, tc := range map[string]struct {
		head func(hashes []string) string
		want int64 // the next record sent; 0 for none (uploads start after the local head)
	}{
		"server has record 2":       {func(h []string) string { return `,"head":{"seq":2,"hash":"` + h[1] + `"}}` }, 3},
		"server has nothing":        {func(h []string) string { return `,"head":null}` }, 0},
		"older server, no field":    {func(h []string) string { return `}` }, 0},
		"hash differs (a fork)":     {func(h []string) string { return `,"head":{"seq":2,"hash":"` + h[0] + `"}}` }, 0},
		"server is ahead":           {func(h []string) string { return `,"head":{"seq":9,"hash":"` + h[4] + `"}}` }, 0},
		"server has everything":     {func(h []string) string { return `,"head":{"seq":5,"hash":"` + h[4] + `"}}` }, 0},
		"server has the first only": {func(h []string) string { return `,"head":{"seq":1,"hash":"` + h[0] + `"}}` }, 2},
	} {
		t.Run(name, func(t *testing.T) {
			home, st, hashes := setup(t)
			srv := scriptedApp(t, nil, 200, ok+tc.head(hashes))
			dev, _ := config.LoadDevice(home)
			dev.AppURL = srv.URL
			config.SaveDevice(home, dev)
			if _, err := Login(Options{Home: home, Out: io.Discard, Sleep: func(time.Duration) {}}); err != nil {
				t.Fatal(err)
			}
			if got := next(st); got != tc.want {
				t.Fatalf("next record sent: %d, want %d (cursor %+v)", got, tc.want, st.LoadCursor())
			}
		})
	}
}

// QA3-REC-9: sign-in saves the app it used, and logout sends the token only there, even when
// SIGDEK_APP_URL now names another server.
func TestLogoutRevokesAtTheAppThatSignedIn(t *testing.T) {
	srv, _ := fakeApp(t, 0, false)
	var stray atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { stray.Add(1) }))
	t.Cleanup(other.Close)
	home := t.TempDir()
	config.SaveDevice(home, config.Device{ID: "dev_local"})
	t.Setenv("SIGDEK_APP_URL", srv.URL)
	if _, err := Login(Options{Home: home, Out: io.Discard, Sleep: func(time.Duration) {}}); err != nil {
		t.Fatal(err)
	}
	if dev, _ := config.LoadDevice(home); dev.AppURL != srv.URL {
		t.Fatalf("app URL not saved: %+v", dev)
	}
	t.Setenv("SIGDEK_APP_URL", other.URL)
	res, err := Logout(home, nil)
	if err != nil || res.RevokeErr != nil || res.Org != "Tekton" || stray.Load() != 0 {
		t.Fatalf("logout: %+v %v, requests to the other server: %d", res, err, stray.Load())
	}
}

// QA3-REC-2, QA3-REC-10: sign-in says how many records will upload and how many stay, and the
// upload errors and back-off from before it are over.
func TestLoginCountsRecordsAndClearsUploadErrors(t *testing.T) {
	home := t.TempDir()
	st := store.New(home)
	var heads []string
	for i := 0; i < 5; i++ {
		now := time.Now()
		r := &event.Record{V: 1, ID: event.NewID(now), TS: event.Timestamp(now), Device: "dev_local", Tool: "cursor",
			Session: "s", Repo: event.Repo{RootHash: strings.Repeat("a", 64)}, Event: "session_start"}
		st.Append(r)
		heads = append(heads, r.Hash)
	}
	os.MkdirAll(config.StateDir(home), 0o700)
	os.WriteFile(filepath.Join(config.StateDir(home), "upload-health.json"),
		[]byte(`{"last_error":"this device's token was rejected","fails":3,"retry_after":"2999-01-01T00:00:00Z"}`), 0o600)
	srv := scriptedApp(t, nil, 200, `{"device_id":"dev_local","token":"sdt_x","user":"dev@example.com","org_id":"o","head":{"seq":2,"hash":"`+heads[1]+`"}}`)
	config.SaveDevice(home, config.Device{ID: "dev_local", AppURL: srv.URL})
	reg, err := Login(Options{Home: home, Out: io.Discard, Sleep: func(time.Duration) {}})
	if err != nil || reg.Pending != 3 || reg.Kept != 0 {
		t.Fatalf("resumed: %+v %v", reg, err)
	}
	h := upload.LoadHealth(home)
	if h.LastError != "" || h.Fails != 0 || !h.RetryAfter.IsZero() || h.SignedIn.IsZero() {
		t.Fatalf("health after sign-in: %+v", h)
	}
	if paused, why := upload.Paused(home, time.Now()); paused {
		t.Fatalf("uploads still paused: %s", why)
	}

	home2 := t.TempDir()
	for i := 0; i < 4; i++ {
		now := time.Now()
		store.New(home2).Append(&event.Record{V: 1, ID: event.NewID(now), TS: event.Timestamp(now), Device: "dev_2", Tool: "cursor",
			Session: "s", Repo: event.Repo{RootHash: strings.Repeat("a", 64)}, Event: "session_start"})
	}
	config.SaveDevice(home2, config.Device{ID: "dev_2", AppURL: srv.URL})
	if reg, err := Login(Options{Home: home2, Out: io.Discard, Sleep: func(time.Duration) {}}); err != nil || reg.Pending != 0 || reg.Kept != 4 {
		t.Fatalf("first sign-in: %+v %v", reg, err)
	}
}
