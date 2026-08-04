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

func TestSanitizeContextKeepsCompleteReadableHistoryAndAuxiliarySummary(t *testing.T) {
	early := "BEGIN-" + strings.Repeat("a", 200_000)
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
	if result.Summary != input.Summary || result.FullSession || len(result.Messages) != 2 || result.Messages[0].Text != early || result.Messages[1].Text != "END" {
		t.Fatalf("canonical context was summarized or truncated: summary=%q messages=%d", result.Summary, len(result.Messages))
	}
}

func TestFallbackSectionsKeepsSummaryAndCompleteHistory(t *testing.T) {
	messages := make([]types.Message, 0, 10)
	for index := 0; index < 10; index++ {
		messages = append(messages, types.Message{Role: "user", Text: strings.Repeat("x", 2_100) + string(rune('A'+index))})
	}
	result := FallbackSections(IntentContinue, "continue", types.Context{
		Source:   "codex",
		Summary:  "auxiliary summary",
		Messages: messages,
	})
	if !strings.Contains(result.Context, "Native summary (auxiliary)") || !strings.Contains(result.Context, "auxiliary summary") {
		t.Fatalf("fallback dropped the native summary: %q", result.Context)
	}
	for index := 0; index < 10; index++ {
		marker := string(rune('A' + index))
		if !strings.Contains(result.Context, marker) {
			t.Fatalf("fallback dropped or truncated message %d", index)
		}
	}
}

func TestParseSectionsAcceptsSingletonStringsForListFields(t *testing.T) {
	sections, err := ParseSections(`{
  "intent":"continue",
  "human_background":"Background",
  "human_status":"Ready",
  "human_todos":"Continue",
  "human_sections":[],
  "context":"Known context",
  "decisions":"Keep the narrow fix",
  "current_state":"Ready",
  "important_files":"internal/card/card.go",
  "next_steps":"Continue",
  "open_questions":"Verify OpenCode"
}`, IntentContinue)
	if err != nil {
		t.Fatal(err)
	}
	if len(sections.HumanTodos) != 1 || sections.HumanTodos[0] != "Continue" ||
		len(sections.Decisions) != 1 || sections.Decisions[0] != "Keep the narrow fix" ||
		len(sections.ImportantFiles) != 1 || sections.ImportantFiles[0] != "internal/card/card.go" ||
		len(sections.NextSteps) != 1 || sections.NextSteps[0] != "Continue" ||
		len(sections.OpenQuestions) != 1 || sections.OpenQuestions[0] != "Verify OpenCode" {
		t.Fatalf("singleton lists were not normalized: %#v", sections)
	}
}

func TestParseSectionsKeepsNonStringListValuesStrict(t *testing.T) {
	_, err := ParseSections(`{
  "intent":"continue",
  "human_background":"Background",
  "human_status":"Ready",
  "human_todos":["Continue"],
  "human_sections":[],
  "context":"Known context",
  "decisions":{"unexpected":true},
  "current_state":"Ready",
  "important_files":[],
  "next_steps":["Continue"],
  "open_questions":[]
}`, IntentContinue)
	if err == nil || !strings.Contains(err.Error(), "cannot unmarshal object") {
		t.Fatalf("unexpected non-string list result: %v", err)
	}
}

func TestTruncateDoesNotSplitUnicode(t *testing.T) {
	if got := truncate("你好世界", 3); got != "你好世…" {
		t.Fatalf("unicode truncate = %q", got)
	}
}

func TestContextAttachmentOmitsProviderLocalIdentifiers(t *testing.T) {
	attachment := BuildContextAttachment(types.Context{
		Source: "codex", SessionID: "private-session", Cursor: "line:20",
		CWD: "/Users/alice/work/demo", Summary: "native summary",
		NativeCompactFound: true,
		Messages:           []types.Message{{Role: "user", Text: "continue"}},
		Repo:               types.Repository{Root: "/Users/alice/work/demo", Branch: "main", ChangedFiles: []string{"README.md"}},
	})
	encoded, err := json.Marshal(attachment)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-session", "line:20", "/Users/alice", `"root"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("attachment leaked %q: %s", forbidden, encoded)
		}
	}
	if attachment.Version != types.ContextAttachmentVersion || attachment.Redaction != types.RedactionVersion || len(attachment.Messages) != 1 {
		t.Fatalf("unexpected attachment: %#v", attachment)
	}
}

func TestBuildDeterministicHandoffHasStableContract(t *testing.T) {
	now := time.Date(2026, 7, 22, 8, 0, 0, 0, time.UTC)
	handoff, err := Build(context.Background(), nil, "abcdefghijklmnopqrstuv", IntentContinue, "continue the CLI", types.Context{
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
	if !strings.Contains(handoff.Markdown, "version: 6") || !strings.Contains(handoff.Markdown, "continue the CLI") {
		t.Fatalf("unexpected handoff:\n%s", handoff.Markdown)
	}
	receiverInstruction := "> 这是一份被传递的 Handoff。请先用清晰易懂的话向用户简单介绍当前背景，然后询问用户下一步要怎么做。\n"
	if !strings.HasSuffix(handoff.Markdown, receiverInstruction) {
		t.Fatalf("handoff does not end with the receiver instruction:\n%s", handoff.Markdown)
	}
}

func TestShareHandoffPrioritizesDiscussionResults(t *testing.T) {
	now := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	handoff, err := BuildFromSections("abcdefghijklmnopqrstuv", "MCP App 架构讨论", types.SourceRef{Kind: "codex"}, Sections{
		Intent: IntentShare,
		HumanSections: []types.HumanSection{
			{Title: "先把三个东西分清楚", Body: "MCP App 主要规定 App 与宿主如何通信，并不等于 App 本体。"},
			{Title: "故事种子具体怎么通信", Body: "隔离运行的 App 无法直接调用宿主函数。故事种子通过 `command.run` 取数，通过 `openLink` 打开签约页。"},
			{Title: "为什么不搬回宿主", Body: "把业务 UI 搬回宿主会重新引入发版耦合，所以保留独立 View。"},
		},
		Context:        "作品管理 UI 是 protocol: mcp-app 的 view。",
		Decisions:      []string{"保留 MCP App 通信通道。"},
		ImportantFiles: []string{"internal/card/card.go"},
		OpenQuestions:  []string{"未来是否需要官方 App 信任分级？"},
	}, "agent:codex", now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if handoff.Intent != IntentShare || !strings.Contains(handoff.Markdown, `intent: "share"`) {
		t.Fatalf("share intent is missing: %#v\n%s", handoff, handoff.Markdown)
	}
	for _, expected := range []string{"先把三个东西分清楚", "故事种子具体怎么通信", "为什么不搬回宿主", "MCP App 主要规定"} {
		if !strings.Contains(handoff.Markdown, expected) {
			t.Fatalf("share handoff missing %q:\n%s", expected, handoff.Markdown)
		}
	}
	for _, genericHeading := range []string{"关键结论", "为什么会得出这些结论", "帮助理解的例子", "讨论中纠正的误解"} {
		if strings.Contains(handoff.Markdown, "### "+genericHeading) {
			t.Fatalf("share handoff still uses generic bucket %q:\n%s", genericHeading, handoff.Markdown)
		}
	}
	for _, taskHeading := range []string{"### 待办事项", "### Current State", "### Next Steps"} {
		if strings.Contains(handoff.Markdown, taskHeading) {
			t.Fatalf("share handoff contains task section %q:\n%s", taskHeading, handoff.Markdown)
		}
	}
	if !strings.HasSuffix(handoff.Markdown, "> 这是一份讨论成果分享。请准确保留它的结论与推理；除非用户明确要求，不要把其中的问题自动改写成待办事项。\n") {
		t.Fatalf("unexpected receiver instruction:\n%s", handoff.Markdown)
	}
	page := HTML(handoff)
	for _, expected := range []string{"讨论成果", "技术附录", "MCP App 主要规定"} {
		if !strings.Contains(page, expected) {
			t.Fatalf("share page missing %q: %s", expected, page)
		}
	}
}

func TestLegacyShareBucketsRemainPublishable(t *testing.T) {
	now := time.Now().UTC()
	handoff, err := BuildFromSections("abcdefghijklmnopqrstuv", "legacy share", types.SourceRef{Kind: "codex"}, Sections{
		Intent: IntentShare, HumanBackground: "Background", HumanSummary: "Summary",
		KeyConclusions: []string{"Conclusion"}, Reasoning: []string{"Reason"}, Context: "Technical",
	}, "agent:legacy", now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"### 我们讨论了什么", "### 讨论结果", "### 关键结论", "### 为什么会得出这些结论"} {
		if !strings.Contains(handoff.Markdown, expected) {
			t.Fatalf("legacy share did not migrate %q:\n%s", expected, handoff.Markdown)
		}
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

func TestParseReviewedShareMarkdownRoundTrip(t *testing.T) {
	now := time.Now().UTC()
	draft := types.Handoff{
		Version: types.ProtocolVersion, ID: "review-draft", Goal: "share discussion", Intent: IntentShare,
		Source: types.SourceRef{Kind: "codex"}, Generator: "agent:codex",
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	markdown := RenderReviewDraft(draft, Sections{
		Intent: IntentShare,
		HumanSections: []types.HumanSection{
			{Title: "Understand C# and [views]", Body: "Summary with reason and example."},
			{Title: "Why this choice", Body: "Decision explanation."},
		},
		Context:   "Technical",
		Decisions: []string{"Decision"}, ImportantFiles: []string{"README.md"}, OpenQuestions: []string{"Question?"},
	})
	markdown = strings.Replace(markdown, "Summary with reason and example.", "Reviewed summary with reason and example.", 1)
	sections, err := ParseReviewedMarkdown(markdown)
	if err != nil {
		t.Fatal(err)
	}
	if sections.Intent != IntentShare || len(sections.HumanSections) != 2 || sections.HumanSections[0].Title != "Understand C# and [views]" || sections.HumanSections[0].Body != "Reviewed summary with reason and example." || sections.CurrentState != "" || len(sections.NextSteps) != 0 {
		t.Fatalf("unexpected reviewed share sections: %#v", sections)
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

func TestHTMLRendersInlineAndDisplayMathWithoutTouchingCode(t *testing.T) {
	page := HTML(types.Handoff{
		ID:   "abcdefghijklmnopqrstuv",
		Goal: "Explain reward scores",
		Markdown: "---\nversion: 6\n---\n\n# Math\n\nInline $s_A = f_\\theta(p,r_A)$ works.\n\n" +
			"$$\nP(A \\succ B)=\\frac{e^{s_A}}{e^{s_A}+e^{s_B}}\n$$\n\n```text\n$not_math$\n```",
	})
	for _, expected := range []string{`class="math-inline"`, `class="math-display"`, "<math", "<mfrac", "<msub", "<msup"} {
		if !strings.Contains(page, expected) {
			t.Fatalf("rendered page does not contain %q: %s", expected, page)
		}
	}
	if !strings.Contains(page, "$not_math$") || !strings.Contains(page, `<code class="language-text">`) {
		t.Fatalf("math inside code fence was changed: %s", page)
	}
}

func TestHTMLMathRenderingCannotInjectRawHTML(t *testing.T) {
	page := HTML(types.Handoff{
		ID:       "abcdefghijklmnopqrstuv",
		Goal:     "Safe math",
		Markdown: "# Math\n\n$\\text{<script>alert(1)</script>}$\n\n$\\class{evil\\\" onclick=\\\"alert(1)}{x}$",
	})
	if strings.Contains(page, "<script>") || strings.Contains(page, `onclick="alert`) {
		t.Fatalf("unsafe HTML escaped the math renderer: %s", page)
	}
	if !strings.Contains(page, `class="math-source"`) {
		t.Fatalf("unsupported math did not fall back to escaped source: %s", page)
	}
}

type invalidCompactor struct{}

func (invalidCompactor) Compact(context.Context, string, string, types.Context) (Sections, error) {
	return Sections{Context: "only context"}, nil
}

type namedCompactor struct{}

func (namedCompactor) Compact(context.Context, string, string, types.Context) (Sections, error) {
	return Sections{Context: "Known", CurrentState: "Ready", NextSteps: []string{"Continue"}}, nil
}
func (namedCompactor) Generator() string { return "server:agent-plan" }

func TestBuildUsesNamedCompactorGenerator(t *testing.T) {
	now := time.Now().UTC()
	handoff, err := Build(context.Background(), namedCompactor{}, "abcdefghijklmnopqrstuv", IntentContinue, "continue", types.Context{
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
	handoff, err := Build(context.Background(), invalidCompactor{}, "abcdefghijklmnopqrstuv", IntentContinue, "continue", types.Context{
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
	sections, err := compactor.Compact(context.Background(), IntentContinue, "Continue", types.Context{Source: "stdin", Messages: []types.Message{{Role: "user", Text: "Ready"}}})
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
