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
			Text: "api_key = \"super-secret-value\"\nAuthorization: Bearer abcdefghijklmnop\nread /Users/alice/work/demo/main.go",
		}},
	}
	result := SanitizeContext(input)
	text := result.Messages[0].Text
	for _, forbidden := range []string{"super-secret-value", "abcdefghijklmnop", "/Users/alice"} {
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

type invalidCompactor struct{}

func (invalidCompactor) Compact(context.Context, string, types.Context) (Sections, error) {
	return Sections{Context: "only context"}, nil
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

func TestArkCompactorUsesOpenAICompatibleContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/chat/completions" || request.Header.Get("Authorization") != "Bearer secret" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["model"] != "model-1" {
			t.Fatalf("model = %#v", body["model"])
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"choices":[{"message":{"content":"` +
			`{\"context\":\"Known context\",\"decisions\":[],\"current_state\":\"Ready\",\"important_files\":[],\"next_steps\":[\"Continue\"],\"open_questions\":[]}` +
			`"}}]}`))
	}))
	defer server.Close()

	compactor := ArkCompactor{BaseURL: server.URL, APIKey: "secret", Model: "model-1", Client: server.Client()}
	sections, err := compactor.Compact(context.Background(), "Continue", types.Context{Source: "stdin", Messages: []types.Message{{Role: "user", Text: "Ready"}}})
	if err != nil {
		t.Fatal(err)
	}
	if sections.Context != "Known context" || len(sections.NextSteps) != 1 {
		t.Fatalf("sections = %#v", sections)
	}
}
