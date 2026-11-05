package apierr

import (
	"strings"
	"testing"
)

func TestMessages(t *testing.T) {
	for _, c := range []struct {
		body, want string
	}{
		{`{"error":"device limit reached for your plan"}`, "device limit reached for your plan (HTTP 402"},
		{`{"error":"invalid_grant","error_description":"code already used"}`, "invalid_grant: code already used"},
		{`{"error":{"message":"bad origin"}}`, "bad origin"},
		{`upstream timed out`, "upstream timed out"},
		{`<html>502</html>`, "returned HTTP 502"},
	} {
		status := 402
		if strings.HasPrefix(c.body, "<") {
			status = 502
		}
		if got := New("https://app.example/x", status, []byte(c.body)).Error(); !strings.Contains(got, c.want) {
			t.Errorf("%s: got %q", c.body, got)
		}
	}
	if !New("u", 503, nil).Temporary() || New("u", 400, nil).Temporary() {
		t.Fatal("Temporary")
	}
}
