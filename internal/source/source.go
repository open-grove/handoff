package source

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/open-grove/handoff/internal/types"
)

type Options struct {
	Kind               string
	Files              []string
	ReadStdin          bool
	Stdin              io.Reader
	CWD                string
	Home               string
	NoGit              bool
	PreferredSource    string
	PreferredSessionID string
}

type candidate struct {
	kind    string
	path    string
	modTime time.Time
}

const maxStdinBytes = 4 << 20

func Load(options Options) (types.Context, error) {
	cwd := options.CWD
	if cwd == "" {
		var err error
		cwd, err = os.Getwd()
		if err != nil {
			return types.Context{}, err
		}
	}
	cwd, _ = filepath.Abs(cwd)

	var result types.Context
	var err error
	switch {
	case len(options.Files) > 0:
		result, err = fromFiles(options.Files, cwd)
	case options.ReadStdin:
		reader := options.Stdin
		if reader == nil {
			reader = os.Stdin
		}
		result, err = fromReader("stdin", reader, cwd)
	default:
		home := options.Home
		if home == "" {
			home, err = os.UserHomeDir()
			if err != nil {
				return types.Context{}, err
			}
		}
		preferredSource := strings.ToLower(strings.TrimSpace(options.PreferredSource))
		preferredSessionID := strings.TrimSpace(options.PreferredSessionID)
		if preferredSessionID == "" {
			preferredSource, preferredSessionID = activeSessionHint(options.Kind)
		}
		result, err = fromAgent(options.Kind, home, cwd, preferredSource, preferredSessionID)
	}
	if err != nil {
		return types.Context{}, err
	}
	if !options.NoGit {
		result.Repo = inspectRepository(cwd)
	}
	return result, nil
}

func fromReader(kind string, reader io.Reader, cwd string) (types.Context, error) {
	data, err := readLimited(reader, maxStdinBytes)
	if err != nil {
		return types.Context{}, err
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return types.Context{}, errors.New("no context received on stdin")
	}
	now := time.Now().UTC()
	return types.Context{
		Source:    kind,
		CWD:       cwd,
		UpdatedAt: now,
		Cursor:    "input:1",
		Messages:  []types.Message{{Role: "user", Text: text, At: now}},
	}, nil
}

func readLimited(reader io.Reader, limit int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("stdin context exceeds the %d MiB limit", limit>>20)
	}
	return data, nil
}

func fromFiles(paths []string, cwd string) (types.Context, error) {
	messages := make([]types.Message, 0, len(paths))
	var updated time.Time
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return types.Context{}, fmt.Errorf("read %s: %w", path, err)
		}
		info, err := os.Stat(path)
		if err == nil && info.ModTime().After(updated) {
			updated = info.ModTime().UTC()
		}
		messages = append(messages, types.Message{
			Role: "user",
			Text: fmt.Sprintf("File: %s\n\n%s", filepath.Base(path), strings.TrimSpace(string(data))),
			At:   infoTime(info),
		})
	}
	if updated.IsZero() {
		updated = time.Now().UTC()
	}
	return types.Context{
		Source:    "file",
		CWD:       cwd,
		UpdatedAt: updated,
		Cursor:    fmt.Sprintf("files:%d", len(paths)),
		Messages:  messages,
	}, nil
}

func fromAgent(kind, home, cwd, preferredSource, preferredSessionID string) (types.Context, error) {
	if kind == "" {
		kind = "auto"
	}
	if kind != "auto" && kind != "codex" && kind != "claude" && kind != "pi" {
		return types.Context{}, fmt.Errorf("unknown source %q (use auto, codex, claude, pi, stdin, or --file)", kind)
	}

	var candidates []candidate
	if kind == "auto" || kind == "codex" {
		candidates = append(candidates, findCandidates("codex", filepath.Join(home, ".codex", "sessions"))...)
	}
	if kind == "auto" || kind == "claude" {
		candidates = append(candidates, findCandidates("claude", filepath.Join(home, ".claude", "projects"))...)
	}
	if kind == "auto" || kind == "pi" {
		candidates = append(candidates, findCandidates("pi", filepath.Join(home, ".pi", "agent", "sessions"))...)
		candidates = append(candidates, findCandidates("pi", filepath.Join(home, ".pi", "sessions"))...)
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		leftPreferred := candidateMatchesSession(candidates[i], preferredSource, preferredSessionID)
		rightPreferred := candidateMatchesSession(candidates[j], preferredSource, preferredSessionID)
		if leftPreferred != rightPreferred {
			return leftPreferred
		}
		return candidates[i].modTime.After(candidates[j].modTime)
	})
	allCandidates := candidates
	if len(candidates) > 200 {
		candidates = candidates[:200]
	}

	var selected *types.Context
	selectedRank := 100
	for _, item := range candidates {
		parsed, parseErr := loadCandidate(item)
		if parseErr != nil {
			if candidateMatchesSession(item, preferredSource, preferredSessionID) || sameWorkspace(parsed.CWD, cwd) {
				return types.Context{}, fmt.Errorf("parse Agent session %s: %w", item.path, parseErr)
			}
			continue
		}
		if len(parsed.Messages) == 0 || !sameWorkspace(parsed.CWD, cwd) {
			continue
		}
		parsed.Source = item.kind
		parsed.SessionPath = item.path
		parsed.UpdatedAt = item.modTime.UTC()
		rank := sessionCandidateRank(parsed, item.kind, preferredSource, preferredSessionID)
		if selected == nil || rank < selectedRank {
			copy := parsed
			selected = &copy
			selectedRank = rank
		}
		if rank == 0 {
			break
		}
	}
	if selected == nil {
		return types.Context{}, errors.New("no recent Agent session matched this workspace; pipe context on stdin or pass --file")
	}
	if selected.Source == "codex" {
		if err := mergeCodexChildFinals(selected, allCandidates, cwd); err != nil {
			return types.Context{}, err
		}
	}
	return *selected, nil
}

func activeSessionHint(requestedKind string) (string, string) {
	requestedKind = strings.ToLower(strings.TrimSpace(requestedKind))
	allowed := func(kind string) bool { return requestedKind == "" || requestedKind == "auto" || requestedKind == kind }
	switch {
	case allowed("codex") && strings.TrimSpace(os.Getenv("CODEX_THREAD_ID")) != "":
		return "codex", strings.TrimSpace(os.Getenv("CODEX_THREAD_ID"))
	case allowed("claude") && strings.TrimSpace(os.Getenv("CLAUDE_CODE_SESSION_ID")) != "":
		return "claude", strings.TrimSpace(os.Getenv("CLAUDE_CODE_SESSION_ID"))
	case allowed("pi") && strings.TrimSpace(os.Getenv("PI_SESSION_ID")) != "":
		return "pi", strings.TrimSpace(os.Getenv("PI_SESSION_ID"))
	default:
		return "", ""
	}
}

func candidateMatchesSession(item candidate, preferredSource, preferredSessionID string) bool {
	return preferredSessionID != "" && item.kind == preferredSource && strings.Contains(filepath.Base(item.path), preferredSessionID)
}

func sessionCandidateRank(parsed types.Context, kind, preferredSource, preferredSessionID string) int {
	if preferredSessionID != "" && kind == preferredSource && parsed.SessionID == preferredSessionID {
		return 0
	}
	if preferredSource != "" && kind == preferredSource && parsed.ThreadSource != "subagent" {
		return 1
	}
	if parsed.ThreadSource != "subagent" {
		return 2
	}
	if preferredSource != "" && kind == preferredSource {
		return 3
	}
	return 4
}

func loadCandidate(item candidate) (types.Context, error) {
	file, err := os.Open(item.path)
	if err != nil {
		return types.Context{}, err
	}
	defer file.Close()
	switch item.kind {
	case "codex":
		return parseCodex(file)
	case "claude":
		return parseClaude(file)
	default:
		return parsePi(file)
	}
}

func findCandidates(kind, root string) []candidate {
	var output []candidate
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		info, err := entry.Info()
		if err == nil {
			output = append(output, candidate{kind: kind, path: path, modTime: info.ModTime()})
		}
		return nil
	})
	return output
}

func ParseCodex(reader io.Reader) (types.Context, error) {
	return parseCodex(reader)
}

func parseCodex(reader io.Reader) (types.Context, error) {
	var output types.Context
	var identityFound bool
	var createdAt time.Time
	currentTurn := ""
	turnOrder := make([]string, 0)
	knownTurns := map[string]bool{}
	completedTurns := map[string]bool{}
	abortedTurns := map[string]bool{}
	rolledBackTurns := map[string]bool{}
	ownedTurns := map[string]bool{}
	lastAgentMessage := map[string]string{}
	childSessions := map[string]types.ChildSessionRef{}
	err := parseJSONLines(reader, func(line int, value map[string]any) {
		payload := object(value["payload"])
		eventAt := parseTime(value["timestamp"])
		switch stringValue(value["type"]) {
		case "session_meta":
			if identityFound {
				return
			}
			identityFound = true
			output.CWD = stringValue(payload["cwd"])
			output.SessionID = firstString(payload["id"], payload["session_id"])
			output.ThreadSource = stringValue(payload["thread_source"])
			output.ParentSessionID = firstString(payload["parent_thread_id"], nestedString(payload, "source", "subagent", "thread_spawn", "parent_thread_id"))
			if output.ParentSessionID == "" {
				if sessionID := stringValue(payload["session_id"]); sessionID != output.SessionID {
					output.ParentSessionID = sessionID
				}
			}
			output.AgentPath = firstString(payload["agent_path"], nestedString(payload, "source", "subagent", "thread_spawn", "agent_path"))
			if output.ThreadSource == "" && output.ParentSessionID != "" {
				output.ThreadSource = "subagent"
			}
			createdAt = parseTime(payload["timestamp"])
			if createdAt.IsZero() {
				createdAt = eventAt
			}
		case "compacted":
			output.NativeCompactFound = true
			if summary := codexCompactSummary(payload); summary != "" {
				output.Summary = summary
			}
		case "event_msg":
			switch stringValue(payload["type"]) {
			case "task_started":
				currentTurn = stringValue(payload["turn_id"])
				if currentTurn == "" {
					return
				}
				if !knownTurns[currentTurn] {
					knownTurns[currentTurn] = true
					turnOrder = append(turnOrder, currentTurn)
				}
				if output.ThreadSource != "subagent" || eventOwned(eventAt, createdAt) {
					ownedTurns[currentTurn] = true
				}
			case "task_complete":
				turnID := firstString(payload["turn_id"], currentTurn)
				if turnID != "" {
					completedTurns[turnID] = true
					if text := stringValue(payload["last_agent_message"]); text != "" {
						lastAgentMessage[turnID] = text
					}
				}
			case "turn_aborted":
				if turnID := firstString(payload["turn_id"], currentTurn); turnID != "" {
					abortedTurns[turnID] = true
				}
			case "thread_rolled_back":
				count := intValue(payload["num_turns"])
				for count > 0 && len(turnOrder) > 0 {
					index := len(turnOrder) - 1
					turnID := turnOrder[index]
					turnOrder = turnOrder[:index]
					rolledBackTurns[turnID] = true
					delete(knownTurns, turnID)
					count--
				}
				currentTurn = ""
			case "sub_agent_activity":
				if output.ThreadSource == "subagent" && !eventOwned(eventAt, createdAt) {
					return
				}
				id := stringValue(payload["agent_thread_id"])
				if id == "" || id == output.SessionID {
					return
				}
				childSessions[id] = types.ChildSessionRef{
					ID:        id,
					AgentPath: stringValue(payload["agent_path"]),
				}
			}
		case "response_item":
			if stringValue(payload["type"]) != "message" {
				return
			}
			role := stringValue(payload["role"])
			if role != "user" && role != "assistant" {
				return
			}
			if text := contentText(payload["content"]); text != "" {
				turnID := firstString(nestedString(payload, "internal_chat_message_metadata_passthrough", "turn_id"), currentTurn)
				owned := output.ThreadSource != "subagent" || ownedTurns[turnID] || eventOwned(eventAt, createdAt)
				output.Messages = append(output.Messages, types.Message{
					Role:   role,
					Text:   text,
					At:     eventAt,
					Phase:  stringValue(payload["phase"]),
					TurnID: turnID,
					Owned:  owned,
				})
				output.Cursor = fmt.Sprintf("line:%d", line)
			}
		}
	})
	if err != nil {
		return output, err
	}
	output.Messages = finalizeCodexMessages(output.Messages, turnOrder, completedTurns, abortedTurns, rolledBackTurns, ownedTurns, lastAgentMessage, output.ThreadSource != "subagent")
	for _, ref := range childSessions {
		output.ChildSessions = append(output.ChildSessions, ref)
	}
	sort.Slice(output.ChildSessions, func(i, j int) bool {
		return output.ChildSessions[i].ID < output.ChildSessions[j].ID
	})
	return output, nil
}

func finalizeCodexMessages(
	messages []types.Message,
	turnOrder []string,
	completedTurns, abortedTurns, rolledBackTurns map[string]bool,
	ownedTurns map[string]bool,
	lastAgentMessage map[string]string,
	rootSession bool,
) []types.Message {
	hasFinal := map[string]bool{}
	for index := range messages {
		message := &messages[index]
		if message.Role != "assistant" || abortedTurns[message.TurnID] || rolledBackTurns[message.TurnID] {
			continue
		}
		if completedTurns[message.TurnID] && strings.TrimSpace(message.Text) == strings.TrimSpace(lastAgentMessage[message.TurnID]) {
			message.Phase = "final_answer"
		}
		if message.Phase == "final_answer" {
			hasFinal[message.TurnID] = true
		}
	}
	for _, turnID := range turnOrder {
		text := lastAgentMessage[turnID]
		if text == "" || abortedTurns[turnID] || rolledBackTurns[turnID] || hasFinal[turnID] {
			continue
		}
		messages = append(messages, types.Message{
			Role:      "assistant",
			Text:      text,
			Phase:     "final_answer",
			TurnID:    turnID,
			Owned:     rootSession || ownedTurns[turnID],
			Completed: true,
		})
		hasFinal[turnID] = true
	}
	output := messages[:0]
	for _, message := range messages {
		if rolledBackTurns[message.TurnID] {
			continue
		}
		if message.Role == "assistant" && abortedTurns[message.TurnID] {
			continue
		}
		message.Completed = completedTurns[message.TurnID] || message.Phase == "final_answer"
		if message.Role == "assistant" && message.Phase == "commentary" {
			if hasFinal[message.TurnID] {
				continue
			}
			message.Text = "[Provisional commentary; not a final answer]\n" + message.Text
		}
		output = append(output, message)
	}
	return output
}

func ParseClaude(reader io.Reader) (types.Context, error) {
	return parseClaude(reader)
}

func parseClaude(reader io.Reader) (types.Context, error) {
	var output types.Context
	err := parseJSONLines(reader, func(line int, value map[string]any) {
		if cwd := stringValue(value["cwd"]); cwd != "" {
			output.CWD = cwd
		}
		if id := firstString(value["sessionId"], value["session_id"]); id != "" {
			output.SessionID = id
		}
		entryType := stringValue(value["type"])
		if entryType == "system" && stringValue(value["subtype"]) == "compact_boundary" {
			output.NativeCompactFound = true
			return
		}
		if entryType != "user" && entryType != "assistant" || boolValue(value["isMeta"]) {
			return
		}
		message := object(value["message"])
		if boolValue(value["isCompactSummary"]) {
			output.NativeCompactFound = true
			if summary := contentText(message["content"]); summary != "" {
				output.Summary = summary
				output.NativeCompactFound = true
				output.Cursor = firstString(value["uuid"], fmt.Sprintf("line:%d", line))
			}
			return
		}
		role := stringValue(message["role"])
		if role == "" {
			role = entryType
		}
		if text := contentText(message["content"]); text != "" {
			if boolValue(value["isSidechain"]) {
				text = "[Sidechain result; supporting context, not a final answer]\n" + text
			}
			output.Messages = append(output.Messages, types.Message{Role: role, Text: text, At: parseTime(value["timestamp"])})
			output.Cursor = firstString(value["uuid"], fmt.Sprintf("line:%d", line))
		}
	})
	return output, err
}

func ParsePi(reader io.Reader) (types.Context, error) {
	return parsePi(reader)
}

func parsePi(reader io.Reader) (types.Context, error) {
	var output types.Context
	err := parseJSONLines(reader, func(line int, value map[string]any) {
		entryType := stringValue(value["type"])
		if entryType == "session" || entryType == "session_meta" {
			output.CWD = firstString(value["cwd"], object(value["payload"])["cwd"])
			output.SessionID = firstString(value["id"], value["sessionId"], object(value["payload"])["id"])
		}
		if entryType == "compaction" {
			output.NativeCompactFound = true
			summary := firstString(value["summary"], object(value["payload"])["summary"])
			if summary == "" {
				return
			}
			output.Summary = summary
			output.NativeCompactFound = true
			return
		}
		message := object(value["message"])
		role := stringValue(message["role"])
		if role != "user" && role != "assistant" {
			return
		}
		if text := contentText(message["content"]); text != "" {
			parsed := types.Message{Role: role, Text: text, At: parseTime(value["timestamp"])}
			output.Messages = append(output.Messages, parsed)
			output.Cursor = fmt.Sprintf("line:%d", line)
		}
	})
	return output, err
}

func codexCompactSummary(payload map[string]any) string {
	if summary := firstString(payload["message"], payload["summary"]); summary != "" {
		return summary
	}
	history, _ := payload["replacement_history"].([]any)
	for index := len(history) - 1; index >= 0; index-- {
		entry := object(history[index])
		if stringValue(entry["type"]) != "compaction" {
			continue
		}
		if summary := firstString(entry["summary"], entry["content"], object(entry["payload"])["summary"]); summary != "" {
			return summary
		}
	}
	return ""
}

func parseJSONLines(reader io.Reader, visit func(int, map[string]any)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	line := 0
	for scanner.Scan() {
		line++
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var value map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			return fmt.Errorf("invalid JSONL at line %d: %w", line, err)
		}
		visit(line, value)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read JSONL at line %d: %w", line+1, err)
	}
	return nil
}

func mergeCodexChildFinals(primary *types.Context, candidates []candidate, cwd string) error {
	if primary == nil || len(primary.ChildSessions) == 0 {
		return nil
	}
	existing := make(map[string]bool, len(primary.Messages))
	for _, message := range primary.Messages {
		existing[strings.TrimSpace(message.Text)] = true
	}
	for _, ref := range primary.ChildSessions {
		var childCandidate *candidate
		for index := range candidates {
			item := candidates[index]
			if item.kind == "codex" && strings.Contains(filepath.Base(item.path), ref.ID) {
				copy := item
				childCandidate = &copy
				break
			}
		}
		if childCandidate == nil {
			continue
		}
		child, err := loadCandidate(*childCandidate)
		if err != nil {
			return fmt.Errorf("parse child Agent session %s: %w", childCandidate.path, err)
		}
		if child.SessionID != ref.ID || child.ParentSessionID != primary.SessionID || !sameWorkspace(child.CWD, cwd) {
			continue
		}
		for _, message := range childFinalMessages(child.Messages) {
			text := strings.TrimSpace(message.Text)
			if text == "" || existing[text] {
				continue
			}
			label := firstString(ref.AgentPath, child.AgentPath, ref.ID)
			message.Text = fmt.Sprintf("[Sub-agent final result: %s]\n%s", label, text)
			message.Phase = "final_answer"
			message.Owned = true
			message.Completed = true
			primary.Messages = append(primary.Messages, message)
			existing[text] = true
		}
	}
	return nil
}

func childFinalMessages(messages []types.Message) []types.Message {
	var finals []types.Message
	for _, message := range messages {
		if message.Role == "assistant" && message.Owned && message.Phase == "final_answer" {
			finals = append(finals, message)
		}
	}
	if len(finals) > 0 {
		return finals
	}
	lastByTurn := map[string]types.Message{}
	var order []string
	for _, message := range messages {
		if message.Role != "assistant" || !message.Owned || !message.Completed {
			continue
		}
		if _, found := lastByTurn[message.TurnID]; !found {
			order = append(order, message.TurnID)
		}
		lastByTurn[message.TurnID] = message
	}
	for _, turnID := range order {
		finals = append(finals, lastByTurn[turnID])
	}
	return finals
}

func inspectRepository(cwd string) types.Repository {
	root := commandOutput(cwd, "git", "rev-parse", "--show-toplevel")
	if root == "" {
		return types.Repository{}
	}
	repo := types.Repository{
		Root:   root,
		Branch: commandOutput(root, "git", "branch", "--show-current"),
		Commit: commandOutput(root, "git", "rev-parse", "--short=12", "HEAD"),
	}
	status := strings.TrimRight(commandOutputRaw(root, "git", "status", "--porcelain=v1", "--untracked-files=normal"), "\r\n")
	for _, line := range strings.Split(status, "\n") {
		if len(line) < 4 {
			continue
		}
		path := strings.TrimSpace(line[3:])
		if arrow := strings.LastIndex(path, " -> "); arrow >= 0 {
			path = path[arrow+4:]
		}
		if path != "" {
			repo.ChangedFiles = append(repo.ChangedFiles, path)
		}
		if len(repo.ChangedFiles) == 100 {
			break
		}
	}
	return repo
}

func commandOutput(cwd, name string, args ...string) string {
	return strings.TrimSpace(commandOutputRaw(cwd, name, args...))
}

func commandOutputRaw(cwd, name string, args ...string) string {
	command := exec.Command(name, args...)
	command.Dir = cwd
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = io.Discard
	if command.Run() != nil {
		return ""
	}
	return stdout.String()
}

func contentText(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	parts, ok := value.([]any)
	if !ok {
		return ""
	}
	var texts []string
	for _, raw := range parts {
		part := object(raw)
		partType := stringValue(part["type"])
		if partType != "text" && partType != "input_text" && partType != "output_text" {
			continue
		}
		if text := stringValue(part["text"]); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.TrimSpace(strings.Join(texts, "\n"))
}

func sameWorkspace(sessionCWD, currentCWD string) bool {
	if sessionCWD == "" {
		return false
	}
	a, errA := filepath.Abs(sessionCWD)
	b, errB := filepath.Abs(currentCWD)
	if errA != nil || errB != nil {
		return filepath.Clean(sessionCWD) == filepath.Clean(currentCWD)
	}
	return within(a, b) || within(b, a)
}

func within(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func parseTime(value any) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, stringValue(value))
	return parsed
}

func infoTime(info os.FileInfo) time.Time {
	if info == nil {
		return time.Time{}
	}
	return info.ModTime().UTC()
}

func object(value any) map[string]any {
	result, _ := value.(map[string]any)
	return result
}

func stringValue(value any) string {
	result, _ := value.(string)
	return strings.TrimSpace(result)
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}

func intValue(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case json.Number:
		result, _ := typed.Int64()
		return int(result)
	default:
		return 0
	}
}

func firstString(values ...any) string {
	for _, value := range values {
		if result := stringValue(value); result != "" {
			return result
		}
	}
	return ""
}

func nestedString(value map[string]any, keys ...string) string {
	var current any = value
	for _, key := range keys {
		current = object(current)[key]
	}
	return stringValue(current)
}

func eventOwned(eventAt, createdAt time.Time) bool {
	return createdAt.IsZero() || eventAt.IsZero() || !eventAt.Before(createdAt)
}
