package card

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/open-grove/handoff/internal/types"
)

func TestSanitizeContextRedactsSecretsAndPaths(t *testing.T) {
	input := types.Context{
		Source: "codex",
		CWD:    "/Users/alice/work/demo",
		Messages: []types.Message{{
			Role: "user",
			Text: "api_key = \"super-secret-value\"\nAuthorization: Bearer abcdefghijklmnop\nkey：ark-1234567890-abcdefghijklmnop\nread /Users/alice/work/demo/main.go",
		}},
	}
	result := SanitizeContext(input)
	text := result.Messages[0].Text
	for _, forbidden := range []string{"super-secret-value", "abcdefghijklmnop", "ark-1234567890-abcdefghijklmnop", "/Users/alice"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized text contains %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "[REDACTED]") || !strings.Contains(text, "$HOME/work/demo/main.go") {
		t.Fatalf("sanitized text lost expected markers: %s", text)
	}
}

func TestBuildDeterministicHandoffHasStableContract(t *testing.T) {
	now := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	handoff, err := Build(context.Background(), nil, "abcdefghijklmnopqrstuv", "continue the CLI", types.Context{
		Source:   "stdin",
		Cursor:   "input:1",
		Messages: []types.Message{{Role: "user", Text: "The parser is complete."}},
		Repo:     types.Repository{Branch: "main", Commit: "abc123", ChangedFiles: []string{"main.go"}},
	}, now, now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Generator != "deterministic" {
		t.Fatalf("generator = %q", handoff.Generator)
	}
	for _, heading := range []string{"## Goal", "## Context", "## Decisions", "## Current State", "## Important Files", "## Next Steps", "## Open Questions"} {
		if !strings.Contains(handoff.Markdown, heading) {
			t.Fatalf("handoff missing %s", heading)
		}
	}
	if !strings.Contains(handoff.Markdown, "version: 2") || !strings.Contains(handoff.Markdown, "continue the CLI") {
		t.Fatalf("unexpected handoff:\n%s", handoff.Markdown)
	}
}

func TestHTMLRendersMarkdownWithoutRawHTML(t *testing.T) {
	page := HTML(types.Handoff{
		Goal:      "Continue safely",
		Markdown:  "# Handoff\n\n## Current State\n\nReady.\n\n<script>alert('no')</script>",
		ExpiresAt: time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC),
	})
	if !strings.Contains(page, "<h1>Handoff</h1>") || !strings.Contains(page, "<h2>Current State</h2>") {
		t.Fatalf("Markdown was not rendered: %s", page)
	}
	if strings.Contains(page, "<script>") {
		t.Fatalf("raw HTML was rendered: %s", page)
	}
}

type invalidCompactor struct{}

func (invalidCompactor) Compact(context.Context, string, types.Context) (Sections, error) {
	return Sections{Context: "only context"}, nil
}

type namedCompactor struct{}

func (namedCompactor) Compact(context.Context, string, types.Context) (Sections, error) {
	return Sections{Context: "Known", CurrentState: "Ready", NextSteps: []string{"Continue"}}, nil
}
func (namedCompactor) Generator() string { return "server:agent-plan" }

func TestBuildUsesNamedCompactorGenerator(t *testing.T) {
	now := time.Now().UTC()
	handoff, err := Build(context.Background(), namedCompactor{}, "abcdefghijklmnopqrstuv", "continue", types.Context{
		Source: "stdin", Messages: []types.Message{{Role: "user", Text: "known state"}},
	}, now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Generator != "server:agent-plan" || !strings.Contains(handoff.Markdown, `generator: "server:agent-plan"`) {
		t.Fatalf("unexpected generator: %q", handoff.Generator)
	}
}

func TestBuildFallsBackWhenModelContractIsIncomplete(t *testing.T) {
	now := time.Now().UTC()
	handoff, err := Build(context.Background(), invalidCompactor{}, "abcdefghijklmnopqrstuv", "continue", types.Context{
		Source:   "stdin",
		Messages: []types.Message{{Role: "user", Text: "known state"}},
	}, now, now.Add(time.Hour))
	if err == nil {
		t.Fatal("expected compact contract error")
	}
	if handoff.Generator != "deterministic" {
		t.Fatalf("generator = %q", handoff.Generator)
	}
}

func TestAgentPlanCompactorUsesAnthropicCompatibleContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/messages" || request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("anthropic-version") != "2023-06-01" {
			t.Fatalf("anthropic-version = %q", request.Header.Get("anthropic-version"))
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "model-1" {
			t.Fatalf("model = %#v", body["model"])
		}
		if body["max_tokens"] != float64(16384) || body["system"] == nil {
			t.Fatalf("invalid Anthropic request: %#v", body)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"content":[{"type":"text","text":"` +
			`{\"context\":\"Known context\",\"decisions\":[],\"current_state\":\"Ready\",\"important_files\":[],\"next_steps\":[\"Continue\"],\"open_questions\":[]}` +
			`"}]}`))
	}))
	defer server.Close()

	compactor := AgentPlanCompactor{BaseURL: server.URL, APIKey: "secret", Model: "model-1", Client: server.Client()}
	sections, err := compactor.Compact(context.Background(), "Continue", types.Context{Source: "stdin", Messages: []types.Message{{Role: "user", Text: "Ready"}}})
	if err != nil {
		t.Fatal(err)
	}
	if sections.Context != "Known context" || len(sections.NextSteps) != 1 {
		t.Fatalf("sections = %#v", sections)
	}
	if compactor.Generator() != "server:agent-plan" {
		t.Fatalf("generator = %q", compactor.Generator())
	}
}
