package compare

import (
	"strings"
	"testing"
)

func TestTheGitHubTableIsThePublishedOne(t *testing.T) {
	if len(githubAPI) != 203 {
		t.Fatalf("githubAPI has %d routes, want 203", len(githubAPI))
	}
	if first := githubAPI[0]; first != (route{"GET", "/authorizations"}) {
		t.Errorf("first route = %v, want GET /authorizations", first)
	}
	if last := githubAPI[len(githubAPI)-1]; last != (route{"DELETE", "/user/keys/:id"}) {
		t.Errorf("last route = %v, want DELETE /user/keys/:id", last)
	}
}

func TestEveryFrameworkAnswersEveryScenarioAlike(t *testing.T) {
	for _, tg := range Targets() {
		for _, s := range Scenarios() {
			t.Run(tg.Name+"/"+s.Name, func(t *testing.T) {
				if err := Check(tg, s); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

// TestEveryFrameworkServesTheWholeGitHubTable sends one request to every route,
// so that a route one framework silently failed to register cannot hide behind
// the githubapi scenario's single lookup.
func TestEveryFrameworkServesTheWholeGitHubTable(t *testing.T) {
	for _, tg := range Targets() {
		t.Run(tg.Name, func(t *testing.T) {
			srv := tg.Build(GitHub)
			for _, rt := range githubAPI {
				path := fillParams(rt.Path)
				if status, body := Do(srv, rt.Method, path, nil); status != 200 || body != "ok" {
					t.Errorf("%s %s: got %d %q, want 200 %q", rt.Method, path, status, body, "ok")
				}
			}
		})
	}
}

// fillParams replaces each :name segment with a concrete value.
func fillParams(pattern string) string {
	parts := strings.Split(pattern, "/")
	for i, p := range parts {
		if strings.HasPrefix(p, ":") {
			parts[i] = "v" + p[1:]
		}
	}
	return strings.Join(parts, "/")
}
