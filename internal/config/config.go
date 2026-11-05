// Package config holds where SigDek keeps its files and the device's identity.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/tektonsoftware/sigdek/internal/fsretry"
	"github.com/tektonsoftware/sigdek/internal/lockfile"
)

const (
	DefaultAppURL    = "https://app.sigdek.com"
	DefaultIngestURL = "https://ingest.sigdek.com"
)

// Home is ~/.sigdek, or SIGDEK_HOME when set.
func Home() string {
	if h := os.Getenv("SIGDEK_HOME"); h != "" {
		return h
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".sigdek")
	}
	return ".sigdek"
}

func LogDir(home string) string      { return filepath.Join(home, "log") }
func StateDir(home string) string    { return filepath.Join(home, "state") }
func DevicePath(home string) string  { return filepath.Join(home, "device.json") }
func RulesPath(home string) string   { return filepath.Join(home, "rules.json") }
func ErrorLog(home string) string    { return filepath.Join(home, "errors.log") }
func ChainLock(home string) string   { return filepath.Join(StateDir(home), "chain.lock") }
func FlushLock(home string) string   { return filepath.Join(StateDir(home), "flush.lock") }
func UploadState(home string) string { return filepath.Join(StateDir(home), "upload.json") }

// TokenRejected exists while the server is refusing this device's token (since the last
// upload), so `sigdek status` can say so.
func TokenRejected(home string) string { return filepath.Join(StateDir(home), "token-rejected") }

// Device is this machine's identity. The ID is generated locally on first use and
// registered with the hosted service at sign-in, so the chain is continuous across sign-in.
type Device struct {
	ID        string `json:"id"`
	Token     string `json:"token,omitempty"` // ingest-only token, issued at sign-in
	User      string `json:"user,omitempty"`  // email of the signed-in user
	Org       string `json:"org,omitempty"`
	OrgName   string `json:"org_name,omitempty"`
	IngestURL string `json:"ingest_url,omitempty"`
	AppURL    string `json:"app_url,omitempty"`
}

func (d Device) SignedIn() bool { return d.Token != "" }

func (d Device) Ingest() string {
	if u := os.Getenv("SIGDEK_INGEST_URL"); u != "" {
		return u
	}
	if d.IngestURL != "" {
		return d.IngestURL
	}
	return DefaultIngestURL
}

func (d Device) App() string {
	if u := os.Getenv("SIGDEK_APP_URL"); u != "" {
		return u
	}
	if d.AppURL != "" {
		return d.AppURL
	}
	return DefaultAppURL
}

// LoadDevice reads device.json, creating it with a fresh id on first use. The first hooks
// of a session often run at the same moment, so the id is created under the chain lock and
// re-read once the lock is held: every process ends up with the same device.
func LoadDevice(home string) (Device, error) {
	d, ok, err := readDevice(home)
	if ok || err != nil {
		return d, err
	}
	if lock, lerr := lockfile.AcquireTimeout(ChainLock(home), time.Second); lerr == nil {
		defer lock.Release()
		if d, ok, err = readDevice(home); ok || err != nil {
			return d, err
		}
	}
	var buf [12]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return d, err
	}
	d.ID = "dev_" + hex.EncodeToString(buf[:])
	return d, SaveDevice(home, d)
}

// ReadDevice reads device.json without creating it; ok is false when it doesn't exist yet
// (or has no id).
func ReadDevice(home string) (Device, bool, error) { return readDevice(home) }

var idInJSON = regexp.MustCompile(`"id"\s*:\s*"(dev_[0-9a-f]{24})"`)

// SalvageID finds the device id in a device.json that no longer parses, so records keep
// going on the machine's own chain until the file is fixed; "" when there is none.
func SalvageID(home string) string {
	b, err := fsretry.ReadFile(DevicePath(home))
	if err != nil {
		return ""
	}
	if m := idInJSON.FindSubmatch(b); m != nil {
		return string(m[1])
	}
	return ""
}

// readDevice reports ok when device.json exists and has an id.
func readDevice(home string) (Device, bool, error) {
	var d Device
	p := DevicePath(home)
	b, err := fsretry.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return d, false, fmt.Errorf("%s is not valid JSON (%v); fix it, or delete it and run `sigdek login` again", p, err)
	}
	return d, d.ID != "", nil
}

// SaveDevice writes device.json readable only by the user; it can hold the ingest token.
// Each writer uses its own temporary file, so concurrent writers can't mix their bytes.
func SaveDevice(home string, d Device) error {
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(home, "device-*.tmp")
	if err != nil {
		return err
	}
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return errors.Join(werr, cerr)
	}
	if err := fsretry.Rename(tmp.Name(), DevicePath(home)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	return nil
}
