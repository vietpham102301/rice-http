package rice

import "testing"

func TestMethodIndexMapsEveryCommonVerb(t *testing.T) {
	cases := []struct {
		verb string
		want method
	}{
		{"GET", mGET},
		{"POST", mPOST},
		{"PUT", mPUT},
		{"PATCH", mPATCH},
		{"DELETE", mDELETE},
		{"HEAD", mHEAD},
		{"OPTIONS", mOPTIONS},
	}

	for _, c := range cases {
		got, ok := methodIndex([]byte(c.verb))
		if !ok {
			t.Errorf("methodIndex(%q) reported the verb as uncommon", c.verb)
			continue
		}
		if got != c.want {
			t.Errorf("methodIndex(%q) = %d, want %d", c.verb, got, c.want)
		}
	}
}

func TestMethodIndexRejectsUncommonVerbs(t *testing.T) {
	for _, verb := range []string{"PROPFIND", "TRACE", "CONNECT", "", "get", "GETX", "GE"} {
		if _, ok := methodIndex([]byte(verb)); ok {
			t.Errorf("methodIndex(%q) claimed the verb is common, want false", verb)
		}
	}
}

// TestEveryCommonVerbHasADistinctIndex guards the fixed-size array: two verbs
// sharing an index would silently route one verb's traffic to the other's tree.
func TestEveryCommonVerbHasADistinctIndex(t *testing.T) {
	seen := make(map[method]string)

	for _, verb := range []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"} {
		i, ok := methodIndex([]byte(verb))
		if !ok {
			t.Fatalf("methodIndex(%q) reported the verb as uncommon", verb)
		}
		if other, clash := seen[i]; clash {
			t.Errorf("%q and %q both map to index %d", verb, other, i)
		}
		seen[i] = verb

		if i >= methodCount {
			t.Errorf("methodIndex(%q) = %d, which is outside the trees array of size %d", verb, i, methodCount)
		}
	}

	if len(seen) != int(methodCount) {
		t.Errorf("%d verbs map to indices but methodCount is %d", len(seen), methodCount)
	}
}

func TestAllocBudgetMethodIndex(t *testing.T) {
	verbs := [][]byte{
		[]byte("GET"), []byte("POST"), []byte("PUT"), []byte("PATCH"),
		[]byte("DELETE"), []byte("HEAD"), []byte("OPTIONS"), []byte("PROPFIND"),
	}

	budget(t, "methodIndex", 0, func() {
		for _, v := range verbs {
			_, _ = methodIndex(v)
		}
	})
}
