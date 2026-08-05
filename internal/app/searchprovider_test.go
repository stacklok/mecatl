package app

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	refsearch "github.com/stacklok/mecatl/engine/adapter/search"
	"github.com/stacklok/mecatl/engine/port"
	"github.com/stacklok/mecatl/engine/tool"
)

// searchDiag is a port.Diagnostics double recording the message text AND the
// flattened key/value args, so the precedence test can both pin the per-branch
// INFO line and assert no secret ever appears in any log field.
type searchDiag struct {
	mu    sync.Mutex
	lines []string
}

func (d *searchDiag) Log(_ context.Context, _ port.Level, msg string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	b := strings.Builder{}
	b.WriteString(msg)
	for _, a := range args {
		b.WriteString(" ")
		b.WriteString(stringify(a))
	}
	d.lines = append(d.lines, b.String())
}

func (d *searchDiag) With(...any) port.Diagnostics { return d }

func (d *searchDiag) all() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return strings.Join(d.lines, "\n")
}

func stringify(a any) string {
	switch v := a.(type) {
	case string:
		return v
	default:
		return ""
	}
}

// TestBuildSearchProviderPrecedence walks the issue-#26 backend ladder: first
// match wins, web search ON by default (Exa). It asserts the resolved concrete
// provider type per branch AND that no secret key leaks into any diagnostic line.
//
// MUTATION-VERIFY (the default arm): delete the Exa default branch (return
// refsearch.Unavailable{} instead) and the "nothing set => Exa default" sub-case
// fails — the zero-config default is then no longer wired.
func TestBuildSearchProviderPrecedence(t *testing.T) {
	const braveKey = "brave-secret-key"
	const exaKey = "exa-secret-key"

	t.Run("nothing set => Exa anonymous default", func(t *testing.T) {
		d := &searchDiag{}
		p := buildSearchProvider(context.Background(), Config{Diagnostics: d})
		exa, ok := p.(*refsearch.ExaProvider)
		if !ok {
			t.Fatalf("default should be *ExaProvider, got %T", p)
		}
		if exa.PaidTier() {
			t.Fatal("no EXA_API_KEY => anonymous (not paid) tier")
		}
		if !strings.Contains(d.all(), "Exa anonymous default") {
			t.Fatalf("expected the Exa-default INFO line; got:\n%s", d.all())
		}
	})

	t.Run("EXA_API_KEY => Exa paid tier, key never logged", func(t *testing.T) {
		d := &searchDiag{}
		p := buildSearchProvider(context.Background(), Config{Diagnostics: d, ExaAPIKey: exaKey})
		exa, ok := p.(*refsearch.ExaProvider)
		if !ok {
			t.Fatalf("expected *ExaProvider, got %T", p)
		}
		if !exa.PaidTier() {
			t.Fatal("EXA_API_KEY set => paid tier")
		}
		if strings.Contains(d.all(), exaKey) {
			t.Fatalf("EXA key leaked into diagnostics:\n%s", d.all())
		}
	})

	t.Run("BRAVE_API_KEY => HTTP provider (Brave), key never logged", func(t *testing.T) {
		d := &searchDiag{}
		p := buildSearchProvider(context.Background(), Config{Diagnostics: d, BraveAPIKey: braveKey})
		if _, ok := p.(*refsearch.HTTPProvider); !ok {
			t.Fatalf("BRAVE_API_KEY should resolve to *HTTPProvider, got %T", p)
		}
		if !strings.Contains(d.all(), "Brave backend") || !strings.Contains(d.all(), braveSearchEndpoint) {
			t.Fatalf("expected the Brave INFO line + endpoint; got:\n%s", d.all())
		}
		if strings.Contains(d.all(), braveKey) {
			t.Fatalf("Brave key leaked into diagnostics:\n%s", d.all())
		}
	})

	t.Run("SEARXNG_URL => HTTP provider (SearXNG)", func(t *testing.T) {
		d := &searchDiag{}
		p := buildSearchProvider(context.Background(), Config{Diagnostics: d, SearXNGURL: "https://searx.example/search"})
		if _, ok := p.(*refsearch.HTTPProvider); !ok {
			t.Fatalf("SEARXNG_URL should resolve to *HTTPProvider, got %T", p)
		}
		if !strings.Contains(d.all(), "SearXNG backend") {
			t.Fatalf("expected the SearXNG INFO line; got:\n%s", d.all())
		}
	})

	t.Run("SEARXNG_URL beats BRAVE_API_KEY", func(t *testing.T) {
		d := &searchDiag{}
		p := buildSearchProvider(context.Background(), Config{
			Diagnostics: d, SearXNGURL: "https://searx.example/search", BraveAPIKey: braveKey,
		})
		if _, ok := p.(*refsearch.HTTPProvider); !ok {
			t.Fatalf("expected *HTTPProvider, got %T", p)
		}
		if !strings.Contains(d.all(), "SearXNG backend") || strings.Contains(d.all(), "Brave backend") {
			t.Fatalf("SearXNG must win over Brave; got:\n%s", d.all())
		}
	})

	t.Run("--websearch-url wins over env tiers", func(t *testing.T) {
		d := &searchDiag{}
		p := buildSearchProvider(context.Background(), Config{
			Diagnostics:  d,
			WebSearchURL: "https://explicit.example/search",
			SearXNGURL:   "https://searx.example/search",
			BraveAPIKey:  braveKey,
		})
		if _, ok := p.(*refsearch.HTTPProvider); !ok {
			t.Fatalf("expected *HTTPProvider, got %T", p)
		}
		if !strings.Contains(d.all(), "explicit HTTP backend") {
			t.Fatalf("explicit --websearch-url must win; got:\n%s", d.all())
		}
	})

	t.Run("misconfigured --websearch-url => BackendDown (NOT disabled)", func(t *testing.T) {
		d := &searchDiag{}
		// A non-empty but malformed URL: it passes the switch's trim check (so the
		// branch is entered) but fails NewHTTPProvider construction.
		p := buildSearchProvider(context.Background(), Config{Diagnostics: d, WebSearchURL: "://broken"})
		if _, ok := p.(refsearch.BackendDown); !ok {
			t.Fatalf("a construction failure must resolve to BackendDown, got %T", p)
		}
		// The tool would see backend-down, NOT disabled.
		_, err := p.Search(context.Background(), tool.SearchQuery{Query: "anything"})
		if !errors.Is(err, tool.ErrSearchBackendDown) {
			t.Fatalf("misconfigured backend should yield ErrSearchBackendDown, got %v", err)
		}
		if errors.Is(err, tool.ErrSearchUnavailable) {
			t.Fatalf("misconfigured backend must NOT yield ErrSearchUnavailable (the disabled cause), got %v", err)
		}
	})

	t.Run("misconfigured SEARXNG_URL => BackendDown (NOT disabled)", func(t *testing.T) {
		d := &searchDiag{}
		p := buildSearchProvider(context.Background(), Config{Diagnostics: d, SearXNGURL: "not-a-url"})
		if _, ok := p.(refsearch.BackendDown); !ok {
			t.Fatalf("a SearXNG construction failure must resolve to BackendDown, got %T", p)
		}
		_, err := p.Search(context.Background(), tool.SearchQuery{Query: "anything"})
		if !errors.Is(err, tool.ErrSearchBackendDown) {
			t.Fatalf("misconfigured SearXNG should yield ErrSearchBackendDown, got %v", err)
		}
	})

	t.Run("WebSearchOff (kill switch) => Unavailable, beats everything", func(t *testing.T) {
		d := &searchDiag{}
		p := buildSearchProvider(context.Background(), Config{
			Diagnostics:  d,
			WebSearchOff: true,
			WebSearchURL: "https://explicit.example/search",
			SearXNGURL:   "https://searx.example/search",
			BraveAPIKey:  braveKey,
			ExaAPIKey:    exaKey,
		})
		if _, ok := p.(refsearch.Unavailable); !ok {
			t.Fatalf("kill switch should resolve to refsearch.Unavailable, got %T", p)
		}
		if !strings.Contains(d.all(), "DISABLED by operator") {
			t.Fatalf("expected the DISABLED INFO line; got:\n%s", d.all())
		}
		// Even with keys present, no secret may appear (the kill switch builds nothing).
		if strings.Contains(d.all(), braveKey) || strings.Contains(d.all(), exaKey) {
			t.Fatalf("a key leaked into diagnostics on the kill-switch path:\n%s", d.all())
		}
	})
}
