package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/open-grove/handoff/internal/card"
	"github.com/open-grove/handoff/internal/types"
)

var runtimes = []string{"codex", "claude", "pi"}

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
			return "", fmt.Errorf("unknown Agent runtime %q (use auto, codex, claude, or pi)", requested)
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
	}
	if isRuntime(sourceKind) {
		return runner.require(sourceKind)
	}
	for _, runtime := range runtimes {
		if _, err := runner.lookPath()(runtime); err == nil {
			return runtime, nil
		}
	}
	return "", errors.New("no supported Agent CLI found (install Codex, Claude Code, or Pi, or use --generator deterministic)")
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
	default:
		return nil, fmt.Errorf("unsupported Agent runtime %q", runtime)
	}
}

func defaultExecute(ctx context.Context, name string, args []string, input, dir string) (string, error) {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = dir
	command.Stdin = strings.NewReader(input)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(card.Redact(stderr.String()))
		if len(detail) > 800 {
			detail = detail[len(detail)-800:]
		}
		if detail != "" {
			return "", fmt.Errorf("%w: %s", err, detail)
		}
		return "", err
	}
	return stdout.String(), nil
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
