package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/open-grove/handoff/internal/types"
)

func TestValidateServerRequiresHTTPSOutsideLoopback(t *testing.T) {
	for _, value := range []string{"https://handoff.example", "http://127.0.0.1:7391", "http://localhost:7391", "http://[::1]:7391"} {
		if err := validateServer(value); err != nil {
			t.Fatalf("validateServer(%q): %v", value, err)
		}
	}
	for _, value := range []string{"http://handoff.example", "handoff.example", ""} {
		if err := validateServer(value); err == nil {
			t.Fatalf("validateServer(%q) accepted insecure value", value)
		}
	}
}

func TestDeleteUsesPerHandoffCredentialHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodDelete || request.Header.Get("X-Handoff-Delete-Token") != "owner-secret" {
			t.Fatalf("unexpected delete request: %s %q", request.Method, request.Header.Get("X-Handoff-Delete-Token"))
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	err := (Client{Server: server.URL, DeleteToken: "owner-secret", HTTP: server.Client()}).Delete(context.Background(), "abcdefghijklmnopqrstuv")
	if err != nil {
		t.Fatal(err)
	}
}

func TestGetContextUsesSeparateEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/handoffs/abcdefghijklmnopqrstuv/context" {
			t.Fatalf("unexpected context request: %s %s", request.Method, request.URL.Path)
		}
		_ = json.NewEncoder(response).Encode(types.ContextResponse{
			HandoffID: "abcdefghijklmnopqrstuv",
			Context: types.ContextAttachment{
				Version:   types.ContextAttachmentVersion,
				Source:    types.SourceRef{Kind: "codex"},
				Messages:  []types.Message{{Role: "user", Text: "known"}},
				Redaction: types.RedactionVersion,
			},
		})
	}))
	defer server.Close()
	result, err := (Client{Server: server.URL, HTTP: server.Client()}).GetContext(context.Background(), "abcdefghijklmnopqrstuv")
	if err != nil {
		t.Fatal(err)
	}
	if result.HandoffID != "abcdefghijklmnopqrstuv" || len(result.Context.Messages) != 1 {
		t.Fatalf("unexpected Context response: %#v", result)
	}
}

func TestPreviewServerCompactionConsumesEventStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !strings.Contains(request.Header.Get("Accept"), "text/event-stream") {
			t.Fatalf("Accept = %q", request.Header.Get("Accept"))
		}
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = response.Write([]byte("event: start\ndata: {\"request_id\":\"request-1\"}\n\n"))
		_, _ = response.Write([]byte("event: delta\ndata: {\"text\":\"partial\"}\n\n"))
		result, _ := json.Marshal(types.CompactPreviewResponse{
			Generator: "server:agent-plan",
			Sections: types.Sections{
				HumanBackground: "Background",
				HumanStatus:     "Ready",
				HumanTodos:      []string{"Continue"},
				Context:         "Known",
				CurrentState:    "Ready",
				NextSteps:       []string{"Continue"},
			},
		})
		_, _ = response.Write([]byte("event: result\ndata: " + string(result) + "\n\n"))
	}))
	defer server.Close()

	preview, err := (Client{Server: server.URL, HTTP: server.Client()}).PreviewServerCompaction(
		context.Background(),
		types.CompactRequest{Goal: "Continue", Context: types.Context{Source: "stdin", Messages: []types.Message{{Role: "user", Text: "Ready"}}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if preview.Generator != "server:agent-plan" || preview.Sections.Context != "Known" {
		t.Fatalf("preview = %#v", preview)
	}
}

func TestPreviewServerCompactionReportsStreamError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "text/event-stream")
		_, _ = response.Write([]byte("event: error\ndata: {\"error\":\"upstream reset\"}\n\n"))
	}))
	defer server.Close()

	_, err := (Client{Server: server.URL, HTTP: server.Client()}).PreviewServerCompaction(
		context.Background(),
		types.CompactRequest{Goal: "Continue"},
	)
	if err == nil || !strings.Contains(err.Error(), "upstream reset") {
		t.Fatalf("error = %v", err)
	}
}
