package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCodex(t *testing.T) {
	input := strings.Join([]string{
		`{"timestamp":"2026-07-22T08:00:00Z","type":"session_meta","payload":{"id":"session-1","cwd":"/work/demo"}}`,
		`{"timestamp":"2026-07-22T08:01:00Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"ignore"}]}}`,
		`{"timestamp":"2026-07-22T08:02:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"build it"}]}}`,
		`{"timestamp":"2026-07-22T08:03:00Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}`,
	}, "\n")
	result, err := ParseCodex(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "session-1" || result.CWD != "/work/demo" || len(result.Messages) != 2 {
		t.Fatalf("unexpected parse result: %#v", result)
	}
}

func TestParseClaudeSkipsToolResultsAndSidechains(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"user","sessionId":"session-2","cwd":"/work/demo","timestamp":"2026-07-22T08:00:00Z","message":{"role":"user","content":"build it"}}`,
		`{"type":"user","sessionId":"session-2","cwd":"/work/demo","message":{"role":"user","content":[{"type":"tool_result","content":"secret noise"}]}}`,
		`{"type":"assistant","sessionId":"session-2","cwd":"/work/demo","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"side work"}]}}`,
		`{"type":"assistant","sessionId":"session-2","cwd":"/work/demo","uuid":"last","message":{"role":"assistant","content":[{"type":"text","text":"done"}]}}`,
	}, "\n")
	result, err := ParseClaude(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "session-2" || len(result.Messages) != 2 || result.Cursor != "last" {
		t.Fatalf("unexpected parse result: %#v", result)
	}
}

func TestAutoSelectsLatestMatchingWorkspace(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(home, ".codex", "sessions", "2026", "07", "22")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(sessionDir, "session.jsonl")
	data := `{"type":"session_meta","payload":{"id":"session-3","cwd":` + quote(workspace) + `}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"continue"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := Load(Options{Kind: "auto", Home: home, CWD: workspace, NoGit: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "codex" || result.SessionID != "session-3" {
		t.Fatalf("unexpected source: %#v", result)
	}
}

func quote(value string) string {
	return `"` + strings.ReplaceAll(value, `\`, `\\`) + `"`
}
