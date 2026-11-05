// Package login signs a device in with the OAuth device authorization flow (RFC 8628):
// the CLI shows a short code, the user approves it in the browser, and the device then
// trades the resulting session for its own ingest-only token.
package login

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/tektonsoftware/sigdek/internal/apierr"
	"github.com/tektonsoftware/sigdek/internal/config"
	"github.com/tektonsoftware/sigdek/internal/store"
	"github.com/tektonsoftware/sigdek/internal/upload"
)

const ClientID = "sigdek-cli"

type deviceCode struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

type tokenResponse struct {
	AccessToken      string `json:"access_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

// Registration is what the app returns when a device is registered.
type Registration struct {
	DeviceID  string `json:"device_id"`
	Token     string `json:"token"`
	IngestURL string `json:"ingest_url"`
	User      string `json:"user"`
	OrgID     string `json:"org_id"`
	OrgName   string `json:"org_name"`
	// Head is the last record the server has from this device, when it has any (a device
	// signing in again). An older server leaves it out.
	Head *ServerHead `json:"head"`

	// Worked out by Login, not sent by the server:
	Pending int64 `json:"-"` // records made since this machine's last upload (including while signed out), uploading now
	Kept    int64 `json:"-"` // records made before this sign-in that stay on this machine
}

// ServerHead is the server's chain head for a device.
type ServerHead struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
}

type Options struct {
	Home        string
	Version     string
	Out         io.Writer
	OpenBrowser bool
	Client      *http.Client
	Sleep       func(time.Duration) // replaced in tests
}

// Login runs the whole flow and saves the device token.
func Login(o Options) (*Registration, error) {
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	dev, err := config.LoadDevice(o.Home)
	if err != nil {
		return nil, err
	}
	app := strings.TrimRight(dev.App(), "/")

	var dc deviceCode
	if err := postJSON(o.Client, app+"/api/auth/device/code", "", map[string]string{"client_id": ClientID, "scope": "device:register"}, &dc); err != nil {
		return nil, fmt.Errorf("starting sign-in: %w", err)
	}
	if dc.DeviceCode == "" || dc.UserCode == "" {
		return nil, errors.New("starting sign-in: the server returned no code")
	}
	link := dc.VerificationURIComplete
	if link == "" {
		link = dc.VerificationURI
	}
	if link != "" && !strings.HasPrefix(link, "http") {
		link = app + link
	}
	fmt.Fprintf(o.Out, "Your code: %s\nApprove it at: %s\n\nWaiting for approval…\n", formatCode(dc.UserCode), link)
	if o.OpenBrowser {
		openBrowser(link)
	}

	interval := time.Duration(max(dc.Interval, 5)) * time.Second
	expires := 30 * time.Minute
	if dc.ExpiresIn > 0 {
		expires = time.Duration(dc.ExpiresIn) * time.Second
	}
	var session string
	failures := 0 // in a row; one hiccup while the user is in the browser shouldn't end sign-in
	for waited := time.Duration(0); waited < expires; waited += interval {
		o.Sleep(interval)
		var tr tokenResponse
		err := postJSON(o.Client, app+"/api/auth/device/token", "", map[string]string{
			"grant_type":  "urn:ietf:params:oauth:grant-type:device_code",
			"device_code": dc.DeviceCode,
			"client_id":   ClientID,
		}, &tr)
		if tr.AccessToken != "" && err == nil {
			session = tr.AccessToken
			break
		}
		var api *apierr.Error
		if err != nil && (!errors.As(err, &api) || api.Temporary()) {
			if failures++; failures >= MaxPollFailures {
				return nil, fmt.Errorf("waiting for approval: %w", err)
			}
			continue
		}
		failures = 0
		switch tr.Error {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied":
			return nil, errors.New("sign-in was denied in the browser")
		case "expired_token":
			return nil, errors.New("the code expired; run `sigdek login` again")
		default:
			if err == nil {
				err = errors.New("the server returned no token")
			}
			return nil, fmt.Errorf("sign-in failed: %w", err)
		}
	}
	if session == "" {
		return nil, errors.New("the code expired; run `sigdek login` again")
	}

	head, err := store.New(o.Home).Head()
	if err != nil {
		return nil, err
	}
	host, _ := os.Hostname()
	var reg Registration
	err = postJSON(o.Client, app+"/api/devices", session, map[string]any{
		"device_id":        dev.ID,
		"name":             host,
		"os":               runtime.GOOS,
		"arch":             runtime.GOARCH,
		"recorder_version": o.Version,
		"chain_head":       map[string]any{"seq": head.Seq, "hash": head.Hash},
	}, &reg)
	if err != nil {
		return nil, fmt.Errorf("registering this device: %w", err)
	}
	if reg.Token == "" {
		return nil, errors.New("registering this device: the server returned no token")
	}
	// The browser session is only needed to register; the device keeps its own token.
	_ = postJSON(o.Client, app+"/api/auth/sign-out", session, map[string]string{}, nil)

	// The app that issued the token is the one logout asks to revoke it, whatever
	// SIGDEK_APP_URL says by then.
	dev.Token, dev.User, dev.Org, dev.OrgName, dev.AppURL = reg.Token, reg.User, reg.OrgID, reg.OrgName, app
	if reg.IngestURL != "" {
		dev.IngestURL = reg.IngestURL
	}
	if err := config.SaveDevice(o.Home, dev); err != nil {
		return nil, err
	}
	_ = os.Remove(config.TokenRejected(o.Home))
	upload.SignedIn(o.Home, time.Now()) // earlier upload errors and back-off are over
	if reg.Pending, reg.Kept, err = resumeUploads(store.New(o.Home), reg.Head, head); err != nil {
		return nil, err
	}
	return &reg, nil
}

// resumeUploads sets where uploads start, and returns how many records will upload and how
// many stay on this machine. When the server already has this device's chain up to a record
// this machine also holds (a machine signing in again after its token was revoked or it signed
// out), uploads continue right after it, so what was recorded in between goes up. Otherwise
// records written before sign-in stay local and uploads start after the head registered now.
func resumeUploads(st *store.Store, server *ServerHead, local store.Head) (pending, kept int64, err error) {
	if server != nil && server.Seq >= 1 && server.Seq <= local.Seq {
		if l, hash, ok, err := st.At(server.Seq); err == nil && ok && hash == server.Hash {
			return local.Seq - l.Seq, 0, st.SaveCursor(store.Cursor{Seq: l.Seq, File: l.File, Offset: l.Offset})
		}
	}
	from := st.LoadCursor().Seq
	if from >= local.Seq {
		return 0, 0, nil
	}
	cur := store.Cursor{Seq: local.Seq}
	if files, _ := st.Files(); len(files) > 0 {
		last := files[len(files)-1]
		if info, err := os.Stat(last); err == nil {
			cur.File, cur.Offset = baseName(last), info.Size()
		}
	}
	return 0, local.Seq - from, st.SaveCursor(cur)
}

// LogoutResult says what Logout did.
type LogoutResult struct {
	WasSignedIn bool
	Org         string // the team it was signed in to
	RevokeErr   error  // the server didn't confirm it revoked the token
}

// Logout asks the server to revoke the device token and forgets it locally either way; the
// revocation is best effort, so the caller should say when it failed. The token goes only to
// the app that issued it (saved at sign-in); a machine signed in by an older version, which
// didn't save it, uses SIGDEK_APP_URL or the default as before.
func Logout(home string, client *http.Client) (LogoutResult, error) {
	dev, err := config.LoadDevice(home)
	if err != nil || !dev.SignedIn() {
		return LogoutResult{}, err
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	app := dev.AppURL
	if app == "" {
		app = dev.App()
	}
	res := LogoutResult{WasSignedIn: true, Org: dev.OrgName}
	if res.Org == "" {
		res.Org = dev.Org
	}
	res.RevokeErr = postJSON(client, strings.TrimRight(app, "/")+"/api/devices/self/revoke", dev.Token, map[string]string{}, nil)
	dev.Token, dev.User, dev.Org, dev.OrgName = "", "", "", ""
	_ = os.Remove(config.TokenRejected(home))
	return res, config.SaveDevice(home, dev)
}

// MaxPollFailures is how many failed polls in a row end sign-in.
const MaxPollFailures = 5

func postJSON(c *http.Client, url, bearer string, body any, out any) error {
	b, _ := json.Marshal(body)
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := c.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if out != nil && len(data) > 0 {
		_ = json.Unmarshal(data, out) // error bodies carry {error: ...} too
	}
	if res.StatusCode >= 300 {
		return apierr.New(url, res.StatusCode, data)
	}
	return nil
}

func formatCode(c string) string {
	c = strings.ToUpper(strings.ReplaceAll(c, "-", ""))
	if len(c) == 8 {
		return c[:4] + "-" + c[4:]
	}
	return c
}

func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
