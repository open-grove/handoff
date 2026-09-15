package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/open-grove/handoff/internal/card"
	"github.com/open-grove/handoff/internal/types"
)

func TestCreatePreparedMaterialPublishesWithoutStartingAnotherAgent(t *testing.T) {
	for _, test := range []struct {
		name, generator, intent, input string
		attach, review                 bool
	}{
		{"default-share-file", "", "share", "file", false, false},
		{"default-continue-stdin", "", "continue", "stdin", true, false},
		{"explicit-current-continue", "current-session", "continue", "file", false, false},
		{"legacy-preserve-continue", "preserve", "continue", "file", false, false},
		{"default-auto", "", "auto", "stdin", false, false},
		{"review-share", "", "share", "stdin", false, true},
		{"review-continue", "", "continue", "file", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("HANDOFF_CONFIG", filepath.Join(t.TempDir(), "config.json"))
			t.Setenv("HANDOFF_CALLER_RUNTIME", "codex")
			fakeBin := t.TempDir()
			called := filepath.Join(fakeBin, "called")
			if err := os.WriteFile(filepath.Join(fakeBin, "codex"), []byte("#!/bin/sh\ntouch \"$HANDOFF_TEST_CALLED\"\nexit 2\n"), 0o700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HANDOFF_TEST_CALLED", called)
			t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
			requests := make(chan types.PublishRequest, 1)
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/handoffs" {
					http.Error(w, "unexpected request", http.StatusNotFound)
					return
				}
				var input types.PublishRequest
				if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				requests <- input
				artifact, err := card.BuildFromSections("abcdefghijklmnopqrstuv", input.Goal, input.Source, input.Sections, input.Generator, time.Now())
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(types.CreateResponse{Handoff: artifact})
			}))
			defer api.Close()
			t.Setenv("HANDOFF_SERVER", api.URL)
			body := "## Background\n\nFix the release failure.\n\n## Current State\n\nThe cause is verified.\n\n## Next Steps\n\n- Apply the compatible reader.\n\n## Evidence\n\n" + strings.Repeat("Keep this evidence.\n", 150) + "\n```sh\nprintf '%s' exact\n```\n\nhttps://example.com/issue\nSHA-256: `0123456789abcdef`\napi_key=super-secret-value"
			inputPath := filepath.Join(t.TempDir(), "prepared.md")
			if err := os.WriteFile(inputPath, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			args := []string{"Release failure", "--intent", test.intent, "--no-git"}
			if test.generator != "" {
				args = append(args, "--generator", test.generator)
			}
			if test.review {
				t.Setenv("VISUAL", "true")
				args = append(args, "--review")
			}
			if test.attach {
				args = append(args, "--attach-context")
			}
			if test.input == "file" {
				args = append(args, "--file", inputPath)
			} else {
				stdin, err := os.Open(inputPath)
				if err != nil {
					t.Fatal(err)
				}
				old := os.Stdin
				os.Stdin = stdin
				t.Cleanup(func() { os.Stdin = old; stdin.Close() })
			}
			stdout, err := os.CreateTemp(t.TempDir(), "stdout")
			if err != nil {
				t.Fatal(err)
			}
			old := os.Stdout
			os.Stdout = stdout
			t.Cleanup(func() { os.Stdout = old; stdout.Close() })
			if err := runCreate("", "json", args); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(called); !os.IsNotExist(err) {
				t.Fatalf("current-session started a second Agent: %v", err)
			}
			input := <-requests
			wantIntent := test.intent
			if wantIntent == "auto" {
				wantIntent = "share"
			}
			if input.Sections.Intent != wantIntent || input.Generator != "preserve" || input.Source.Kind != test.input {
				t.Fatalf("unexpected published metadata: %#v", input)
			}
			if (input.ContextAttachment != nil) != test.attach {
				t.Fatal("attachment opt-in was not respected")
			}
			preserved := input.Sections.Context
			if wantIntent == "share" {
				preserved = input.Sections.HumanSections[0].Body
			}
			if preserved != card.Redact(body) {
				t.Fatalf("prepared Markdown changed or was truncated: %q", preserved)
			}
			if wantIntent == "continue" && (input.Sections.CurrentState != "The cause is verified." || strings.Join(input.Sections.NextSteps, "\n") != "Apply the compatible reader.") {
				t.Fatalf("continue state/actions were not retained: %#v", input.Sections)
			}
			if _, err := stdout.Seek(0, io.SeekStart); err != nil {
				t.Fatal(err)
			}
			var output map[string]any
			if err := json.NewDecoder(stdout).Decode(&output); err != nil {
				t.Fatal(err)
			}
			if output["fallback_used"] == true {
				t.Fatal("prepared content incorrectly reported as fallback")
			}
		})
	}
}

func TestCurrentSessionWithoutPreparedInputDoesNotDiscoverAgentSessions(t *testing.T) {
	stdin, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = stdin
	t.Cleanup(func() { os.Stdin = old; stdin.Close() })
	for _, extra := range [][]string{nil, {"--source", "codex"}, {"--generator", "preserve"}} {
		err := runCreate("", "json", append([]string{"Prepare here", "--dry-run"}, extra...))
		if err == nil || !strings.Contains(err.Error(), "current-session requires Markdown prepared in the current conversation") || !strings.Contains(err.Error(), "--generator new-session") {
			t.Fatalf("missing prepared input did not stop before discovery: %v", err)
		}
	}
}

func TestGeneratorCompatibilityAliases(t *testing.T) {
	for old, canonical := range map[string]string{"preserve": "current-session", "agent": "new-session"} {
		selection, err := resolveCreateSelection(createSelectionInput{Source: "auto", Generator: old, Runtime: "auto", Set: map[string]bool{"generator": true}})
		if err != nil || selection.Generator != canonical || len(selection.Deprecated) != 1 {
			t.Fatalf("%s alias: %#v, %v", old, selection, err)
		}
	}
	_, err := resolveCreateSelection(createSelectionInput{Source: "auto", Generator: "current-session", Runtime: "auto", LegacyAgent: "codex", Set: map[string]bool{"agent": true}})
	if err == nil || !strings.Contains(err.Error(), "only selects the local sidecar") {
		t.Fatalf("legacy runtime bypassed current-session validation: %v", err)
	}
}
