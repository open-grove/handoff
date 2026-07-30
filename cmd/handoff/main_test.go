package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-grove/handoff/internal/types"
	skillbundle "github.com/open-grove/handoff/skills"
)

func TestParseHandoffRef(t *testing.T) {
	id := "abcdefghijklmnopqrstuv"
	parsedID, server := parseHandoffRef("https://handoff.example/h/" + id)
	if parsedID != id || server != "https://handoff.example" {
		t.Fatalf("parsed (%q, %q)", parsedID, server)
	}
	parsedID, server = parseHandoffRef(id)
	if parsedID != id || server != "" {
		t.Fatalf("parsed (%q, %q)", parsedID, server)
	}
	parsedID, server = parseHandoffRef("opengrove-handoff，分享码：" + id)
	if parsedID != id || server != "" {
		t.Fatalf("parsed branded reference (%q, %q)", parsedID, server)
	}
	parsedID, server = parseHandoffRef("请使用 opengrove-handoff 读取内容，分享码：`" + id + "`")
	if parsedID != id || server != "" {
		t.Fatalf("parsed Agent instruction (%q, %q)", parsedID, server)
	}
	parsedID, server = parseHandoffRef("请读取 `opengrove-handoff:" + id + "`")
	if parsedID != id || server != "" {
		t.Fatalf("parsed stable Agent reference (%q, %q)", parsedID, server)
	}
	parsedID, server = parseHandoffRef("https://handoff.example/h/" + id + ".md")
	if parsedID != id || server != "https://handoff.example" {
		t.Fatalf("parsed Markdown URL (%q, %q)", parsedID, server)
	}
	parsedID, server = parseHandoffRef("收到一条 opengrove-handoff 分享，分享交接：[https://handoff.example/h/" + id + "](https://handoff.example/h/" + id + ")")
	if parsedID != id || server != "https://handoff.example" {
		t.Fatalf("parsed notification (%q, %q)", parsedID, server)
	}
}

func TestParseHandoffRefRejectsInvalidValues(t *testing.T) {
	if id, _ := parseHandoffRef("short"); id != "" {
		t.Fatalf("accepted invalid id %q", id)
	}
}

func TestFormatShareMessageSeparatesHumanAndAgentInstructions(t *testing.T) {
	expiresAt := time.Date(2026, time.July, 29, 19, 42, 0, 0, time.Local)
	result := types.CreateResponse{
		Handoff: types.Handoff{
			ID: "abcdefghijklmnopqrstuv", Goal: "完成 [CLI]\n部署", ExpiresAt: expiresAt,
		},
		ShareURL: "https://handoff.openmau.com/h/abcdefghijklmnopqrstuv",
	}
	message := formatShareMessage(result)
	for _, expected := range []string{
		"🖐️ **For Human**",
		"你收到一份 Handoff，请打开[完成 \\[CLI\\] 部署](https://handoff.openmau.com/h/abcdefghijklmnopqrstuv)查看。",
		"🤖 **For Agent**",
		"请使用 OpenGrove Handoff 读取：`opengrove-handoff:abcdefghijklmnopqrstuv`",
		"[查看安装方法](https://github.com/open-grove/handoff)",
		"有效期：" + expiresAt.Format(time.RFC3339),
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("share message missing %q:\n%s", expected, message)
		}
	}
}

func TestResolveCreateSelectionUsesPreferredVocabulary(t *testing.T) {
	selection, err := resolveCreateSelection(createSelectionInput{
		Source: "codex", Generator: "cloud", Runtime: "claude", AttachContext: true,
		Set: map[string]bool{"source": true, "generator": true, "runtime": true, "attach-context": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Source != "codex" || selection.Generator != "cloud" || selection.Runtime != "claude" || !selection.AttachContext || selection.SessionPath || len(selection.Deprecated) != 0 {
		t.Fatalf("unexpected preferred selection: %#v", selection)
	}
}

func TestResolveCreateSelectionMapsLegacyVocabulary(t *testing.T) {
	selection, err := resolveCreateSelection(createSelectionInput{
		Source: "auto", Generator: "agent", Runtime: "auto",
		LegacyFrom: "pi", LegacyMode: "server", LegacyAgent: "codex", LegacyIncludeTranscript: true,
		Set: map[string]bool{"from": true, "mode": true, "agent": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Source != "pi" || selection.Generator != "cloud" || selection.Runtime != "codex" || selection.UploadContext != "selected" || len(selection.Deprecated) < 4 {
		t.Fatalf("unexpected compatibility selection: %#v", selection)
	}
}

func TestResolveCreateSelectionRejectsConflicts(t *testing.T) {
	_, err := resolveCreateSelection(createSelectionInput{
		Source: "codex", Generator: "agent", Runtime: "auto",
		LegacyFrom: "pi",
		Set:        map[string]bool{"source": true, "from": true},
	})
	if err == nil {
		t.Fatal("expected preferred and legacy source conflict to fail")
	}
}

func TestFormatShareMessageUsesCompactTitle(t *testing.T) {
	result := types.CreateResponse{
		Handoff: types.Handoff{
			ID:        "abcdefghijklmnopqrstuv",
			Title:     "继续完成编辑部 0.1.12 发布",
			Goal:      "继续完成编辑部 0.1.12 发布：核实并安全修复所有权限问题",
			ExpiresAt: time.Now().Add(time.Hour),
		},
		ShareURL: "https://handoff.openmau.com/h/abcdefghijklmnopqrstuv",
	}
	message := formatShareMessage(result)
	if !strings.Contains(message, "[继续完成编辑部 0.1.12 发布]") || strings.Contains(message, "核实并安全修复") {
		t.Fatalf("share message did not use compact title:\n%s", message)
	}
}

func TestMarkdownLinkLabelFallback(t *testing.T) {
	if label := markdownLinkLabel(" \n\t"); label != "Handoff 交接" {
		t.Fatalf("fallback label = %q", label)
	}
}

func TestResolveCreateMode(t *testing.T) {
	for _, test := range []struct {
		mode, compact       string
		modeSet, compactSet bool
		want                string
	}{
		{mode: "agent", want: "agent"},
		{mode: "local", modeSet: true, want: "local"},
		{mode: "session", modeSet: true, want: "session"},
		{mode: "agent", compact: "current", compactSet: true, want: "agent"},
		{mode: "agent", compact: "none", compactSet: true, want: "local"},
		{mode: "agent", compact: "server", compactSet: true, want: "server"},
	} {
		got, err := resolveCreateMode(test.mode, test.compact, test.modeSet, test.compactSet)
		if err != nil || got != test.want {
			t.Fatalf("resolveCreateMode(%q, %q) = %q, %v; want %q", test.mode, test.compact, got, err, test.want)
		}
	}
	if _, err := resolveCreateMode("local", "server", true, true); err == nil {
		t.Fatal("expected conflicting mode flags to fail")
	}
}

func TestLocalSessionMessageIsExplicitlySameMachineOnly(t *testing.T) {
	message := formatLocalSessionMessage("continue", "codex", "/tmp/session.jsonl")
	for _, expected := range []string{"Local Session（仅限本机）", "/tmp/session.jsonl", "下一目标：continue", "不会上传", "不会生成分享码", "其他设备无法访问"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("local Session message missing %q:\n%s", expected, message)
		}
	}
}

func TestFormatAttachedContextDistinguishesPortableContextFromRawSession(t *testing.T) {
	rendered := formatAttachedContext(types.ContextResponse{
		HandoffID: "abcdefghijklmnopqrstuv",
		Context: types.ContextAttachment{
			Source:        types.SourceRef{Kind: "codex"},
			NativeSummary: "auxiliary summary",
			Messages:      []types.Message{{Role: "user", Text: "continue"}},
			Redaction:     types.RedactionVersion,
		},
	})
	for _, expected := range []string{
		"opengrove-handoff:abcdefghijklmnopqrstuv",
		"Native Compact Summary (auxiliary)",
		"### USER",
		"continue",
		"不是包含工具结果和内部记录的原始 Provider Session",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("attached Context missing %q:\n%s", expected, rendered)
		}
	}
}

func TestCreateJSONIncludesCanonicalShareMessage(t *testing.T) {
	result := types.CreateResponse{
		Handoff: types.Handoff{
			ID: "abcdefghijklmnopqrstuv", Goal: "continue", ExpiresAt: time.Now().Add(time.Hour),
		},
		ShareURL: "https://handoff.openmau.com/h/abcdefghijklmnopqrstuv",
	}
	output := createCommandOutput(result, true, nil)
	if output["share_message"] != formatShareMessage(result) {
		t.Fatalf("share_message = %#v", output["share_message"])
	}
	if output["agent_reference"] != "opengrove-handoff:abcdefghijklmnopqrstuv" {
		t.Fatalf("agent_reference = %#v", output["agent_reference"])
	}
	if output["fallback_used"] != false {
		t.Fatalf("fallback_used = %#v", output["fallback_used"])
	}
	warningOutput := createCommandOutput(result, true, errors.New("agent failed"))
	if warningOutput["fallback_used"] != true || warningOutput["generation_warning"] != "agent failed" {
		t.Fatalf("fallback warning missing from JSON: %#v", warningOutput)
	}
}

func TestReadStdinContextRejectsOversizedInput(t *testing.T) {
	_, err := readStdinContext(bytes.NewReader(bytes.Repeat([]byte("x"), (4<<20)+1)), 4<<20)
	if err == nil || !strings.Contains(err.Error(), "exceeds the 4 MiB limit") {
		t.Fatalf("oversized stdin was silently truncated: %v", err)
	}
}

func TestServerGenerationMustFailClosed(t *testing.T) {
	for _, preview := range []types.CompactPreviewResponse{
		{Generator: "deterministic", Warning: "Agent Plan timed out"},
		{Generator: "deterministic"},
	} {
		if err := validateServerPreview(preview); err == nil {
			t.Fatalf("accepted failed server preview: %#v", preview)
		}
	}
	if err := validateServerPreview(types.CompactPreviewResponse{Generator: "server:agent-plan"}); err != nil {
		t.Fatalf("rejected valid server preview: %v", err)
	}
}

func TestReviewSectionsAcceptsUnchangedDraft(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "true")
	input := types.Sections{
		HumanBackground: "Background", HumanStatus: "Ready", HumanTodos: []string{"Continue"},
		Context: "Known", CurrentState: "Ready", NextSteps: []string{"Continue"},
	}
	output, err := reviewSections(context.Background(), "continue", types.Context{Source: "stdin"}, input, "deterministic", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if output.Context != input.Context || output.CurrentState != input.CurrentState || len(output.NextSteps) != 1 {
		t.Fatalf("unexpected reviewed sections: %#v", output)
	}
}

func TestSchemaContracts(t *testing.T) {
	for _, command := range []string{
		"create", "session.locate", "receive", "context", "delete",
		"admin.login", "admin.status", "admin.logout",
		"config.show", "config.set-server", "doctor", "whoami", "update",
		"skills.list", "skills.read", "skills.install", "version",
		"auth", "config", "skills",
	} {
		contract, err := schemaContract(command)
		if err != nil {
			t.Fatalf("schemaContract(%q): %v", command, err)
		}
		if contract["inputSchema"] == nil || contract["outputSchema"] == nil || contract["_meta"] == nil {
			t.Fatalf("schemaContract(%q) is incomplete: %#v", command, contract)
		}
	}
	if _, err := schemaContract("missing"); err == nil {
		t.Fatal("expected unknown schema to fail")
	}
	create, _ := schemaContract("create")
	createProperties := create["inputSchema"].(map[string]any)["properties"].(map[string]any)
	for _, legacy := range []string{"upload_context", "mode", "from", "agent", "include_transcript", "full_session", "stdin", "compact"} {
		if _, exists := createProperties[legacy]; exists {
			t.Fatalf("preferred create schema exposes legacy property %q", legacy)
		}
	}
	receive, _ := schemaContract("receive")
	receiveRequired := receive["outputSchema"].(map[string]any)["required"].([]string)
	if strings.Contains(strings.Join(receiveRequired, ","), "share_message") {
		t.Fatal("receive output schema incorrectly reused create-only fields")
	}
}

func TestEmbeddedHandoffSkill(t *testing.T) {
	available := skillbundle.List()
	if len(available) != 1 || available[0].Name != "handoff" {
		t.Fatalf("unexpected embedded skills: %#v", available)
	}
	const description = "description: Package current work into a portable, immutable Handoff so another person or Agent can continue it"
	content, ok := skillbundle.Read("handoff")
	if !ok || !strings.Contains(content, "name: handoff") ||
		!strings.Contains(content, description) ||
		!strings.Contains(content, "--generator cloud") ||
		!strings.Contains(content, "--attach-context") ||
		!strings.Contains(content, "handoff context <reference>") ||
		!strings.Contains(content, "handoff session locate") ||
		strings.Contains(content, "--upload-context selected") ||
		strings.Contains(content, "--mode server") {
		t.Fatalf("embedded skill is incomplete: ok=%v content=%q", ok, content)
	}
	metadata, ok := skillbundle.OpenAIYAML("handoff")
	if !ok ||
		!strings.Contains(metadata, `display_name: "OpenGrove Handoff"`) ||
		!strings.Contains(metadata, "package current work for someone else to continue") {
		t.Fatalf("embedded Skill metadata is incomplete: ok=%v content=%q", ok, metadata)
	}
}

func TestDeleteRequiresStructuredConfirmation(t *testing.T) {
	err := runDelete("", "", []string{"abcdefghijklmnopqrstuv"})
	var structured *structuredError
	if !errors.As(err, &structured) {
		t.Fatalf("expected structured confirmation error, got %T: %v", err, err)
	}
	if structured.ExitCode != 10 {
		t.Fatalf("exit code = %d, want 10", structured.ExitCode)
	}
	envelope, _ := structured.Payload["error"].(map[string]any)
	if envelope["type"] != "confirmation_required" {
		t.Fatalf("unexpected error envelope: %#v", structured.Payload)
	}
}

func TestGlobalOutputFormatCanAppearBeforeOrAfterCommand(t *testing.T) {
	if _, format, err := extractOutputFormat([]string{"whoami"}); err != nil || format != "text" {
		t.Fatalf("default output format = %q, %v; want text", format, err)
	}
	for _, input := range [][]string{
		{"--json", "whoami"},
		{"whoami", "--json"},
		{"receive", "abcdefghijklmnopqrstuv", "--format=json"},
	} {
		cleaned, format, err := extractOutputFormat(input)
		if err != nil || format != "json" {
			t.Fatalf("extractOutputFormat(%v) = %v, %q, %v", input, cleaned, format, err)
		}
		for _, argument := range cleaned {
			if argument == "--json" || strings.HasPrefix(argument, "--format") {
				t.Fatalf("global output flag was not removed: %v", cleaned)
			}
		}
	}
	if _, _, err := extractOutputFormat([]string{"--json", "--format", "text", "version"}); err == nil {
		t.Fatal("expected conflicting formats to fail")
	}
}

func TestInstallSkillWritesSupportedAgentLocations(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HANDOFF_SKILL_HOME", root)
	paths, err := installSkill("handoff", "skill content\n", "all", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 3 {
		t.Fatalf("installed paths = %#v", paths)
	}
	for _, directory := range []string{".codex", ".claude", ".agents"} {
		path := filepath.Join(root, directory, "skills", "handoff", "SKILL.md")
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "skill content\n" {
			t.Fatalf("installed Skill %s = %q, %v", path, data, err)
		}
		metadataPath := filepath.Join(root, directory, "skills", "handoff", "agents", "openai.yaml")
		metadata, err := os.ReadFile(metadataPath)
		if err != nil || !strings.Contains(string(metadata), "OpenGrove Handoff") {
			t.Fatalf("installed Skill metadata %s = %q, %v", metadataPath, metadata, err)
		}
	}
	if _, err := installSkill("handoff", "different\n", "all", false); err == nil {
		t.Fatal("expected existing different Skill to require --force")
	}
}
