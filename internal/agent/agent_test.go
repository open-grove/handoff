package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

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

func TestRuntimeArgsNeverResumeOrSelectAModel(t *testing.T) {
	for _, runtime := range []string{"codex", "claude", "pi"} {
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
		}
	}
}
