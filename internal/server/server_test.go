package server

import (
	"bytes"
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

func TestCreateReceiveDeleteRoundTrip(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	api := &API{Store: store, Token: "create-token", PublicURL: "https://handoff.example"}
	server := httptest.NewServer(api.Handler())
	defer server.Close()

	input := types.PublishRequest{
		Goal:       "continue implementation",
		Source:     types.SourceRef{Kind: "codex", SessionID: "session-1"},
		Generator:  "agent:codex",
		TTLSeconds: 1, // Legacy clients may still send this; it is ignored.
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
	createdJSON, _ := json.Marshal(created)
	if strings.Contains(string(createdJSON), "expires_at") || strings.Contains(created.Handoff.Markdown, "expires_at") {
		t.Fatalf("permanent handoff exposed legacy expiry metadata: %s", createdJSON)
	}
	if strings.Contains(created.Handoff.Markdown, "super-secret-value") {
		t.Fatal("secret leaked into handoff")
	}
	if strings.Contains(created.Handoff.Markdown, "session-1") || created.Handoff.Source.SessionID != "" {
		t.Fatal("provider-local session identifier leaked into the public handoff")
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

func TestPublishAndFetchExplicitContextAttachment(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&API{Store: store, PublicURL: "https://handoff.example"}).Handler())
	defer server.Close()

	input := types.PublishRequest{
		Goal:      "continue",
		Source:    types.SourceRef{Kind: "codex"},
		Generator: "agent:codex",
		Sections:  types.Sections{Context: "summary", CurrentState: "ready", NextSteps: []string{"ask user"}},
		ContextAttachment: &types.ContextAttachment{
			Version: 99,
			Source:  types.SourceRef{Kind: "codex", SessionID: "private-session", Cursor: "line:10"},
			Messages: []types.Message{
				{Role: "user", Text: "read /Users/alice/work/demo"},
				{Role: "tool", Text: "must be removed"},
				{Role: "assistant", Text: "done"},
			},
			Redaction: "untrusted",
		},
	}
	body, _ := json.Marshal(input)
	response, err := http.Post(server.URL+"/v1/handoffs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("publish status = %d: %s", response.StatusCode, data)
	}
	var created types.CreateResponse
	if err := json.NewDecoder(response.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Handoff.Context == nil || !created.Handoff.Context.Available || created.Handoff.Context.MessageCount != 2 {
		t.Fatalf("missing attachment metadata: %#v", created.Handoff.Context)
	}
	if !strings.Contains(created.Handoff.Markdown, "### Attached Context") || !strings.Contains(created.Handoff.Markdown, "handoff context opengrove-handoff:") {
		t.Fatalf("attachment instructions missing:\n%s", created.Handoff.Markdown)
	}

	regular, err := http.Get(server.URL + "/v1/handoffs/" + created.Handoff.ID)
	if err != nil {
		t.Fatal(err)
	}
	regularBody, _ := io.ReadAll(regular.Body)
	regular.Body.Close()
	if strings.Contains(string(regularBody), "read $HOME") || strings.Contains(string(regularBody), "private-session") {
		t.Fatalf("normal receive leaked attached Context: %s", regularBody)
	}

	attached, err := http.Get(server.URL + "/v1/handoffs/" + created.Handoff.ID + "/context")
	if err != nil {
		t.Fatal(err)
	}
	defer attached.Body.Close()
	if attached.StatusCode != http.StatusOK {
		t.Fatalf("context status = %d", attached.StatusCode)
	}
	var contextResponse types.ContextResponse
	if err := json.NewDecoder(attached.Body).Decode(&contextResponse); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(contextResponse)
	if contextResponse.Context.Version != types.ContextAttachmentVersion || len(contextResponse.Context.Messages) != 2 ||
		strings.Contains(string(encoded), "private-session") || strings.Contains(string(encoded), "must be removed") ||
		!strings.Contains(string(encoded), "$HOME/work/demo") {
		t.Fatalf("unexpected attached Context: %s", encoded)
	}
}

func TestStoreKeepsHandoffUntilDeleted(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handoff := types.Handoff{
		Version:   types.ProtocolVersion,
		ID:        "abcdefghijklmnopqrstuv",
		CreatedAt: time.Now().Add(-time.Hour),
	}
	if err := store.Save(handoff); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(handoff.ID); err != nil {
		t.Fatalf("permanent handoff was not returned: %v", err)
	}
}

func TestDefaultPublishEndpointRejectsRawContext(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer((&API{Store: store}).Handler())
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
