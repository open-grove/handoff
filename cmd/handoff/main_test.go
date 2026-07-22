package main

import (
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
		Handoff:  types.Handoff{ID: "abcdefghijklmnopqrstuv", ExpiresAt: expiresAt},
		ShareURL: "https://handoff.openmau.com/h/abcdefghijklmnopqrstuv",
	}
	message := formatShareMessage(result)
	for _, expected := range []string{
		"**给人看**",
		"[打开交接文档](https://handoff.openmau.com/h/abcdefghijklmnopqrstuv)",
		"浏览器直接打开，无需安装 Handoff",
		"**给 Agent**",
		"`opengrove-handoff，分享码：abcdefghijklmnopqrstuv`",
		"[查看安装方法](https://github.com/open-grove/handoff)",
		"有效期：" + expiresAt.Format(time.RFC3339),
	} {
		if !strings.Contains(message, expected) {
			t.Fatalf("share message missing %q:\n%s", expected, message)
		}
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
	if !ok || !strings.Contains(content, "name: handoff") || !strings.Contains(content, "--compact server") {
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
