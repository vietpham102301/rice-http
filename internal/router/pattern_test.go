package router

import (
	"strconv"
	"strings"
	"testing"
)

func TestParsePatternSplitsIntoSegments(t *testing.T) {
	cases := []struct {
		pattern string
		want    []segment
	}{
		{"/", []segment{{segStatic, "/"}}},
		{"/users", []segment{{segStatic, "/users"}}},
		{"/users/", []segment{{segStatic, "/users/"}}},
		{"/users/:id", []segment{{segStatic, "/users/"}, {segParam, "id"}}},
		{
			"/users/:id/posts/:pid",
			[]segment{{segStatic, "/users/"}, {segParam, "id"}, {segStatic, "/posts/"}, {segParam, "pid"}},
		},
		{"/users/:id/edit", []segment{{segStatic, "/users/"}, {segParam, "id"}, {segStatic, "/edit"}}},
		{"/files/*path", []segment{{segStatic, "/files/"}, {segWildcard, "path"}}},
		{"/*all", []segment{{segStatic, "/"}, {segWildcard, "all"}}},
		// A parameter marker need not be preceded by '/': the name is "ver",
		// terminated by the '/' before "users", not by anything about ":v".
		{"/v:ver/users", []segment{{segStatic, "/v"}, {segParam, "ver"}, {segStatic, "/users"}}},
	}

	for _, c := range cases {
		got, err := parsePattern(c.pattern)
		if err != nil {
			t.Errorf("parsePattern(%q) returned %v, want nil", c.pattern, err)
			continue
		}
		if len(got) != len(c.want) {
			t.Errorf("parsePattern(%q) produced %d segments, want %d: %+v", c.pattern, len(got), len(c.want), got)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parsePattern(%q) segment %d = %+v, want %+v", c.pattern, i, got[i], c.want[i])
			}
		}
	}
}

// rejected pairs a pattern with a phrase its error must contain, so the message
// is asserted to be useful rather than merely present.
func TestParsePatternRejects(t *testing.T) {
	cases := []struct {
		pattern string
		phrase  string
	}{
		{"", "empty"},
		{"users", "does not begin with /"},
		{"/a//b", "empty segment"},
		{"//", "empty segment"},
		{"/a/./b", "dot segment"},
		{"/a/../b", "dot segment"},
		{"/a/.", "dot segment"},
		{"/a/..", "dot segment"},
		{"/caf%C3%A9", "percent-encoded"},
		{"/users/:", "empty parameter name"},
		{"/users/:/edit", "empty parameter name"},
		{"/files/*", "empty wildcard name"},
		{"/files/*path/edit", "must be the last"},
		{"/a/:id/b/:id", "twice"},
		{"/files/:name.txt", "outside a-z, A-Z, 0-9 and _"},
		{"/u/:id-:name", "outside a-z, A-Z, 0-9 and _"},
		{"/f/*path.zip", "outside a-z, A-Z, 0-9 and _"},
	}

	for _, c := range cases {
		_, err := parsePattern(c.pattern)
		if err == nil {
			t.Errorf("parsePattern(%q) returned nil error, want a rejection", c.pattern)
			continue
		}
		if !strings.Contains(err.Error(), c.phrase) {
			t.Errorf("parsePattern(%q) error %q does not mention %q", c.pattern, err, c.phrase)
		}
		if !strings.Contains(err.Error(), c.pattern) && c.pattern != "" {
			t.Errorf("parsePattern(%q) error %q does not name the offending pattern", c.pattern, err)
		}
	}
}

// TestParsePatternSuggestsTheDecodedForm checks the one error that has to do
// work to be useful: telling the author what to write instead.
func TestParsePatternSuggestsTheDecodedForm(t *testing.T) {
	_, err := parsePattern("/caf%C3%A9")
	if err == nil {
		t.Fatal("parsePattern accepted a percent-encoded pattern")
	}
	if !strings.Contains(err.Error(), "/café") {
		t.Errorf("error %q should suggest the decoded form /café", err)
	}
}

// TestParsePatternAllowsABarePercent guards against over-rejecting: a literal
// percent sign that is not an escape sequence is a legitimate path character.
func TestParsePatternAllowsABarePercent(t *testing.T) {
	if _, err := parsePattern("/discount/100%"); err != nil {
		t.Errorf("parsePattern rejected a bare percent sign: %v", err)
	}
}

// TestParsePatternAcceptsManyParameters records M6's removal of the
// eight-parameter limit. The limit existed only because Params was a fixed
// array; it is a slice now.
func TestParsePatternAcceptsManyParameters(t *testing.T) {
	pattern := ""
	for i := 0; i < 20; i++ {
		pattern += "/a/:p" + strconv.Itoa(i)
	}
	pattern += "/*rest"

	if _, err := parsePattern(pattern); err != nil {
		t.Fatalf("parsePattern rejected 20 parameters and a wildcard: %v", err)
	}
}
