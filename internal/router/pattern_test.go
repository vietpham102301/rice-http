package router

import (
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
		{"/a/:x1/:x2/:x3/:x4/:x5/:x6/:x7/:x8/:x9", "at most"},
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

func TestParsePatternAllowsExactlyMaxParams(t *testing.T) {
	pattern := ""
	for i := 0; i < MaxParams; i++ {
		pattern += "/a/:p" + string(rune('0'+i))
	}

	segs, err := parsePattern(pattern)
	if err != nil {
		t.Fatalf("parsePattern(%q) returned %v, want nil for exactly MaxParams parameters", pattern, err)
	}

	params := 0
	for _, s := range segs {
		if s.kind == segParam {
			params++
		}
	}
	if params != MaxParams {
		t.Errorf("parsed %d parameters, want %d", params, MaxParams)
	}
}

// TestParsePatternCountsAWildcardTowardTheLimit records the rule: a wildcard
// occupies a Params slot exactly as a named parameter does.
func TestParsePatternCountsAWildcardTowardTheLimit(t *testing.T) {
	pattern := "/a/:p0/:p1/:p2/:p3/:p4/:p5/:p6/:p7/*rest"

	if _, err := parsePattern(pattern); err == nil {
		t.Error("parsePattern accepted MaxParams parameters plus a wildcard, want a rejection")
	}
}
