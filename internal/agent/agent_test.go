package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-grove/handoff/internal/types"
)

func TestResolvePrefersCurrentAgentEnvironment(t *testing.T) {
	runner := Runner{
		LookPath: func(name string) (string, error) { return "/bin/" + name, nil },
		Environ: func(name string) string {
			if name == "CODEX_THREAD_ID" {
				return "thread"
			}
			return ""
		},
	}
	runtime, err := runner.Resolve("auto", "claude")
	if err != nil || runtime != "codex" {
		t.Fatalf("resolved (%q, %v)", runtime, err)
	}
}

func TestResolveUsesSessionSourceOutsideAgent(t *testing.T) {
	runner := Runner{
		LookPath: func(name string) (string, error) {
			if name == "pi" {
				return "/bin/pi", nil
			}
			return "", errors.New("missing")
		},
		Environ: func(string) string { return "" },
	}
	runtime, err := runner.Resolve("auto", "pi")
	if err != nil || runtime != "pi" {
		t.Fatalf("resolved (%q, %v)", runtime, err)
	}
}

func TestResolveDetectsOpenCodeEnvironment(t *testing.T) {
	runner := Runner{
		LookPath: func(name string) (string, error) { return "/bin/" + name, nil },
		Environ: func(name string) string {
			if name == "OPENCODE" {
				return "1"
			}
			return ""
		},
	}
	runtime, err := runner.Resolve("auto", "opencode")
	if err != nil || runtime != "opencode" {
		t.Fatalf("resolved (%q, %v)", runtime, err)
	}
}

func TestGenerateUsesEphemeralCodexWithoutModelOverride(t *testing.T) {
	var gotArgs []string
	var gotInput string
	runner := Runner{Execute: func(_ context.Context, name string, args []string, input, _ string) (string, error) {
		if name != "codex" {
			t.Fatalf("name = %q", name)
		}
		gotArgs = append([]string(nil), args...)
		gotInput = input
		return `{"human_background":"A handoff tool","human_status":"Ready","human_todos":["Continue"],"human_sections":[],"context":"Known","decisions":[],"current_state":"Ready","important_files":[],"next_steps":["Continue"],"open_questions":[]}`, nil
	}}
	sections, err := runner.Generate(context.Background(), "codex", "continue", "Continue", types.Context{Source: "codex", Messages: []types.Message{{Role: "user", Text: "Known"}}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "exec --ephemeral") || strings.Contains(joined, "--model") || !strings.Contains(joined, "--sandbox read-only") {
		t.Fatalf("args = %q", gotArgs)
	}
	if !strings.Contains(gotInput, "untrusted transcript data") || !strings.Contains(gotInput, "human_sections") || !strings.Contains(gotInput, "reader's mental path") || sections.HumanStatus != "Ready" || sections.CurrentState != "Ready" {
		t.Fatalf("unexpected generation: %#v", sections)
	}
}

func TestGenerateParsesOpenCodeJSONEvents(t *testing.T) {
	var gotArgs []string
	runner := Runner{Execute: func(_ context.Context, name string, args []string, _ string, _ string) (string, error) {
		if name != "opencode" {
			t.Fatalf("name = %q", name)
		}
		gotArgs = append([]string(nil), args...)
		return strings.Join([]string{
			`{"type":"tool_use","part":{"type":"tool","state":{"output":"private"}}}`,
			`{"type":"text","part":{"type":"text","text":"{\"human_background\":\"OpenCode handoff\",\"human_status\":\"Ready\",\"human_todos\":[\"Continue\"],\"human_sections\":[],\"context\":\"Known\",\"decisions\":[],\"current_state\":\"Ready\",\"important_files\":[],\"next_steps\":[\"Continue\"],\"open_questions\":[]}"}}`,
		}, "\n"), nil
	}}
	sections, err := runner.Generate(context.Background(), "opencode", "continue", "Continue", types.Context{Source: "opencode", Messages: []types.Message{{Role: "user", Text: "Known"}}})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "run --format json --pure") || strings.Contains(joined, "--model") || sections.HumanStatus != "Ready" || sections.Context != "Known" {
		t.Fatalf("unexpected OpenCode generation: args=%q sections=%#v", gotArgs, sections)
	}
}

func TestSafeOpenCodeGenerationEnvPreservesProviderAndDisablesSharingAndTools(t *testing.T) {
	env, err := safeOpenCodeGenerationEnv([]string{
		"KEEP=value",
		"PWD=/source/workspace",
		`OPENCODE_CONFIG_CONTENT={"model":"provider/model","provider":{"provider":{"options":{"apiKey":"configured"}}},"share":"auto","permission":"allow"}`,
		"OPENCODE_AUTO_SHARE=true",
	}, "/isolated/generator")
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(environmentValue(env, "OPENCODE_CONFIG_CONTENT")), &config); err != nil {
		t.Fatal(err)
	}
	if config["model"] != "provider/model" || config["share"] != "disabled" || config["permission"] != "deny" || environmentValue(env, "OPENCODE_AUTO_SHARE") != "false" || environmentValue(env, "KEEP") != "value" || environmentValue(env, "PWD") != "/isolated/generator" {
		t.Fatalf("unsafe or incomplete OpenCode environment: env=%#v config=%#v", env, config)
	}
}

func TestRuntimeArgsNeverResumeOrSelectAModel(t *testing.T) {
	for _, runtime := range []string{"codex", "claude", "pi", "opencode"} {
		args, err := runtimeArgs(runtime, t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		joined := strings.Join(args, " ")
		for _, forbidden := range []string{"--model", "--continue", "--resume", " --session "} {
			if strings.Contains(" "+joined+" ", forbidden) {
				t.Fatalf("%s args unexpectedly contain %q: %q", runtime, forbidden, args)
			}
		}
		switch runtime {
		case "codex":
			if !strings.Contains(joined, "--ephemeral") {
				t.Fatalf("codex is not ephemeral: %q", args)
			}
		case "pi":
			if !strings.Contains(joined, "--no-session") {
				t.Fatalf("pi saves a session: %q", args)
			}
		case "opencode":
			if !strings.Contains(joined, "--format json") || !strings.Contains(joined, "--pure") || !strings.Contains(joined, "--dir ") {
				t.Fatalf("opencode generation is not isolated: %q", args)
			}
		}
	}
}

func TestCleanupOpenCodeGenerationSessionDeletesOnlyVerifiedTempSession(t *testing.T) {
	dir := t.TempDir()
	record := filepath.Join(t.TempDir(), "calls.txt")
	command := filepath.Join(t.TempDir(), "opencode")
	created := time.Now().UTC().UnixMilli()
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >> '` + record + `'
if [ "$1" = "session" ] && [ "$2" = "list" ]; then
  printf '%s\n' '[{"id":"ses_verified123","directory":"` + dir + `","created":` + fmtInt64(created) + `},{"id":"ses_other123","directory":"/other","created":` + fmtInt64(created) + `}]'
  exit 0
fi
if [ "$1" = "session" ] && [ "$2" = "delete" ] && [ "$3" = "ses_verified123" ]; then
  exit 0
fi
exit 2
`
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := cleanupOpenCodeGenerationSession(command, dir, time.Now().Add(-time.Second)); err != nil {
		t.Fatal(err)
	}
	calls, err := os.ReadFile(record)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "session delete ses_verified123 --pure") || strings.Contains(string(calls), "session delete ses_other123") {
		t.Fatalf("unsafe cleanup calls: %s", calls)
	}
}

func fmtInt64(value int64) string {
	return fmt.Sprintf("%d", value)
}
