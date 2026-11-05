// Package event defines the AgentEvent record (schema/agent-event.v1.schema.json)
// and how its hash is computed.
package event

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Genesis is the prev hash of a device's first record.
const Genesis = "0000000000000000000000000000000000000000000000000000000000000000"

type Repo struct {
	Remote   *string `json:"remote"`
	RootHash string  `json:"root_hash"`
}

type MCP struct {
	Server string `json:"server"`
	Tool   string `json:"tool"`
}

type Action struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Command   string `json:"command,omitempty"`
	Path      string `json:"path,omitempty"`
	URL       string `json:"url,omitempty"`
	MCP       *MCP   `json:"mcp,omitempty"`
	InputHash string `json:"input_hash"`
}

type Prompt struct {
	Hash   string `json:"hash"`
	Length int    `json:"length"`
}

type Subagent struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

type Config struct {
	Source *string `json:"source"`
	Path   *string `json:"path"`
}

// Record is one AgentEvent. Field order follows the schema; it doesn't affect the hash.
type Record struct {
	V           int     `json:"v"`
	ID          string  `json:"id"`
	TS          string  `json:"ts"`
	Seq         int64   `json:"seq"`
	Prev        string  `json:"prev"`
	Hash        string  `json:"hash"`
	Device      string  `json:"device"`
	User        *string `json:"user"`
	Tool        string  `json:"tool"`
	ToolVersion *string `json:"tool_version"`
	Session     string  `json:"session"`
	PromptID    *string `json:"prompt_id"`
	Repo        Repo    `json:"repo"`
	Event       string  `json:"event"`

	Action         *Action   `json:"action,omitempty"`
	Outcome        string    `json:"outcome,omitempty"`
	DurationMS     *int64    `json:"duration_ms,omitempty"`
	Rule           string    `json:"rule,omitempty"`
	Prompt         *Prompt   `json:"prompt,omitempty"`
	Model          **string  `json:"model,omitempty"`
	PermissionMode **string  `json:"permission_mode,omitempty"`
	Subagent       *Subagent `json:"subagent,omitempty"`
	Config         *Config   `json:"config,omitempty"`

	Redactions int `json:"redactions"`
}

// Optional wraps a value for the optional-and-nullable fields (model, permission_mode):
// nil pointer = field absent, pointer to nil = explicit null.
func Optional(s *string) **string { return &s }

// Str returns a pointer to s, or nil when s is empty.
func Str(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Timestamp formats t the way the schema requires.
func Timestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// Each string field's maxLength in schema/agent-event.v1.schema.json. Clean cuts by bytes,
// which is never more than the schema's count of characters.
const (
	MaxDevice         = 64
	MaxUser           = 320
	MaxToolVersion    = 64
	MaxSession        = 128
	MaxPromptID       = 128
	MaxRemote         = 512
	MaxActionName     = 128
	MaxCommand        = 4096
	MaxPath           = 1024
	MaxURL            = 2048
	MaxMCPName        = 128
	MaxRule           = 64
	MaxModel          = 128
	MaxPermissionMode = 64
	MaxSubagent       = 128
	MaxConfigSource   = 64
	MaxConfigPath     = 1024
)

// Limit cuts every string field to the schema's maxLength and makes it valid UTF-8, so no
// input, however large or odd, can produce a record the log or the server can't take.
func (r *Record) Limit() {
	r.Device = Clean(r.Device, MaxDevice)
	r.User = cleanPtr(r.User, MaxUser)
	r.ToolVersion = cleanPtr(r.ToolVersion, MaxToolVersion)
	r.Session = Clean(r.Session, MaxSession)
	r.PromptID = cleanPtr(r.PromptID, MaxPromptID)
	r.Repo.Remote = cleanPtr(r.Repo.Remote, MaxRemote)
	if a := r.Action; a != nil {
		a.Name = Clean(a.Name, MaxActionName)
		a.Command = Clean(a.Command, MaxCommand)
		a.Path = Clean(a.Path, MaxPath)
		a.URL = Clean(a.URL, MaxURL)
		if a.MCP != nil {
			a.MCP.Server, a.MCP.Tool = Clean(a.MCP.Server, MaxMCPName), Clean(a.MCP.Tool, MaxMCPName)
		}
	}
	r.Rule = Clean(r.Rule, MaxRule)
	if r.Model != nil {
		*r.Model = cleanPtr(*r.Model, MaxModel)
	}
	if r.PermissionMode != nil {
		*r.PermissionMode = cleanPtr(*r.PermissionMode, MaxPermissionMode)
	}
	if s := r.Subagent; s != nil {
		s.ID, s.Type = Clean(s.ID, MaxSubagent), Clean(s.Type, MaxSubagent)
	}
	if c := r.Config; c != nil {
		c.Source, c.Path = cleanPtr(c.Source, MaxConfigSource), cleanPtr(c.Path, MaxConfigPath)
	}
}

func cleanPtr(s *string, max int) *string {
	if s == nil {
		return nil
	}
	c := Clean(*s, max)
	return &c
}

// MaxSafeInteger is the largest integer JavaScript (and so the server) holds exactly.
const MaxSafeInteger = 1<<53 - 1

// Seal fills seq, prev and hash, after cutting every field to its limit. The hash covers
// exactly the JSON that will be written.
func (r *Record) Seal(seq int64, prev string) error {
	r.Limit()
	if r.DurationMS != nil && (*r.DurationMS < 0 || *r.DurationMS > MaxSafeInteger) {
		d := min(max(*r.DurationMS, 0), MaxSafeInteger)
		r.DurationMS = &d
	}
	r.Seq, r.Prev, r.Hash = seq, prev, ""
	h, err := ComputeHash(r)
	if err != nil {
		return err
	}
	r.Hash = h
	return nil
}

// ComputeHash returns SHA-256 over the canonical JSON of the record without its hash field.
func ComputeHash(r *Record) (string, error) {
	raw, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	return HashJSON(raw)
}

// HashJSON hashes any serialized record the same way the server does: parse, drop "hash", canonicalize.
func HashJSON(raw []byte) (string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v map[string]any
	if err := dec.Decode(&v); err != nil {
		return "", err
	}
	delete(v, "hash")
	canon, err := Canonical(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canon)
	return hex.EncodeToString(sum[:]), nil
}

// SHA256Hex is the hash format used everywhere in the record.
func SHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Clean makes a string safe to store: valid UTF-8 with no NUL (the server's database can't
// hold one, so it would refuse the record), and at most max bytes, cut on a rune boundary;
// max 0 doesn't cut. Both become U+FFFD, as JSON decoding does with a lone surrogate.
func Clean(s string, max int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "�")
	}
	if max > 0 && len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	return s
}

// Validate checks the invariants the server will check. It is not a full schema validator.
func (r *Record) Validate() error {
	switch {
	case r.V != 1:
		return fmt.Errorf("v must be 1")
	case len(r.ID) != 36:
		return fmt.Errorf("bad id")
	case r.Seq < 1:
		return fmt.Errorf("seq must be >= 1")
	case len(r.Prev) != 64 || len(r.Hash) != 64:
		return fmt.Errorf("bad prev or hash")
	case r.Device == "" || r.Session == "":
		return fmt.Errorf("device and session are required")
	}
	switch r.Event {
	case "session_start", "session_end", "prompt", "subagent_start", "subagent_stop", "config_change":
	case "action":
		if r.Action == nil || r.Outcome == "" {
			return fmt.Errorf("action records need action and outcome")
		}
	default:
		return fmt.Errorf("unknown event %q", r.Event)
	}
	return nil
}
