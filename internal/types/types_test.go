package types

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLocalSessionPathIsNeverSerialized(t *testing.T) {
	encoded, err := json.Marshal(Context{
		Source:      "codex",
		SessionPath: "/Users/alice/.codex/sessions/private.jsonl",
		FullSession: true,
		Messages:    []Message{{Role: "user", Text: "known"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private.jsonl") || strings.Contains(string(encoded), "/Users/alice") {
		t.Fatalf("serialized context leaked local Session path: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"full_session":true`) {
		t.Fatalf("serialized context lost full_session flag: %s", encoded)
	}
}
