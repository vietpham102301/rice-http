package router

import (
	"fmt"
	"net/url"
	"strings"
)

// segKind distinguishes the three things a pattern can contain.
type segKind uint8

const (
	// segStatic is literal text, including any slashes around it.
	segStatic segKind = iota
	// segParam is a ":name" placeholder matching one path segment.
	segParam
	// segWildcard is a "*name" catch-all matching the remainder of the path.
	segWildcard
)

// segment is one piece of a parsed pattern. For segStatic, text is the literal
// run including slashes; for the other two it is the bare name, without ':' or
// '*'.
type segment struct {
	kind segKind
	text string
}

// parsePattern splits a route pattern into segments, rejecting anything
// malformed, ambiguous, or unable to match.
//
// The last category is the subtle one. fasthttp collapses empty segments,
// resolves dot segments and percent-decodes the path before rice sees it, so
// matching happens in normalised decoded space. A pattern given in any other
// form would register cleanly and then never match a single request. Rejecting
// it here puts the failure at the registration that caused it, which is the same
// reasoning that makes a duplicate route fatal. See the M3 design doc, D8.
func parsePattern(pattern string) ([]segment, error) {
	if pattern == "" {
		return nil, fmt.Errorf("route path is empty")
	}
	if pattern[0] != '/' {
		return nil, fmt.Errorf("route path %s does not begin with /", pattern)
	}
	if err := checkNormalised(pattern); err != nil {
		return nil, err
	}

	var (
		segs  []segment
		names []string
		start int
	)

	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		if c != ':' && c != '*' {
			continue
		}

		// Flush the static run preceding this marker. It is never empty: a
		// pattern begins with '/', and checkNormalised has already rejected an
		// empty segment, so a marker is always preceded by at least a slash.
		segs = append(segs, segment{segStatic, pattern[start:i]})

		name, next := scanName(pattern, i+1)

		if c == ':' {
			if name == "" {
				return nil, fmt.Errorf("route path %s has an empty parameter name", pattern)
			}
			if !isValidParamName(name) {
				return nil, fmt.Errorf("route path %s has a parameter name %q with a character outside a-z, A-Z, 0-9 and _; a literal suffix such as an extension cannot follow a parameter directly, so write it as its own static segment, e.g. /files/:name/txt or a route with no parameter in that position", pattern, name)
			}
			segs = append(segs, segment{segParam, name})
		} else {
			if name == "" {
				return nil, fmt.Errorf("route path %s has an empty wildcard name", pattern)
			}
			if next != len(pattern) {
				return nil, fmt.Errorf("route path %s has a wildcard that is not at the end; a wildcard must be the last segment", pattern)
			}
			if !isValidParamName(name) {
				return nil, fmt.Errorf("route path %s has a wildcard name %q with a character outside a-z, A-Z, 0-9 and _; a wildcard consumes the rest of the path, so nothing can follow it, including a literal suffix such as an extension", pattern, name)
			}
			segs = append(segs, segment{segWildcard, name})
		}

		for _, prior := range names {
			if prior == name {
				return nil, fmt.Errorf("route path %s declares the name %s twice; each parameter name may appear only once", pattern, name)
			}
		}
		names = append(names, name)

		start = next
		i = next - 1
	}

	if start < len(pattern) {
		segs = append(segs, segment{segStatic, pattern[start:]})
	}

	return segs, nil
}

// scanName reads a parameter or wildcard name starting at i, stopping at the
// next '/' or the end of the pattern. It returns the name and the index just
// past it.
func scanName(pattern string, i int) (string, int) {
	j := i
	for j < len(pattern) && pattern[j] != '/' {
		j++
	}
	return pattern[i:j], j
}

// isValidParamName reports whether name is made up only of letters, digits and
// underscore.
//
// scanName stops at the next '/', not at the first character that could not
// belong to a Go-style identifier, so ":name.txt" scans a name of
// "name.txt" rather than treating ".txt" as a literal suffix. Accepting that
// silently registers a route where c.Param("name") always returns empty and
// the ".txt" is never required, so the mistake is rejected here instead. See
// the M3 final review, finding 1.
func isValidParamName(name string) bool {
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= 'A' && c <= 'Z':
		case c >= '0' && c <= '9':
		case c == '_':
		default:
			return false
		}
	}
	return true
}

// checkNormalised rejects patterns that fasthttp's own normalisation would
// prevent from ever matching.
func checkNormalised(pattern string) error {
	if strings.Contains(pattern, "//") {
		return fmt.Errorf("route path %s has an empty segment; register %s", pattern, collapseSlashes(pattern))
	}
	for _, seg := range strings.Split(pattern, "/") {
		if seg == "." || seg == ".." {
			return fmt.Errorf("route path %s has a dot segment; register the resolved path", pattern)
		}
	}
	if hasPercentEscape(pattern) {
		if decoded, err := url.PathUnescape(pattern); err == nil && decoded != pattern {
			return fmt.Errorf("route path %s is percent-encoded; register the decoded form %s", pattern, decoded)
		}
		return fmt.Errorf("route path %s is percent-encoded; register the path in decoded form", pattern)
	}
	return nil
}

// hasPercentEscape reports whether s contains a complete percent escape. A bare
// '%' is a legitimate path character and is left alone.
func hasPercentEscape(s string) bool {
	for i := 0; i+2 < len(s); i++ {
		if s[i] == '%' && isHex(s[i+1]) && isHex(s[i+2]) {
			return true
		}
	}
	return false
}

func isHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// collapseSlashes squeezes runs of '/' down to one, so the error message can
// show the caller what to write instead.
func collapseSlashes(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '/' && i+1 < len(s) && s[i+1] == '/' {
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
