package adapter

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/tektonsoftware/sigdek/internal/event"
)

// hookInput is a hook's input object decoded one field at a time, so a field of an unexpected
// type ("duration_ms":"5", a numeric session_id) costs at most that field, never the event.
type hookInput map[string]json.RawMessage

// decodePayload reads the hook input. complete is false when the input was cut off (over the
// size limit, or stdin never closed mid-JSON); then the fields that arrived are kept.
func decodePayload(raw []byte, complete bool) (hookInput, error) {
	raw = bytes.TrimPrefix(bytes.TrimSpace(raw), []byte("\xef\xbb\xbf"))
	if len(raw) == 0 {
		return nil, errors.New("empty hook input")
	}
	if !complete {
		if p := partialObject(raw); len(p) > 0 {
			return p, nil
		}
		return nil, errors.New("hook input was cut off before any field arrived")
	}
	var p hookInput
	if err := json.Unmarshal(raw, &p); err != nil {
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			// Nested deeper than encoding/json goes (10,000 levels), or broken after some
			// fields: keep the fields that read, so Block rules still apply.
			if p := partialObject(raw); len(p) > 0 {
				return p, nil
			}
			return nil, fmt.Errorf("hook input is not JSON (%v)", err)
		}
		return nil, errors.New("hook input is not a JSON object")
	}
	if p == nil {
		return nil, errors.New("hook input is null, not a JSON object")
	}
	return p, nil
}

// str returns the first of keys holding a non-empty string, or a number's digits (some ids
// arrive as numbers).
func (p hookInput) str(keys ...string) string {
	for _, k := range keys {
		v, ok := p[k]
		if !ok || len(v) == 0 {
			continue
		}
		switch v[0] {
		case '"':
			var s string
			if json.Unmarshal(v, &s) == nil && s != "" {
				return s
			}
		case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
			return string(v)
		}
	}
	return ""
}

// nullable distinguishes an absent field (nil) from an explicit null (pointer to nil).
func (p hookInput) nullable(key string) **string {
	v, ok := p[key]
	if !ok {
		return nil
	}
	if string(v) == "null" {
		var none *string
		return &none
	}
	s := p.str(key)
	if s == "" {
		return nil
	}
	return event.Optional(&s)
}

// num reads a number, or a string holding one.
func (p hookInput) num(keys ...string) *float64 {
	for _, k := range keys {
		v, ok := p[k]
		if !ok {
			continue
		}
		var f float64
		if json.Unmarshal(v, &f) == nil {
			return &f
		}
		var s string
		if json.Unmarshal(v, &s) == nil {
			if f, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil && !math.IsNaN(f) {
				return &f
			}
		}
	}
	return nil
}

func (p hookInput) value(key string) any {
	var v any
	if raw, ok := p[key]; ok {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}

func (p hookInput) strs(key string) []string {
	var out []string
	if arr, ok := p.value(key).([]any); ok {
		for _, x := range arr {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

// partialObject decodes as much of a cut-off JSON object as arrived. Complete fields are
// kept as they are; a field cut off mid-value keeps what arrived (a string's first part, an
// object's complete members), re-encoded as JSON.
func partialObject(b []byte) hookInput {
	d := &partial{b: b}
	d.ws()
	if !d.eat('{') {
		return nil
	}
	m, _ := d.object(0)
	out := hookInput{}
	for k, v := range m {
		if raw, err := json.Marshal(v); err == nil {
			out[k] = raw
		}
	}
	return out
}

type partial struct {
	b []byte
	i int
}

const maxPartialDepth = 64

func (d *partial) ws() {
	for d.i < len(d.b) && (d.b[d.i] == ' ' || d.b[d.i] == '\t' || d.b[d.i] == '\n' || d.b[d.i] == '\r') {
		d.i++
	}
}

func (d *partial) eat(c byte) bool {
	if d.i < len(d.b) && d.b[d.i] == c {
		d.i++
		return true
	}
	return false
}

// value returns the next value and whether it was complete; nil, false at a cut or garbage.
// A value nested deeper than maxPartialDepth is skipped (nil, true) so the fields after it
// are still read.
func (d *partial) value(depth int) (any, bool) {
	d.ws()
	if d.i >= len(d.b) {
		return nil, false
	}
	if depth > maxPartialDepth {
		return nil, d.skip()
	}
	switch c := d.b[d.i]; {
	case c == '{':
		d.i++
		return d.object(depth + 1)
	case c == '[':
		d.i++
		return d.array(depth + 1)
	case c == '"':
		d.i++
		return d.str()
	case c == 't' || c == 'f' || c == 'n':
		for _, lit := range []struct {
			s string
			v any
		}{{"true", true}, {"false", false}, {"null", nil}} {
			if bytes.HasPrefix(d.b[d.i:], []byte(lit.s)) {
				d.i += len(lit.s)
				return lit.v, true
			}
		}
		return nil, false
	case c == '-' || (c >= '0' && c <= '9'):
		j := d.i
		for j < len(d.b) && strings.IndexByte("+-.eE0123456789", d.b[j]) >= 0 {
			j++
		}
		if j == len(d.b) {
			return nil, false // the digits may have been cut
		}
		n := json.Number(d.b[d.i:j])
		d.i = j
		if _, err := n.Float64(); err != nil {
			return nil, false
		}
		return n, true
	}
	return nil, false
}

// skip passes over one value however deeply nested, and reports whether it was complete.
func (d *partial) skip() bool {
	open := 0
	for d.i < len(d.b) {
		switch c := d.b[d.i]; c {
		case '"':
			d.i++
			if _, ok := d.str(); !ok {
				return false
			}
			if open == 0 {
				return true
			}
			continue
		case '[', '{':
			open++
		case ']', '}':
			if open == 0 {
				return true // the end of the enclosing value: an empty skip
			}
			if open--; open == 0 {
				d.i++
				return true
			}
		case ',':
			if open == 0 {
				return true
			}
		}
		d.i++
	}
	return false
}

func (d *partial) object(depth int) (map[string]any, bool) {
	m := map[string]any{}
	for {
		d.ws()
		if d.eat('}') {
			return m, true
		}
		if !d.eat('"') {
			return m, false
		}
		k, ok := d.str()
		if !ok {
			return m, false
		}
		d.ws()
		if !d.eat(':') {
			return m, false
		}
		v, ok := d.value(depth)
		if v != nil || ok {
			m[k] = v
		}
		if !ok {
			return m, false
		}
		d.ws()
		if !d.eat(',') {
			return m, d.eat('}')
		}
	}
}

func (d *partial) array(depth int) ([]any, bool) {
	a := []any{}
	for {
		d.ws()
		if d.eat(']') {
			return a, true
		}
		v, ok := d.value(depth)
		if v != nil || ok {
			a = append(a, v)
		}
		if !ok {
			return a, false
		}
		d.ws()
		if !d.eat(',') {
			return a, d.eat(']')
		}
	}
}

// str reads a string after its opening quote; at a cut it returns what arrived.
func (d *partial) str() (string, bool) {
	var sb strings.Builder
	for d.i < len(d.b) {
		c := d.b[d.i]
		switch {
		case c == '"':
			d.i++
			return sb.String(), true
		case c != '\\':
			_, size := utf8.DecodeRune(d.b[d.i:])
			sb.Write(d.b[d.i : d.i+size])
			d.i += size
			continue
		}
		if d.i+1 >= len(d.b) {
			return sb.String(), false
		}
		e := d.b[d.i+1]
		d.i += 2
		switch e {
		case 'b':
			sb.WriteByte('\b')
		case 'f':
			sb.WriteByte('\f')
		case 'n':
			sb.WriteByte('\n')
		case 'r':
			sb.WriteByte('\r')
		case 't':
			sb.WriteByte('\t')
		case 'u':
			r, ok := d.hex4()
			if !ok {
				return sb.String(), false
			}
			if utf16.IsSurrogate(r) && d.i+1 < len(d.b) && d.b[d.i] == '\\' && d.b[d.i+1] == 'u' {
				d.i += 2
				r2, ok := d.hex4()
				if !ok {
					return sb.String(), false
				}
				r = utf16.DecodeRune(r, r2)
			}
			sb.WriteRune(r)
		default:
			sb.WriteByte(e) // \" \\ \/
		}
	}
	return sb.String(), false
}

func (d *partial) hex4() (rune, bool) {
	if d.i+4 > len(d.b) {
		return 0, false
	}
	n, err := strconv.ParseUint(string(d.b[d.i:d.i+4]), 16, 32)
	if err != nil {
		return 0, false
	}
	d.i += 4
	return rune(n), true
}
