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

func TestParseOpenCodeKeepsOnlyReadableTextAndNativeSummary(t *testing.T) {
	input := `{
  "info":{"id":"ses_current","directory":"/work/demo","time":{"created":1785800000000,"updated":1785800100000}},
  "messages":[
    {"info":{"id":"msg_user","sessionID":"ses_current","role":"user","time":{"created":1785800001000}},"parts":[
      {"type":"text","text":"design it"},
      {"type":"text","text":"hidden system context","synthetic":true},
      {"type":"file","filename":"secret.txt","url":"file:///secret.txt"}
    ]},
    {"info":{"id":"msg_tool_step","sessionID":"ses_current","parentID":"msg_user","role":"assistant","finish":"tool-calls","time":{"created":1785800002000,"completed":1785800003000}},"parts":[
      {"type":"reasoning","text":"private chain of thought"},
      {"type":"text","text":"I am checking."},
      {"type":"tool","state":{"status":"completed","input":{"path":"secret"},"output":"private tool result"}}
    ]},
    {"info":{"id":"msg_final","sessionID":"ses_current","parentID":"msg_user","role":"assistant","finish":"stop","time":{"created":1785800004000,"completed":1785800005000}},"parts":[
      {"type":"text","text":"Finished design."}
    ]},
    {"info":{"id":"msg_compact","sessionID":"ses_current","role":"user","time":{"created":1785800006000}},"parts":[
      {"type":"compaction","auto":true}
    ]},
    {"info":{"id":"msg_summary","sessionID":"ses_current","parentID":"msg_compact","role":"assistant","summary":true,"finish":"stop","time":{"created":1785800007000,"completed":1785800008000}},"parts":[
      {"type":"text","text":"Native OpenCode summary."}
    ]},
    {"info":{"id":"msg_error","sessionID":"ses_current","parentID":"msg_user","role":"assistant","error":{"name":"AbortedError"},"finish":"stop","time":{"created":1785800009000,"completed":1785800010000}},"parts":[
      {"type":"text","text":"aborted response"}
    ]}
  ]
}`
	result, err := ParseOpenCode(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "ses_current" || result.CWD != "/work/demo" || len(result.Messages) != 3 {
		t.Fatalf("unexpected OpenCode parse result: %#v", result)
	}
	if result.Messages[0].Text != "design it" || result.Messages[1].Text != "[Provisional assistant message; not a completed response]\nI am checking." || result.Messages[2].Text != "Finished design." {
		t.Fatalf("unexpected readable messages: %#v", result.Messages)
	}
	joined := result.Messages[0].Text + result.Messages[1].Text + result.Messages[2].Text
	for _, excluded := range []string{"hidden system context", "secret.txt", "private chain of thought", "private tool result", "aborted response"} {
		if strings.Contains(joined, excluded) {
			t.Fatalf("retained provider-private OpenCode content %q in %q", excluded, joined)
		}
	}
	if !result.NativeCompactFound || result.Summary != "Native OpenCode summary." || result.Cursor != "msg_error" {
		t.Fatalf("native OpenCode summary was not preserved as auxiliary context: %#v", result)
	}
}

func TestAutoPrefersActiveOpenCodeSessionAndExportsItReadOnly(t *testing.T) {
	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	record := filepath.Join(t.TempDir(), "calls.txt")
	command := filepath.Join(t.TempDir(), "opencode")
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> ` + quote(record) + `
if [ "$1" = "session" ] && [ "$2" = "list" ]; then
  printf '%s\n' '[{"id":"ses_newer","title":"newer","updated":2000,"created":1000,"projectId":"p","directory":` + quote(workspace) + `},{"id":"ses_active","title":"active","updated":1000,"created":500,"projectId":"p","directory":` + quote(workspace) + `}]'
  exit 0
fi
if [ "$1" = "export" ]; then
  text="newer"
  if [ "$2" = "ses_active" ]; then text="active"; fi
  printf '{"info":{"id":"%s","directory":` + quote(workspace) + `,"time":{"created":500,"updated":1000}},"messages":[{"info":{"id":"msg","role":"user","time":{"created":600}},"parts":[{"type":"text","text":"%s"}]}]}\n' "$2" "$text"
  exit 0
fi
exit 2
`
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_THREAD_ID", "")
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("PI_SESSION_ID", "")
	t.Setenv("OPENCODE", "1")
	t.Setenv("OPENCODE_SESSION_ID", "ses_active")
	result, err := Load(Options{Kind: "auto", Home: home, CWD: workspace, NoGit: true, OpenCodeCommand: command})
	if err != nil {
		t.Fatal(err)
	}
	if result.Source != "opencode" || result.SessionID != "ses_active" || len(result.Messages) != 1 || result.Messages[0].Text != "active" || result.SessionPath != "" {
		t.Fatalf("selected wrong OpenCode session: %#v", result)
	}
	calls, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(calls), "session list") || !strings.Contains(string(calls), "export ses_active --pure") || strings.Contains(string(calls), "export ses_newer") {
		t.Fatalf("unexpected OpenCode calls: %s", calls)
	}

	// OpenCode sets OPENCODE=1 for shell tools but does not currently expose
	// its active Session ID. In that normal case, the newest root Session in
	// the current workspace is the best available read-only match.
	t.Setenv("OPENCODE_SESSION_ID", "")
	result, err = Load(Options{Kind: "auto", Home: home, CWD: workspace, NoGit: true, OpenCodeCommand: command})
	if err != nil {
		t.Fatal(err)
	}
	if result.SessionID != "ses_newer" || len(result.Messages) != 1 || result.Messages[0].Text != "newer" {
		t.Fatalf("did not select newest OpenCode workspace Session: %#v", result)
	}
	calls, err = os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "session list --format json --max-count 200 --pure") || !strings.Contains(string(calls), "export ses_newer --pure") {
		t.Fatalf("OpenCode fallback discovery was not used: %s", calls)
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
