package event

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// The vectors are produced by schema/testdata/gen-vectors.mjs, an independent JavaScript
// implementation, so this test proves the Go recorder and the TypeScript server agree.
func TestCanonicalMatchesJavaScriptReference(t *testing.T) {
	raw, err := os.ReadFile("../../schema/testdata/canonical-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		Canonical string          `json:"canonical"`
		SHA256    string          `json:"sha256"`
	}
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) < 4 {
		t.Fatalf("expected at least 4 vectors, got %d", len(vectors))
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			dec := json.NewDecoder(bytes.NewReader(v.Input))
			dec.UseNumber()
			var parsed map[string]any
			if err := dec.Decode(&parsed); err != nil {
				t.Fatal(err)
			}
			delete(parsed, "hash")
			got, err := Canonical(parsed)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != v.Canonical {
				t.Fatalf("canonical mismatch\n got: %s\nwant: %s", got, v.Canonical)
			}
			if h, _ := HashJSON(v.Input); h != v.SHA256 {
				t.Fatalf("hash mismatch: got %s want %s", h, v.SHA256)
			}
		})
	}
}

func TestSealCoversExactlyWhatIsWritten(t *testing.T) {
	r := &Record{V: 1, ID: NewID(time.Now()), TS: Timestamp(time.Now()), Device: "d", Tool: "claude-code",
		Session: "s", Repo: Repo{RootHash: strings.Repeat("a", 64)}, Event: "action",
		Action:  &Action{Kind: "shell", Name: "Bash", Command: "ls <dir> &  ", InputHash: strings.Repeat("b", 64)},
		Outcome: "ran", Model: Optional(nil)}
	if err := r.Seal(7, Genesis); err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(r)
	again, err := HashJSON(line)
	if err != nil {
		t.Fatal(err)
	}
	if again != r.Hash || r.Seq != 7 || r.Prev != Genesis {
		t.Fatalf("hash of written line %s != sealed %s", again, r.Hash)
	}
	if !bytes.Contains(line, []byte(`"model":null`)) {
		t.Fatalf("explicit null model should be written: %s", line)
	}
	if bytes.Contains(line, []byte(`"permission_mode"`)) {
		t.Fatalf("absent permission_mode should be omitted: %s", line)
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	// Any change to a covered field changes the hash.
	sealed := r.Hash
	r.Action.Command = "ls"
	if h, _ := ComputeHash(r); h == sealed {
		t.Fatal("changing the command did not change the hash")
	}
}

func TestCanonicalRejectsNonIntegers(t *testing.T) {
	if _, err := Canonical(map[string]any{"x": json.Number("1.5")}); err == nil {
		t.Fatal("expected an error for a fractional number")
	}
}

func TestNewIDIsV7AndSortable(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	a := NewID(time.UnixMilli(1_700_000_000_000))
	b := NewID(time.UnixMilli(1_700_000_000_001))
	if !re.MatchString(a) || !re.MatchString(b) {
		t.Fatalf("not UUIDv7: %s %s", a, b)
	}
	if a >= b {
		t.Fatalf("ids should sort by time: %s >= %s", a, b)
	}
}

func TestCleanKeepsUTF8Valid(t *testing.T) {
	if got := Clean("ab\xffcd", 0); got != "ab�cd" {
		t.Fatalf("got %q", got)
	}
	if got := Clean("héllo", 2); got != "h" {
		t.Fatalf("cut inside a rune: %q", got)
	}
	// QA2-REC-7: the server refuses a record with a NUL in any field.
	if got := Clean("printf 'a\x00b'", 0); got != "printf 'a\uFFFDb'" {
		t.Fatalf("NUL kept: %q", got)
	}
}

// Every string field the schema limits is cut to that limit, whatever the input, so one
// oversized field can't produce a record the log reader or the server refuses (QA REC-1).
func TestSealCapsEveryFieldToTheSchema(t *testing.T) {
	raw, err := os.ReadFile("../../schema/agent-event.v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	limits := map[string]int{} // dotted path -> maxLength
	var walk func(prefix string, props map[string]any)
	walk = func(prefix string, props map[string]any) {
		for k, v := range props {
			p, _ := v.(map[string]any)
			if n, ok := p["maxLength"].(float64); ok {
				limits[prefix+k] = int(n)
			}
			if sub, ok := p["properties"].(map[string]any); ok {
				walk(prefix+k+".", sub)
			}
		}
	}
	walk("", schema["properties"].(map[string]any))
	if len(limits) < 15 {
		t.Fatalf("found only %d limited fields in the schema", len(limits))
	}

	big := "\x00" + strings.Repeat("é", 6000) + "\xff" // 12 KB, multibyte, with a NUL and invalid UTF-8
	s := func() *string { v := big; return &v }
	d := int64(1) << 60
	r := &Record{V: 1, ID: NewID(time.Now()), TS: Timestamp(time.Now()), Device: big, User: s(), Tool: "cursor",
		ToolVersion: s(), Session: big, PromptID: s(), Repo: Repo{Remote: s(), RootHash: strings.Repeat("a", 64)},
		Event: "action", Action: &Action{Kind: "mcp", Name: big, Command: big, Path: big, URL: big,
			MCP: &MCP{Server: big, Tool: big}, InputHash: strings.Repeat("b", 64)},
		Outcome: "ran", DurationMS: &d, Rule: big, Model: Optional(s()), PermissionMode: Optional(s()),
		Subagent: &Subagent{ID: big, Type: big}, Config: &Config{Source: s(), Path: s()}}
	if err := r.Seal(1, Genesis); err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(r)
	var got map[string]any
	json.Unmarshal(line, &got)
	for path, max := range limits {
		var v any = got
		for _, k := range strings.Split(path, ".") {
			m, _ := v.(map[string]any)
			v = m[k]
		}
		str, ok := v.(string)
		if !ok {
			t.Errorf("%s: test record doesn't set it (got %T)", path, v)
			continue
		}
		if n := len([]rune(str)); n > max || n == 0 || !utf8.ValidString(str) || strings.ContainsRune(str, 0) {
			t.Errorf("%s: %d characters, schema allows %d", path, n, max)
		}
	}
	if *r.DurationMS != MaxSafeInteger {
		t.Errorf("duration_ms %d not clamped to a safe integer", *r.DurationMS)
	}
	if h, err := HashJSON(line); err != nil || h != r.Hash {
		t.Fatalf("sealed hash doesn't cover the capped record: %v", err)
	}
}
