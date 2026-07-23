package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/open-grove/handoff/internal/types"
)

type previewCompactor struct{}

func (previewCompactor) Compact(_ context.Context, _ string, _ types.Context) (types.Sections, error) {
	return types.Sections{Context: "review me", CurrentState: "ready", NextSteps: []string{"continue"}}, nil
}

func (previewCompactor) Generator() string { return "server:test" }

func TestCreateReceiveDeleteRoundTrip(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	api := &API{Store: store, Token: "create-token", PublicURL: "https://handoff.example", DefaultTTL: time.Hour}
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	input := types.PublishRequest{
		Goal:      "continue implementation",
		Source:    types.SourceRef{Kind: "codex", SessionID: "session-1"},
		Generator: "agent:codex",
		Sections: types.Sections{
			Context: "api_key=super-secret-value\nparser is complete", CurrentState: "Ready",
			NextSteps: []string{"Continue"},
		},
	}
	body, _ := json.Marshal(input)
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/handoffs", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("create status = %d: %s", response.StatusCode, data)
	}
	var created types.CreateResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if len(created.Handoff.ID) != 22 || !strings.Contains(created.ShareURL, created.Handoff.ID) || created.MarkdownURL != created.ShareURL+".md" || created.DeleteToken == "" {
		t.Fatalf("unexpected create response: %#v", created)
	}
	if strings.Contains(created.Handoff.Markdown, "super-secret-value") {
		t.Fatal("secret leaked into handoff")
	}

	stored, err := os.ReadFile(store.path(created.Handoff.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "super-secret-value") || strings.Contains(string(stored), created.DeleteToken) || strings.Contains(string(stored), `"messages"`) || strings.Contains(string(stored), `"sections"`) {
		t.Fatal("source request shape or secret was persisted")
	}

	getResponse, err := http.Get(server.URL + "/v1/handoffs/" + created.Handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	if getResponse.StatusCode != http.StatusOK {
		getResponse.Body.Close()
		t.Fatalf("get status = %d", getResponse.StatusCode)
	}
	var fetched types.CreateResponse
	if err := json.NewDecoder(getResponse.Body).Decode(&fetched); err != nil {
		getResponse.Body.Close()
		t.Fatal(err)
	}
	getResponse.Body.Close()
	if fetched.DeleteToken != "" {
		t.Fatal("GET response leaked the delete credential")
	}

	pageResponse, err := http.Get(server.URL + "/h/" + created.Handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	pageBody, _ := io.ReadAll(pageResponse.Body)
	pageResponse.Body.Close()
	if pageResponse.StatusCode != http.StatusOK || !strings.HasPrefix(pageResponse.Header.Get("Content-Type"), "text/html") || !strings.Contains(string(pageBody), "<h1>continue implementation</h1>") || !strings.Contains(string(pageBody), "FOR HUMAN") || !strings.Contains(string(pageBody), "FOR AGENT") {
		t.Fatalf("unexpected human page: status=%d type=%q body=%s", pageResponse.StatusCode, pageResponse.Header.Get("Content-Type"), pageBody)
	}

	markdownResponse, err := http.Get(server.URL + "/h/" + created.Handoff.ID + ".md")
	if err != nil {
		t.Fatal(err)
	}
	markdownBody, _ := io.ReadAll(markdownResponse.Body)
	markdownResponse.Body.Close()
	if markdownResponse.StatusCode != http.StatusOK || !strings.HasPrefix(markdownResponse.Header.Get("Content-Type"), "text/markdown") || string(markdownBody) != created.Handoff.Markdown {
		t.Fatalf("unexpected Markdown response: status=%d type=%q", markdownResponse.StatusCode, markdownResponse.Header.Get("Content-Type"))
	}

	rejectedRequest, _ := http.NewRequest(http.MethodDelete, server.URL+"/v1/handoffs/"+created.Handoff.ID, nil)
	rejectedRequest.Header.Set("X-Handoff-Delete-Token", "wrong-token")
	rejectedResponse, err := http.DefaultClient.Do(rejectedRequest)
	if err != nil {
		t.Fatal(err)
	}
	rejectedResponse.Body.Close()
	if rejectedResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong owner credential status = %d", rejectedResponse.StatusCode)
	}

	deleteRequest, _ := http.NewRequest(http.MethodDelete, server.URL+"/v1/handoffs/"+created.Handoff.ID, nil)
	deleteRequest.Header.Set("X-Handoff-Delete-Token", created.DeleteToken)
	deleteResponse, err := http.DefaultClient.Do(deleteRequest)
	if err != nil {
		t.Fatal(err)
	}
	deleteResponse.Body.Close()
	if deleteResponse.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d", deleteResponse.StatusCode)
	}
	if _, err := store.Get(created.Handoff.ID); !os.IsNotExist(err) {
		t.Fatalf("deleted handoff still exists: %v", err)
	}
}

func TestStoreExpiresHandoff(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handoff := types.Handoff{
		Version:   types.ProtocolVersion,
		ID:        "abcdefghijklmnopqrstuv",
		CreatedAt: time.Now().Add(-time.Hour),
		ExpiresAt: time.Now().Add(-time.Minute),
	}
	if err := store.Save(handoff); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(handoff.ID); !os.IsNotExist(err) {
		t.Fatalf("expired handoff returned: %v", err)
	}
}

func TestDefaultPublishEndpointRejectsRawContext(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&API{Store: store, DefaultTTL: time.Hour}).Handler())
	defer server.Close()

	body := `{"goal":"continue","context":{"source":"stdin","messages":[{"role":"user","text":"raw transcript"}]}}`
	response, err := http.Post(server.URL+"/v1/handoffs", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("raw context status = %d", response.StatusCode)
	}
}

func TestCompactPreviewDoesNotStoreHandoff(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&API{
		Store: store, Compactor: previewCompactor{}, DefaultTTL: time.Hour,
		VerifyOpenGroveUser: func(_ context.Context, token string) (bool, error) {
			return token == "opengrove-access", nil
		},
	}).Handler())
	defer server.Close()
	body := `{"goal":"continue","context":{"source":"stdin","messages":[{"role":"user","text":"raw transcript"}]}}`
	unauthorized, err := http.Post(server.URL+"/v1/handoffs/compact-preview", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized preview status = %d", unauthorized.StatusCode)
	}
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/handoffs/compact-preview", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer opengrove-access")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("preview status = %d: %s", response.StatusCode, data)
	}
	var preview types.CompactPreviewResponse
	if err := json.NewDecoder(response.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	if preview.Generator != "server:test" || preview.Sections.Context != "review me" {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	entries, err := os.ReadDir(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("preview persisted %d files", len(entries))
	}
}
