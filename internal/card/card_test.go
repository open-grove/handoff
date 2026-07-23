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
			Text: "api_key = \"super-secret-value\"\nAuthorization: Bearer abcdefghijklmnop\nkey：ark-1234567890-abcdefghijklmnop\nemail alice@example.com\nhost 203.0.113.9\nread /Users/alice/work/demo/main.go",
		}},
	}
	result := SanitizeContext(input)
	text := result.Messages[0].Text
	for _, forbidden := range []string{"super-secret-value", "abcdefghijklmnop", "ark-1234567890-abcdefghijklmnop", "alice@example.com", "203.0.113.9", "/Users/alice"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("sanitized text contains %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "[REDACTED]") || !strings.Contains(text, "[REDACTED EMAIL]") || !strings.Contains(text, "[REDACTED IP]") || !strings.Contains(text, "$HOME/work/demo/main.go") {
		t.Fatalf("sanitized text lost expected markers: %s", text)
	}
}

func TestSanitizeFullSessionDoesNotApplyNormalContextLimit(t *testing.T) {
	early := "BEGIN-" + strings.Repeat("a", maxContextChars)
	input := types.Context{
		Source:      "codex",
		FullSession: true,
		Summary:     "compact summary must not replace the full conversation",
		Messages: []types.Message{
			{Role: "user", Text: early},
			{Role: "assistant", Text: "END"},
		},
	}
	result := SanitizeContext(input)
	if result.Summary != "" || len(result.Messages) != 2 || !strings.HasPrefix(result.Messages[0].Text, "BEGIN-") || result.Messages[1].Text != "END" {
		t.Fatalf("full session was summarized or truncated: summary=%q messages=%d", result.Summary, len(result.Messages))
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
	for _, heading := range []string{"# continue the CLI", "## For Human", "### 项目背景", "### 当前情况", "### 待办事项", "## For Agent", "### Goal", "### Context", "### Decisions", "### Current State", "### Important Files", "### Next Steps", "### Open Questions"} {
		if !strings.Contains(handoff.Markdown, heading) {
			t.Fatalf("handoff missing %s", heading)
		}
	}
	if !strings.Contains(handoff.Markdown, "version: 3") || !strings.Contains(handoff.Markdown, "continue the CLI") {
		t.Fatalf("unexpected handoff:\n%s", handoff.Markdown)
	}
	receiverInstruction := "> 这是一份被传递的 Handoff。请先用清晰易懂的话向用户简单介绍当前背景，然后询问用户下一步要怎么做。\n"
	if !strings.HasSuffix(handoff.Markdown, receiverInstruction) {
		t.Fatalf("handoff does not end with the receiver instruction:\n%s", handoff.Markdown)
	}
}

func TestCompactTitleKeepsFullGoalInAgentSection(t *testing.T) {
	goal := "继续完成编辑部 0.1.12 发布：核实并安全修复团队权限，测试后重新发布"
	if got := CompactTitle(goal); got != "继续完成编辑部 0.1.12 发布" {
		t.Fatalf("compact title = %q", got)
	}
	longEnglish := "Continue the editorial release while preserving every verified implementation detail for the receiving agent"
	title := CompactTitle(longEnglish)
	if len([]rune(title)) >= len([]rune(longEnglish)) || !strings.HasSuffix(title, "…") {
		t.Fatalf("long title was not capped: %q", title)
	}
	now := time.Date(2026, 7, 23, 8, 0, 0, 0, time.UTC)
	handoff, err := BuildFromSections("abcdefghijklmnopqrstuv", goal, types.SourceRef{Kind: "codex"}, Sections{
		Context: "Known", CurrentState: "Ready", NextSteps: []string{"Continue"},
	}, "agent:codex", now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Title != "继续完成编辑部 0.1.12 发布" {
		t.Fatalf("stored title = %q", handoff.Title)
	}
	if !strings.Contains(handoff.Markdown, "# 继续完成编辑部 0.1.12 发布") || !strings.Contains(handoff.Markdown, goal) {
		t.Fatalf("compact heading or complete Agent goal is missing:\n%s", handoff.Markdown)
	}
}

func TestHTMLSeparatesHumanSummaryFromAgentContext(t *testing.T) {
	now := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	handoff, err := BuildFromSections("abcdefghijklmnopqrstuv", "继续交接工具", types.SourceRef{Kind: "codex"}, Sections{
		HumanBackground: "让同事和 Agent 都能快速接手一个已有会话。",
		HumanStatus:     "核心流程已经可用，正在优化分享页。",
		HumanTodos:      []string{"上线新分享页", "请同事试用"},
		Context:         "CLI and service are implemented.",
		CurrentState:    "Tests pass.",
		NextSteps:       []string{"Deploy"},
	}, "agent:codex", now, now.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	page := HTML(handoff)
	for _, expected := range []string{`class="panel human-panel"`, `class="panel agent-panel"`, `class="brand" href="https://github.com/open-grove/handoff"`, `class="brand-mark"><svg viewBox="0 0 128 128"`, `fill="#5FB24A"`, "核心流程已经可用", "Agent 交接上下文", "abcdefghijklmnopqrstuv", "查看安装方法"} {
		if !strings.Contains(page, expected) {
			t.Fatalf("page missing %q: %s", expected, page)
		}
	}
	for _, removed := range []string{"READY TO CONTINUE", "一份给人和 Agent", "来自 codex", "有效期至", "Shared with OpenGrove", `class="brand-mark">OG`} {
		if strings.Contains(page, removed) {
			t.Fatalf("page still contains removed decoration %q: %s", removed, page)
		}
	}
	if !strings.Contains(page, `<section class="hero"><h1>继续交接工具</h1></section>`) {
		t.Fatalf("page title is not the compact heading: %s", page)
	}
	if !strings.Contains(page, `.human-content{display:grid;grid-template-columns:1fr;gap:0}`) {
		t.Fatalf("human summary is not rendered as three full-width rows: %s", page)
	}
	if !strings.Contains(page, `.hero{max-width:760px;margin:0 auto 22px;text-align:center}`) {
		t.Fatalf("page title is not centered: %s", page)
	}
	if strings.Index(page, "核心流程已经可用") > strings.Index(page, "Agent 交接上下文") {
		t.Fatal("human summary must appear before agent context")
	}
}

func TestImportantFilesAreRepositoryRelative(t *testing.T) {
	input := types.Context{
		Source: "codex",
		Repo: types.Repository{
			Root: "/Users/alice/work/demo",
			ChangedFiles: []string{
				"/Users/alice/work/demo/internal/card/card.go",
				"/Users/alice/notes/private.md",
				"README.md",
				"../outside.txt",
			},
		},
	}
	sanitized := SanitizeContext(input)
	if got := strings.Join(sanitized.Repo.ChangedFiles, ","); got != "internal/card/card.go,README.md" {
		t.Fatalf("changed files = %q", got)
	}

	now := time.Now().UTC()
	handoff, err := BuildFromSections("abcdefghijklmnopqrstuv", "continue", types.SourceRef{Kind: "codex"}, Sections{
		Context:      "Known",
		CurrentState: "Ready",
		ImportantFiles: []string{
			"$WORKSPACE/cloudflare/src/index.mjs",
			"$HOME/Downloads/private.txt",
			"/Users/alice/work/demo/README.md",
			"internal/card/card.go",
			"../outside.txt",
		},
		NextSteps: []string{"Continue"},
	}, "agent:codex", now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"cloudflare/src/index.mjs", "internal/card/card.go"} {
		if !strings.Contains(handoff.Markdown, "- "+expected) {
			t.Fatalf("missing portable path %q:\n%s", expected, handoff.Markdown)
		}
	}
	for _, forbidden := range []string{"$HOME", "/Users/", "../outside"} {
		if strings.Contains(handoff.Markdown, forbidden) {
			t.Fatalf("handoff contains machine-specific path %q:\n%s", forbidden, handoff.Markdown)
		}
	}
}

func TestParseReviewedMarkdownRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	draft := types.Handoff{
		Version: types.ProtocolVersion, ID: "review-draft", Goal: "continue",
		Source: types.SourceRef{Kind: "codex"}, Generator: "agent:codex",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	markdown := RenderReviewDraft(draft, Sections{
		HumanBackground: "Background", HumanStatus: "Ready", HumanTodos: []string{"Ship"},
		Context: "Known\n\n# Embedded title\n\n### Arbitrary subsection\n\nStill context.", Decisions: []string{"Use Markdown"}, CurrentState: "Tests pass",
		ImportantFiles: []string{"README.md"}, NextSteps: []string{"Deploy"}, OpenQuestions: []string{"Domain?"},
	})
	markdown = strings.Replace(markdown, "Tests pass", "Tests pass after review", 1)
	sections, err := ParseReviewedMarkdown(markdown)
	if err != nil {
		t.Fatal(err)
	}
	if sections.CurrentState != "Tests pass after review" || !strings.Contains(sections.Context, "Still context.") || len(sections.Decisions) != 1 || sections.NextSteps[0] != "Deploy" {
		t.Fatalf("unexpected reviewed sections: %#v", sections)
	}
}

func TestMarkdownTitleEscapesFormattingWithoutDroppingText(t *testing.T) {
	if got := markdownTitle("Continue C# and [docs]"); got != `Continue C\# and \[docs\]` {
		t.Fatalf("markdown title = %q", got)
	}
}

func TestHTMLRendersMarkdownWithoutRawHTML(t *testing.T) {
	page := HTML(types.Handoff{
		ID:        "abcdefghijklmnopqrstuv",
		Goal:      "Continue safely",
		Markdown:  "---\nversion: 2\n---\n\n# Handoff\n\n## Current State\n\nReady.\n\n<script>alert('no')</script>",
		ExpiresAt: time.Date(2026, 7, 29, 8, 0, 0, 0, time.UTC),
	})
	if !strings.Contains(page, "<h1>Handoff</h1>") || !strings.Contains(page, "<h2>Current State</h2>") {
		t.Fatalf("Markdown was not rendered: %s", page)
	}
	if strings.Contains(page, "<script>") {
		t.Fatalf("raw HTML was rendered: %s", page)
	}
	if strings.Contains(page, "version: 2") || !strings.Contains(page, `href="./abcdefghijklmnopqrstuv.md"`) {
		t.Fatalf("front matter or raw Markdown link is wrong: %s", page)
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
		t.Fatal("expected generation contract error")
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
			`{\"human_background\":\"A handoff tool\",\"human_status\":\"Ready\",\"human_todos\":[\"Continue\"],\"context\":\"Known context\",\"decisions\":[],\"current_state\":\"Ready\",\"important_files\":[],\"next_steps\":[\"Continue\"],\"open_questions\":[]}` +
			`"}]}`))
	}))
	defer server.Close()

	compactor := AgentPlanCompactor{BaseURL: server.URL, APIKey: "secret", Model: "model-1", Client: server.Client()}
	sections, err := compactor.Compact(context.Background(), "Continue", types.Context{Source: "stdin", Messages: []types.Message{{Role: "user", Text: "Ready"}}})
	if err != nil {
		t.Fatal(err)
	}
	if sections.HumanBackground != "A handoff tool" || sections.Context != "Known context" || len(sections.NextSteps) != 1 {
		t.Fatalf("sections = %#v", sections)
	}
	if compactor.Generator() != "server:agent-plan" {
		t.Fatalf("generator = %q", compactor.Generator())
	}
}
