package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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
