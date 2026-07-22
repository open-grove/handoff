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
	Kind      string
	Files     []string
	ReadStdin bool
	Stdin     io.Reader
	CWD       string
	Home      string
	NoGit     bool
}

type candidate struct {
	kind    string
	path    string
	modTime time.Time
}

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
		result, err = fromAgent(options.Kind, home, cwd)
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
	data, err := io.ReadAll(io.LimitReader(reader, 4<<20))
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

func fromAgent(kind, home, cwd string) (types.Context, error) {
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
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].modTime.After(candidates[j].modTime) })
	if len(candidates) > 200 {
		candidates = candidates[:200]
	}

	for _, item := range candidates {
		file, err := os.Open(item.path)
		if err != nil {
			continue
		}
		var parsed types.Context
		switch item.kind {
		case "codex":
			parsed, err = ParseCodex(file)
		case "claude":
			parsed, err = ParseClaude(file)
		default:
			parsed, err = ParsePi(file)
		}
		file.Close()
		if err != nil || len(parsed.Messages) == 0 || !sameWorkspace(parsed.CWD, cwd) {
			continue
		}
		parsed.Source = item.kind
		parsed.UpdatedAt = item.modTime.UTC()
		return parsed, nil
	}
	return types.Context{}, errors.New("no recent Agent session matched this workspace; pipe context on stdin or pass --file")
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
	var output types.Context
	err := parseJSONLines(reader, func(line int, value map[string]any) {
		payload := object(value["payload"])
		switch stringValue(value["type"]) {
		case "session_meta":
			output.CWD = stringValue(payload["cwd"])
			output.SessionID = firstString(payload["id"], payload["session_id"])
		case "compacted":
			output.NativeCompactFound = true
			if summary := codexCompactSummary(payload); summary != "" {
				output.Summary = summary
				output.Messages = nil
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
				output.Messages = append(output.Messages, types.Message{Role: role, Text: text, At: parseTime(value["timestamp"])})
				output.Cursor = fmt.Sprintf("line:%d", line)
			}
		}
	})
	return output, err
}

func ParseClaude(reader io.Reader) (types.Context, error) {
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
		if entryType != "user" && entryType != "assistant" || boolValue(value["isSidechain"]) || boolValue(value["isMeta"]) {
			return
		}
		message := object(value["message"])
		if boolValue(value["isCompactSummary"]) {
			if summary := contentText(message["content"]); summary != "" {
				output.Summary = summary
				output.NativeCompactFound = true
				output.Messages = nil
				output.Cursor = firstString(value["uuid"], fmt.Sprintf("line:%d", line))
			}
			return
		}
		role := stringValue(message["role"])
		if role == "" {
			role = entryType
		}
		if text := contentText(message["content"]); text != "" {
			output.Messages = append(output.Messages, types.Message{Role: role, Text: text, At: parseTime(value["timestamp"])})
			output.Cursor = firstString(value["uuid"], fmt.Sprintf("line:%d", line))
		}
	})
	return output, err
}

func ParsePi(reader io.Reader) (types.Context, error) {
	var output types.Context
	type identifiedMessage struct {
		id      string
		message types.Message
	}
	var seen []identifiedMessage
	err := parseJSONLines(reader, func(line int, value map[string]any) {
		entryType := stringValue(value["type"])
		if entryType == "session" || entryType == "session_meta" {
			output.CWD = firstString(value["cwd"], object(value["payload"])["cwd"])
			output.SessionID = firstString(value["id"], value["sessionId"], object(value["payload"])["id"])
		}
		if entryType == "compaction" {
			summary := firstString(value["summary"], object(value["payload"])["summary"])
			if summary == "" {
				return
			}
			output.Summary = summary
			output.NativeCompactFound = true
			firstKeptID := firstString(value["firstKeptEntryId"], object(value["payload"])["firstKeptEntryId"])
			output.Messages = nil
			if firstKeptID != "" {
				for index, item := range seen {
					if item.id == firstKeptID {
						for _, kept := range seen[index:] {
							output.Messages = append(output.Messages, kept.message)
						}
						break
					}
				}
			}
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
			seen = append(seen, identifiedMessage{id: stringValue(value["id"]), message: parsed})
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
		var value map[string]any
		if json.Unmarshal(scanner.Bytes(), &value) == nil {
			visit(line, value)
		}
	}
	return scanner.Err()
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

func firstString(values ...any) string {
	for _, value := range values {
		if result := stringValue(value); result != "" {
			return result
		}
	}
	return ""
}
