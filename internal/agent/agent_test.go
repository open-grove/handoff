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

func TestResolveDistinguishesNoAgentFromRequestedRuntimeFailure(t *testing.T) {
	missing := func(string) (string, error) { return "", errors.New("missing") }
	runner := Runner{LookPath: missing, Environ: func(string) string { return "" }}

	_, err := runner.Resolve("auto", "file")
	if !errors.Is(err, ErrNoSupportedSidecarRuntime) {
		t.Fatalf("no-Agent discovery error is not recognizable: %v", err)
	}

	_, err = runner.Resolve("opencode", "file")
	if err == nil || errors.Is(err, ErrNoSupportedSidecarRuntime) || !strings.Contains(err.Error(), "requested opencode sidecar CLI was not found") {
		t.Fatalf("explicit OpenCode failure was treated as a generic no-Agent fallback: %v", err)
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

func TestOpenCodeErrorDetailNeverEchoesTextEvents(t *testing.T) {
	detail := openCodeErrorDetail(strings.Join([]string{
		`{"type":"text","part":{"type":"text","text":"SECRET_TRANSCRIPT_CONTENT"}}`,
		`{"type":"error","error":{"name":"APIError","data":{"message":"model unavailable","requestBody":"SECRET_REQUEST_BODY","responseBody":"{\"error\":{\"status\":\"INVALID_ARGUMENT\",\"message\":\"API key not valid\"}}"}}}`,
	}, "\n"))
	if !strings.Contains(detail, "model unavailable") || !strings.Contains(detail, "API key not valid") || strings.Contains(detail, "SECRET_TRANSCRIPT_CONTENT") || strings.Contains(detail, "SECRET_REQUEST_BODY") {
		t.Fatalf("unsafe or incomplete OpenCode error detail: %q", detail)
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

// Run this opt-in test against an installed CLI with, for example:
// HANDOFF_LIVE_AGENT_RUNTIME=codex go test ./internal/agent -run TestLiveSidecarGeneration -v -count=1
// Provider credentials and the default model come from that CLI's existing
// environment/configuration; the test never reads or prints them.
func TestLiveSidecarGeneration(t *testing.T) {
	runtime := strings.ToLower(strings.TrimSpace(os.Getenv("HANDOFF_LIVE_AGENT_RUNTIME")))
	if runtime == "" {
		t.Skip("set HANDOFF_LIVE_AGENT_RUNTIME to codex, claude, pi, or opencode")
	}
	if !isRuntime(runtime) {
		t.Fatalf("unsupported live sidecar runtime %q", runtime)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	sections, err := (Runner{}).Generate(ctx, runtime, "continue", "Continue Project Atlas", types.Context{
		Source: "file",
		Messages: []types.Message{
			{Role: "user", Text: "Prepare a handoff for Project Atlas. The parser update is complete. The remaining task is to add the release note."},
			{Role: "assistant", Text: "Confirmed: keep the parser behavior and add one release note next. No files need to be changed during handoff generation."},
		},
	})
	if err != nil {
		t.Fatalf("%s live sidecar generation failed: %v", runtime, err)
	}
	if sections.Intent != "continue" || strings.TrimSpace(sections.Context) == "" || strings.TrimSpace(sections.CurrentState) == "" || len(sections.NextSteps) == 0 {
		t.Fatalf("%s returned incomplete continue sections: intent=%q context=%t current_state=%t next_steps=%d", runtime, sections.Intent, strings.TrimSpace(sections.Context) != "", strings.TrimSpace(sections.CurrentState) != "", len(sections.NextSteps))
	}
	t.Logf("%s sidecar returned a valid continue handoff (%d next step(s))", runtime, len(sections.NextSteps))
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
