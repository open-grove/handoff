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

func TestParseCodexIgnoresSummaryConfiguration(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"session-1","cwd":"/work/demo"}}`,
		`{"type":"turn_context","payload":{"summary":"auto"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"keep me"}]}}`,
	}, "\n")
	result, err := ParseCodex(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "" || result.NativeCompactFound {
		t.Fatalf("turn context config was treated as a native summary: %#v", result)
	}
}

func TestParseCodexKeepsReadableHistoryAndNativeSummary(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"old"}]}}`,
		`{"type":"compacted","payload":{"message":"native summary"}}`,
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"tail"}]}}`,
	}, "\n")
	result, err := ParseCodex(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "native summary" || !result.NativeCompactFound || len(result.Messages) != 2 || result.Messages[0].Text != "old" || result.Messages[1].Text != "tail" {
		t.Fatalf("unexpected compacted context: %#v", result)
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

func TestParseClaudeKeepsReadableHistoryAndCompactSummary(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"user","message":{"role":"user","content":"old"}}`,
		`{"type":"system","subtype":"compact_boundary","content":"Conversation compacted"}`,
		`{"type":"user","isCompactSummary":true,"uuid":"summary-id","message":{"role":"user","content":"native summary"}}`,
		`{"type":"assistant","uuid":"tail-id","message":{"role":"assistant","content":"tail"}}`,
	}, "\n")
	result, err := ParseClaude(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "native summary" || !result.NativeCompactFound || len(result.Messages) != 2 || result.Messages[0].Text != "old" || result.Messages[1].Text != "tail" || result.Cursor != "tail-id" {
		t.Fatalf("unexpected compacted context: %#v", result)
	}
}

func TestParsePiKeepsReadableHistoryAndCompactSummary(t *testing.T) {
	input := strings.Join([]string{
		`{"type":"message","id":"old","message":{"role":"user","content":"old"}}`,
		`{"type":"message","id":"kept","message":{"role":"assistant","content":"kept"}}`,
		`{"type":"compaction","summary":"native summary","firstKeptEntryId":"kept"}`,
		`{"type":"message","id":"tail","message":{"role":"user","content":"tail"}}`,
	}, "\n")
	result, err := ParsePi(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if result.Summary != "native summary" || !result.NativeCompactFound || len(result.Messages) != 3 || result.Messages[0].Text != "old" || result.Messages[1].Text != "kept" || result.Messages[2].Text != "tail" {
		t.Fatalf("unexpected compacted context: %#v", result)
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
	if result.Source != "codex" || result.SessionID != "session-3" || result.SessionPath != path || result.FullSession {
		t.Fatalf("unexpected source: %#v", result)
	}
}

func quote(value string) string {
	return `"` + strings.ReplaceAll(value, `\`, `\\`) + `"`
}
