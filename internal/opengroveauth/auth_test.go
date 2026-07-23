package opengroveauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestAccessTokenReadsActiveOpenGroveSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-cookies.json")
	t.Setenv("OPENGROVE_AUTH_COOKIES", path)
	expiresAt := time.Now().Add(time.Hour).UnixMilli()
	content := `{"version":1,"cookies":{"opengrove_auth_access":{"value":"access-token","expiresAt":` +
		strconv.FormatInt(expiresAt, 10) + `}}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := AccessToken(time.Now())
	if err != nil || token != "access-token" {
		t.Fatalf("AccessToken() = %q, %v", token, err)
	}
}

func TestAccessTokenRejectsExpiredSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth-cookies.json")
	t.Setenv("OPENGROVE_AUTH_COOKIES", path)
	content := `{"version":1,"cookies":{"opengrove_auth_access":{"value":"expired","expiresAt":1}}}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AccessToken(time.Now()); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("AccessToken() error = %v", err)
	}
}

func TestVerifyAccessTokenUsesOpenGroveUserEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/users/me" || request.Header.Get("Authorization") != "Bearer valid" {
			t.Fatalf("unexpected request: %s %q", request.URL.Path, request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"data":{"user_id":"user-1","email":"person@example.test","role":"user"}}`))
	}))
	defer server.Close()
	ok, err := VerifyAccessToken(context.Background(), server.URL, "valid", server.Client())
	if err != nil || !ok {
		t.Fatalf("VerifyAccessToken() = %v, %v", ok, err)
	}
	user, err := CurrentUser(context.Background(), server.URL, "valid", server.Client())
	if err != nil || user.UserID != "user-1" || user.Email != "person@example.test" {
		t.Fatalf("CurrentUser() = %#v, %v", user, err)
	}
}
