package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
