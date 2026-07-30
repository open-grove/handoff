package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestParseCodexUsesFirstSessionMetaAsForkIdentity(t *testing.T) {
	input := strings.Join([]string{
		`{"timestamp":"2026-07-30T03:28:00Z","type":"session_meta","payload":{"id":"child","session_id":"parent","parent_thread_id":"parent","thread_source":"subagent","agent_path":"/root/audit","cwd":"/work/demo","timestamp":"2026-07-30T03:28:00Z"}}`,
		`{"timestamp":"2026-07-30T03:20:00Z","type":"session_meta","payload":{"id":"parent","session_id":"parent","thread_source":"user","cwd":"/work/demo"}}`,
		`{"timestamp":"2026-07-30T03:29:00Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"child result"}]}}`,
	}, "\n")
	result, err := ParseCodex(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "child" || result.ParentSessionID != "parent" || result.ThreadSource != "subagent" || result.AgentPath != "/root/audit" {
		t.Fatalf("fork identity was overwritten: %#v", result)
	}
}

func TestParseCodexHonorsFinalAbortAndRollbackEvents(t *testing.T) {
	message := func(turnID, role, phase, text string) string {
		return `{"timestamp":"2026-07-30T03:30:00Z","type":"response_item","payload":{"type":"message","role":"` + role + `","phase":"` + phase + `","content":[{"type":"output_text","text":"` + text + `"}],"internal_chat_message_metadata_passthrough":{"turn_id":"` + turnID + `"}}}`
	}
	input := strings.Join([]string{
		`{"timestamp":"2026-07-30T03:28:00Z","type":"session_meta","payload":{"id":"root","thread_source":"user","cwd":"/work/demo"}}`,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		message("t1", "user", "", "question 1"),
		message("t1", "assistant", "commentary", "working 1"),
		message("t1", "assistant", "final_answer", "answer 1"),
		`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t1"}}`,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t2"}}`,
		message("t2", "user", "", "question 2"),
		message("t2", "assistant", "commentary", "aborted work"),
		`{"type":"event_msg","payload":{"type":"turn_aborted","turn_id":"t2"}}`,
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t3"}}`,
		message("t3", "user", "", "question 3"),
		message("t3", "assistant", "commentary", "still provisional"),
		`{"type":"event_msg","payload":{"type":"task_started","turn_id":"t4"}}`,
		message("t4", "user", "", "question 4"),
		message("t4", "assistant", "final_answer", "rolled back answer"),
		`{"type":"event_msg","payload":{"type":"task_complete","turn_id":"t4"}}`,
		`{"type":"event_msg","payload":{"type":"thread_rolled_back","num_turns":1}}`,
	}, "\n")
	result, err := ParseCodex(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, item := range result.Messages {
		texts = append(texts, item.Text)
	}
	got := strings.Join(texts, "|")
	for _, expected := range []string{"question 1", "answer 1", "question 2", "question 3", "[Provisional commentary; not a final answer]\nstill provisional"} {
		if !strings.Contains(got, expected) {
			t.Fatalf("missing %q from %q", expected, got)
		}
	}
	for _, excluded := range []string{"working 1", "aborted work", "question 4", "rolled back answer"} {
		if strings.Contains(got, excluded) {
			t.Fatalf("retained non-canonical message %q in %q", excluded, got)
		}
	}
}

func TestParseCodexCollectsDirectChildSessions(t *testing.T) {
	input := strings.Join([]string{
		`{"timestamp":"2026-07-30T03:28:00Z","type":"session_meta","payload":{"id":"root","thread_source":"user","cwd":"/work/demo","timestamp":"2026-07-30T03:28:00Z"}}`,
		`{"timestamp":"2026-07-30T03:29:00Z","type":"event_msg","payload":{"type":"sub_agent_activity","agent_thread_id":"child","agent_path":"/root/audit","kind":"started"}}`,
	}, "\n")
	result, err := ParseCodex(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ChildSessions) != 1 || result.ChildSessions[0].ID != "child" || result.ChildSessions[0].AgentPath != "/root/audit" {
		t.Fatalf("child session refs = %#v", result.ChildSessions)
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

func TestParseClaudeSkipsToolResultsAndLabelsSidechains(t *testing.T) {
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
	if result.SessionID != "session-2" || len(result.Messages) != 3 || result.Cursor != "last" {
		t.Fatalf("unexpected parse result: %#v", result)
	}
	if result.Messages[1].Text != "[Sidechain result; supporting context, not a final answer]\nside work" {
		t.Fatalf("sidechain was dropped or treated as final: %#v", result.Messages)
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
	t.Setenv("CODEX_THREAD_ID", "")
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

func TestAutoPrefersCurrentCodexThreadOverNewerSession(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionDir := filepath.Join(home, ".codex", "sessions", "2026", "07", "30")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeCodexSession := func(id, text string, modTime time.Time) string {
		path := filepath.Join(sessionDir, "rollout-"+id+".jsonl")
		data := `{"type":"session_meta","payload":{"id":"` + id + `","thread_source":"user","cwd":` + quote(workspace) + `}}` + "\n" +
			`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"` + text + `"}]}}` + "\n"
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, modTime, modTime); err != nil {
			t.Fatal(err)
		}
		return path
	}
	now := time.Now()
	currentPath := writeCodexSession("current-thread", "current", now.Add(-time.Hour))
	writeCodexSession("newer-thread", "newer", now)
	t.Setenv("CODEX_THREAD_ID", "current-thread")
	result, err := Load(Options{Kind: "auto", Home: home, CWD: workspace, NoGit: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "current-thread" || result.SessionPath != currentPath {
		t.Fatalf("selected wrong active session: %#v", result)
	}
}

func TestAutoPrefersRootSessionOverNewerSubagent(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "repo")
	sessionDir := filepath.Join(home, ".codex", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(sessionDir, "rollout-root.jsonl")
	childPath := filepath.Join(sessionDir, "rollout-child.jsonl")
	root := `{"type":"session_meta","payload":{"id":"root","thread_source":"user","cwd":` + quote(workspace) + `}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"user","content":"root"}}` + "\n"
	child := `{"type":"session_meta","payload":{"id":"child","session_id":"root","thread_source":"subagent","cwd":` + quote(workspace) + `}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"user","content":"child"}}` + "\n"
	if err := os.WriteFile(rootPath, []byte(root), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(childPath, []byte(child), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := os.Chtimes(rootPath, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(childPath, now, now); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_THREAD_ID", "")
	result, err := Load(Options{Kind: "auto", Home: home, CWD: workspace, NoGit: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "root" {
		t.Fatalf("selected subagent instead of root: %#v", result)
	}
}

func TestAutoMergesDirectChildFinalAnswer(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "repo")
	sessionDir := filepath.Join(home, ".codex", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(sessionDir, "rollout-root.jsonl")
	childPath := filepath.Join(sessionDir, "rollout-child.jsonl")
	root := strings.Join([]string{
		`{"timestamp":"2026-07-30T03:28:00Z","type":"session_meta","payload":{"id":"root","thread_source":"user","cwd":` + quote(workspace) + `,"timestamp":"2026-07-30T03:28:00Z"}}`,
		`{"timestamp":"2026-07-30T03:29:00Z","type":"response_item","payload":{"type":"message","role":"user","content":"audit"}}`,
		`{"timestamp":"2026-07-30T03:29:01Z","type":"event_msg","payload":{"type":"sub_agent_activity","agent_thread_id":"child","agent_path":"/root/audit","kind":"started"}}`,
	}, "\n")
	child := strings.Join([]string{
		`{"timestamp":"2026-07-30T03:30:00Z","type":"session_meta","payload":{"id":"child","session_id":"root","parent_thread_id":"root","thread_source":"subagent","agent_path":"/root/audit","cwd":` + quote(workspace) + `,"timestamp":"2026-07-30T03:30:00Z"}}`,
		`{"timestamp":"2026-07-30T03:20:00Z","type":"session_meta","payload":{"id":"root","thread_source":"user","cwd":` + quote(workspace) + `}}`,
		`{"timestamp":"2026-07-30T03:20:01Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":"inherited parent answer","internal_chat_message_metadata_passthrough":{"turn_id":"parent-turn"}}}`,
		`{"timestamp":"2026-07-30T03:30:01Z","type":"event_msg","payload":{"type":"task_started","turn_id":"child-turn"}}`,
		`{"timestamp":"2026-07-30T03:30:02Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"commentary","content":"child working","internal_chat_message_metadata_passthrough":{"turn_id":"child-turn"}}}`,
		`{"timestamp":"2026-07-30T03:30:03Z","type":"response_item","payload":{"type":"message","role":"assistant","phase":"final_answer","content":"verified child result","internal_chat_message_metadata_passthrough":{"turn_id":"child-turn"}}}`,
		`{"timestamp":"2026-07-30T03:30:04Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"child-turn"}}`,
	}, "\n")
	if err := os.WriteFile(rootPath, []byte(root), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(childPath, []byte(child), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_THREAD_ID", "")
	result, err := Load(Options{Kind: "auto", Home: home, CWD: workspace, NoGit: true})
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, message := range result.Messages {
		joined += message.Text + "\n"
	}
	if !strings.Contains(joined, "[Sub-agent final result: /root/audit]\nverified child result") {
		t.Fatalf("child final was not merged: %q", joined)
	}
	for _, excluded := range []string{"inherited parent answer", "child working"} {
		if strings.Contains(joined, excluded) {
			t.Fatalf("merged non-final child content %q: %q", excluded, joined)
		}
	}
}

func TestParseJSONLinesRejectsMalformedRecords(t *testing.T) {
	_, err := ParseCodex(strings.NewReader("{\"type\":\"session_meta\",\"payload\":{}}\nnot-json\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("malformed JSONL was silently skipped: %v", err)
	}
}

func TestFromReaderRejectsOversizedInput(t *testing.T) {
	_, err := fromReader("stdin", strings.NewReader(strings.Repeat("x", maxStdinBytes+1)), "/work/demo")
	if err == nil || !strings.Contains(err.Error(), "exceeds the 4 MiB limit") {
		t.Fatalf("oversized stdin was silently truncated: %v", err)
	}
}

func quote(value string) string {
	return `"` + strings.ReplaceAll(value, `\`, `\\`) + `"`
}
