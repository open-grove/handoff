package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/open-grove/handoff/internal/card"
	"github.com/open-grove/handoff/internal/types"
)

var runtimes = []string{"codex", "claude", "pi", "opencode"}

type Runner struct {
	LookPath func(string) (string, error)
	Environ  func(string) string
	Execute  func(context.Context, string, []string, string, string) (string, error)
}

// Resolve chooses the Agent hosting this invocation when its environment is
// visible. Session source is the next-best signal, followed by an installed
// runtime. requested may explicitly select a runtime but never a model.
func (runner Runner) Resolve(requested, sourceKind string) (string, error) {
	requested = strings.ToLower(strings.TrimSpace(requested))
	if requested != "" && requested != "auto" {
		if !isRuntime(requested) {
			return "", fmt.Errorf("unknown Agent runtime %q (use auto, codex, claude, pi, or opencode)", requested)
		}
		if _, err := runner.lookPath()(requested); err != nil {
			return "", fmt.Errorf("%s CLI was not found", requested)
		}
		return requested, nil
	}

	env := runner.environ()
	switch {
	case env("CODEX_THREAD_ID") != "":
		return runner.require("codex")
	case env("CLAUDECODE") != "" || env("CLAUDE_CODE_ENTRYPOINT") != "" || env("CLAUDE_CODE_SESSION_ID") != "":
		return runner.require("claude")
	case env("PI_CODING_AGENT_SESSION") != "" || env("PI_SESSION_ID") != "":
		return runner.require("pi")
	case env("OPENCODE") != "":
		return runner.require("opencode")
	}
	if isRuntime(sourceKind) {
		return runner.require(sourceKind)
	}
	for _, runtime := range runtimes {
		if _, err := runner.lookPath()(runtime); err == nil {
			return runtime, nil
		}
	}
	return "", errors.New("no supported Agent CLI found (install Codex, Claude Code, Pi, or OpenCode, or use --generator deterministic)")
}

// Generate starts a fresh, ephemeral Agent invocation. It never resumes or
// mutates the source session and it does not select a model, so the runtime's
// existing auth, provider, and default model remain in effect.
func (runner Runner) Generate(ctx context.Context, runtime, intent, goal string, source types.Context) (types.Sections, error) {
	if !isRuntime(runtime) {
		return types.Sections{}, fmt.Errorf("unsupported Agent runtime %q", runtime)
	}
	prompt, err := card.GenerationPrompt(intent, goal, source)
	if err != nil {
		return types.Sections{}, err
	}

	tempDir, err := os.MkdirTemp("", "handoff-agent-*")
	if err != nil {
		return types.Sections{}, err
	}
	defer os.RemoveAll(tempDir)

	args, err := runtimeArgs(runtime, tempDir)
	if err != nil {
		return types.Sections{}, err
	}
	output, err := runner.execute()(ctx, runtime, args, prompt, tempDir)
	if err != nil {
		return types.Sections{}, fmt.Errorf("%s handoff generation failed: %w", runtime, err)
	}
	if runtime == "opencode" {
		output, err = openCodeRunText(output)
		if err != nil {
			return types.Sections{}, fmt.Errorf("opencode handoff generation failed: %w", err)
		}
	}
	return card.ParseSections(output, intent)
}

func runtimeArgs(runtime, tempDir string) ([]string, error) {
	switch runtime {
	case "codex":
		schemaPath := filepath.Join(tempDir, "handoff.schema.json")
		schema := []byte(`{"type":"object","additionalProperties":false,"required":["intent","human_background","human_status","human_todos","human_sections","context","decisions","current_state","important_files","next_steps","open_questions"],"properties":{"intent":{"type":"string","enum":["share","continue"]},"human_background":{"type":"string"},"human_status":{"type":"string"},"human_todos":{"type":"array","items":{"type":"string"}},"human_sections":{"type":"array","items":{"type":"object","additionalProperties":false,"required":["title","body"],"properties":{"title":{"type":"string"},"body":{"type":"string"}}}},"context":{"type":"string"},"decisions":{"type":"array","items":{"type":"string"}},"current_state":{"type":"string"},"important_files":{"type":"array","items":{"type":"string"}},"next_steps":{"type":"array","items":{"type":"string"}},"open_questions":{"type":"array","items":{"type":"string"}}}}`)
		if err := os.WriteFile(schemaPath, schema, 0o600); err != nil {
			return nil, err
		}
		return []string{"exec", "--ephemeral", "--sandbox", "read-only", "--skip-git-repo-check", "--output-schema", schemaPath, "-"}, nil
	case "claude":
		return []string{"--print", "--safe-mode", "--disable-slash-commands", "--tools", "", "--permission-mode", "dontAsk", "--output-format", "text"}, nil
	case "pi":
		return []string{"--print", "--no-session", "--no-tools", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-context-files"}, nil
	case "opencode":
		return []string{"run", "--format", "json", "--pure", "--title", "OpenGrove Handoff generator"}, nil
	default:
		return nil, fmt.Errorf("unsupported Agent runtime %q", runtime)
	}
}

func defaultExecute(ctx context.Context, name string, args []string, input, dir string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Stdin = strings.NewReader(input)
	if filepath.Base(name) == "opencode" {
		env, err := safeOpenCodeGenerationEnv(os.Environ())
		if err != nil {
			return "", err
		}
		command.Env = env
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	started := time.Now().UTC()
	runErr := command.Run()
	var cleanupErr error
	if filepath.Base(name) == "opencode" {
		cleanupErr = cleanupOpenCodeGenerationSession(name, dir, started)
	}
	if runErr != nil || cleanupErr != nil {
		detail := strings.TrimSpace(card.Redact(stderr.String()))
		if len(detail) > 800 {
			detail = detail[len(detail)-800:]
		}
		var failures []error
		if runErr != nil {
			if detail != "" {
				failures = append(failures, fmt.Errorf("%w: %s", runErr, detail))
			} else {
				failures = append(failures, runErr)
			}
		}
		if cleanupErr != nil {
			failures = append(failures, cleanupErr)
		}
		return "", errors.Join(failures...)
	}
	return stdout.String(), nil
}

func safeOpenCodeGenerationEnv(environ []string) ([]string, error) {
	config := map[string]any{}
	if value := strings.TrimSpace(environmentValue(environ, "OPENCODE_CONFIG_CONTENT")); value != "" {
		if err := json.Unmarshal([]byte(value), &config); err != nil {
			return nil, fmt.Errorf("parse OPENCODE_CONFIG_CONTENT for safe handoff generation: %w", err)
		}
	}
	if config == nil {
		config = map[string]any{}
	}
	config["share"] = "disabled"
	config["permission"] = "deny"
	encoded, err := json.Marshal(config)
	if err != nil {
		return nil, fmt.Errorf("build safe OpenCode generation config: %w", err)
	}
	result := setEnvironmentValue(environ, "OPENCODE_CONFIG_CONTENT", string(encoded))
	return setEnvironmentValue(result, "OPENCODE_AUTO_SHARE", "false"), nil
}

func environmentValue(environ []string, name string) string {
	for _, item := range environ {
		key, value, found := strings.Cut(item, "=")
		if found && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func setEnvironmentValue(environ []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(environ)+1)
	replaced := false
	for _, item := range environ {
		key, _, found := strings.Cut(item, "=")
		if found && strings.EqualFold(key, name) {
			if !replaced {
				result = append(result, prefix+value)
				replaced = true
			}
			continue
		}
		result = append(result, item)
	}
	if !replaced {
		result = append(result, prefix+value)
	}
	return result
}

func openCodeRunText(output string) (string, error) {
	scanner := bufio.NewScanner(strings.NewReader(output))
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	var texts []string
	line := 0
	for scanner.Scan() {
		line++
		if len(bytes.TrimSpace(scanner.Bytes())) == 0 {
			continue
		}
		var event struct {
			Type string `json:"type"`
			Part struct {
				Type      string `json:"type"`
				Text      string `json:"text"`
				Synthetic bool   `json:"synthetic"`
			} `json:"part"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return "", fmt.Errorf("invalid JSON event at line %d: %w", line, err)
		}
		if event.Type != "text" || event.Part.Type != "text" || event.Part.Synthetic {
			continue
		}
		if text := strings.TrimSpace(event.Part.Text); text != "" {
			texts = append(texts, text)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read OpenCode JSON events: %w", err)
	}
	if len(texts) == 0 {
		return "", errors.New("OpenCode returned no final text")
	}
	return strings.Join(texts, "\n"), nil
}

type openCodeGeneratedSession struct {
	ID        string `json:"id"`
	Directory string `json:"directory"`
	Created   int64  `json:"created"`
}

func cleanupOpenCodeGenerationSession(commandName, dir string, started time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	list := exec.CommandContext(ctx, commandName, "session", "list", "--format", "json", "--max-count", "20", "--pure")
	list.Dir = dir
	data, err := list.Output()
	if err != nil {
		return fmt.Errorf("identify ephemeral OpenCode session for cleanup: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return errors.New("identify ephemeral OpenCode session for cleanup: no session found")
	}
	var sessions []openCodeGeneratedSession
	if err := json.Unmarshal(data, &sessions); err != nil {
		return fmt.Errorf("identify ephemeral OpenCode session for cleanup: %w", err)
	}
	var matches []openCodeGeneratedSession
	cutoff := started.Add(-5 * time.Second).UnixMilli()
	for _, session := range sessions {
		if session.Created < cutoff || !sameDirectory(session.Directory, dir) || !validOpenCodeSessionID(session.ID) {
			continue
		}
		matches = append(matches, session)
	}
	if len(matches) != 1 {
		return fmt.Errorf("refusing OpenCode cleanup: expected exactly one verified ephemeral session, found %d", len(matches))
	}
	remove := exec.CommandContext(ctx, commandName, "session", "delete", matches[0].ID, "--pure")
	remove.Dir = dir
	if output, err := remove.CombinedOutput(); err != nil {
		detail := strings.TrimSpace(card.Redact(string(output)))
		if detail != "" {
			return fmt.Errorf("delete verified ephemeral OpenCode session: %w: %s", err, detail)
		}
		return fmt.Errorf("delete verified ephemeral OpenCode session: %w", err)
	}
	return nil
}

func sameDirectory(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(left)
	rightAbs, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return filepath.Clean(left) == filepath.Clean(right)
	}
	if resolved, err := filepath.EvalSymlinks(leftAbs); err == nil {
		leftAbs = resolved
	}
	if resolved, err := filepath.EvalSymlinks(rightAbs); err == nil {
		rightAbs = resolved
	}
	return filepath.Clean(leftAbs) == filepath.Clean(rightAbs)
}

func validOpenCodeSessionID(value string) bool {
	if !strings.HasPrefix(value, "ses_") || len(value) <= len("ses_") {
		return false
	}
	for _, char := range value[len("ses_"):] {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}

func (runner Runner) require(runtime string) (string, error) {
	if _, err := runner.lookPath()(runtime); err != nil {
		return "", fmt.Errorf("current Agent is %s, but its CLI was not found", runtime)
	}
	return runtime, nil
}

func (runner Runner) lookPath() func(string) (string, error) {
	if runner.LookPath != nil {
		return runner.LookPath
	}
	return exec.LookPath
}

func (runner Runner) environ() func(string) string {
	if runner.Environ != nil {
		return runner.Environ
	}
	return os.Getenv
}

func (runner Runner) execute() func(context.Context, string, []string, string, string) (string, error) {
	if runner.Execute != nil {
		return runner.Execute
	}
	return defaultExecute
}

func isRuntime(value string) bool {
	for _, runtime := range runtimes {
		if value == runtime {
			return true
		}
	}
	return false
}
