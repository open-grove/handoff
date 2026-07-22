package main

import (
	"errors"
	"strings"
	"testing"

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
}

func TestParseHandoffRefRejectsInvalidValues(t *testing.T) {
	if id, _ := parseHandoffRef("short"); id != "" {
		t.Fatalf("accepted invalid id %q", id)
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
