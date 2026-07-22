package main

import (
	"context"
	"errors"
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
		"请使用 opengrove-handoff 读取内容，分享码：`abcdefghijklmnopqrstuv`",
		"[查看安装方法](https://github.com/open-grove/handoff)",
		"有效期：" + expiresAt.Format(time.RFC3339),
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("share message missing %q:\n%s", expected, message)
		}
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
	for _, command := range []string{"create", "receive", "delete"} {
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
}

func TestEmbeddedHandoffSkill(t *testing.T) {
	available := skillbundle.List()
	if len(available) != 1 || available[0].Name != "handoff" {
		t.Fatalf("unexpected embedded skills: %#v", available)
	}
	content, ok := skillbundle.Read("handoff")
	if !ok || !strings.Contains(content, "name: handoff") || !strings.Contains(content, "--mode server") {
		t.Fatalf("embedded skill is incomplete: ok=%v content=%q", ok, content)
	}
}

func TestDeleteRequiresStructuredConfirmation(t *testing.T) {
	err := runDelete("", []string{"abcdefghijklmnopqrstuv"})
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
