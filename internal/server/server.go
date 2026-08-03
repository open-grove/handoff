package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/open-grove/handoff/internal/card"
	"github.com/open-grove/handoff/internal/types"
)

const maxBodyBytes = 4 << 20

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{20,32}$`)

type Store struct {
	dir string
	mu  sync.RWMutex
}

type storedHandoff struct {
	types.Handoff
	Context         *types.ContextAttachment `json:"context_attachment,omitempty"`
	DeleteTokenHash string                   `json:"delete_token_hash,omitempty"`
}

type API struct {
	Store               *Store
	Compactor           card.Compactor
	Token               string
	VerifyOpenGroveUser func(context.Context, string) (bool, error)
	PublicURL           string
	DefaultTTL          time.Duration
	MaxTTL              time.Duration
	Logger              *slog.Logger
}

func NewStore(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("data directory is required")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (store *Store) Save(handoff types.Handoff) error {
	return store.SaveOwned(handoff, "")
}

func (store *Store) SaveOwned(handoff types.Handoff, deleteTokenHash string) error {
	return store.SaveOwnedWithContext(handoff, nil, deleteTokenHash)
}

func (store *Store) SaveOwnedWithContext(handoff types.Handoff, contextAttachment *types.ContextAttachment, deleteTokenHash string) error {
	if !validID.MatchString(handoff.ID) {
		return errors.New("invalid handoff id")
	}
	data, err := json.Marshal(storedHandoff{
		Handoff: handoff, Context: contextAttachment,
		DeleteTokenHash: strings.TrimSpace(deleteTokenHash),
	})
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	temp, err := os.CreateTemp(store.dir, ".handoff-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(append(data, '\n')); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, store.path(handoff.ID))
}

func (store *Store) Get(id string) (types.Handoff, error) {
	if !validID.MatchString(id) {
		return types.Handoff{}, os.ErrNotExist
	}
	store.mu.RLock()
	data, err := os.ReadFile(store.path(id))
	store.mu.RUnlock()
	if err != nil {
		return types.Handoff{}, err
	}
	var stored storedHandoff
	if err := json.Unmarshal(data, &stored); err != nil {
		return types.Handoff{}, err
	}
	handoff := stored.Handoff
	if time.Now().After(handoff.ExpiresAt) {
		_ = store.Delete(id)
		return types.Handoff{}, os.ErrNotExist
	}
	return handoff, nil
}

func (store *Store) GetContext(id string) (types.ContextAttachment, error) {
	if !validID.MatchString(id) {
		return types.ContextAttachment{}, os.ErrNotExist
	}
	store.mu.RLock()
	data, err := os.ReadFile(store.path(id))
	store.mu.RUnlock()
	if err != nil {
		return types.ContextAttachment{}, err
	}
	var stored storedHandoff
	if err := json.Unmarshal(data, &stored); err != nil {
		return types.ContextAttachment{}, err
	}
	if time.Now().After(stored.ExpiresAt) {
		_ = store.Delete(id)
		return types.ContextAttachment{}, os.ErrNotExist
	}
	if stored.Context == nil {
		return types.ContextAttachment{}, os.ErrNotExist
	}
	return *stored.Context, nil
}

func (store *Store) Delete(id string) error {
	if !validID.MatchString(id) {
		return os.ErrNotExist
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return os.Remove(store.path(id))
}

func (store *Store) DeleteOwned(id, token string) (bool, error) {
	if !validID.MatchString(id) || strings.TrimSpace(token) == "" {
		return false, nil
	}
	store.mu.RLock()
	data, err := os.ReadFile(store.path(id))
	store.mu.RUnlock()
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var stored storedHandoff
	if err := json.Unmarshal(data, &stored); err != nil {
		return false, err
	}
	expected := strings.TrimSpace(stored.DeleteTokenHash)
	actual := hashDeleteToken(token)
	if expected == "" || len(expected) != len(actual) || subtle.ConstantTimeCompare([]byte(expected), []byte(actual)) != 1 {
		return false, nil
	}
	if err := store.Delete(id); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

func (store *Store) Cleanup() int {
	entries, err := os.ReadDir(store.dir)
	if err != nil {
		return 0
	}
	removed := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if _, err := store.Get(id); errors.Is(err, os.ErrNotExist) {
			removed++
		}
	}
	return removed
}

func (store *Store) path(id string) string { return filepath.Join(store.dir, id+".json") }

func (api *API) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", api.health)
	mux.HandleFunc("GET /v1/schema/create", api.createSchema)
	mux.HandleFunc("GET /v1/schema/compact", api.compactSchema)
	mux.HandleFunc("POST /v1/handoffs", api.publish)
	mux.HandleFunc("POST /v1/handoffs/compact", api.compact)
	mux.HandleFunc("POST /v1/handoffs/compact-preview", api.compactPreview)
	mux.HandleFunc("GET /v1/handoffs/{id}/context", api.getContext)
	mux.HandleFunc("GET /v1/handoffs/{id}", api.get)
	mux.HandleFunc("DELETE /v1/handoffs/{id}", api.delete)
	mux.HandleFunc("GET /h/{id}", api.page)
	return api.securityHeaders(api.logRequests(mux))
}

func (api *API) health(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]any{
		"ok":               true,
		"service":          "handoffd",
		"version":          types.ProtocolVersion,
		"model_configured": api.Compactor != nil,
	})
}

func (api *API) createSchema(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]any{
		"method":   "POST",
		"path":     "/v1/handoffs",
		"risk":     "write",
		"auth":     "none",
		"required": []string{"goal", "source.kind", "sections", "generator"},
		"optional": []string{"sections.intent", "context_attachment"},
		"privacy":  "publishing is anonymous; context_attachment is persisted only when explicitly supplied and is re-sanitized by the service",
		"limits":   map[string]any{"body_bytes": maxBodyBytes, "max_ttl_seconds": int64(api.maxTTL().Seconds())},
	})
}

func (api *API) compactSchema(response http.ResponseWriter, _ *http.Request) {
	writeJSON(response, http.StatusOK, map[string]any{
		"method":   "POST",
		"path":     "/v1/handoffs/compact-preview",
		"risk":     "write",
		"auth":     "OpenGrove access token",
		"required": []string{"goal", "context.source", "context.summary or context.messages"},
		"optional": []string{"intent: auto|share|continue"},
		"privacy":  "cloud generation temporarily processes canonical sanitized readable context; it is not stored by this endpoint",
		"limits":   map[string]any{"body_bytes": maxBodyBytes, "max_ttl_seconds": int64(api.maxTTL().Seconds())},
	})
}

func (api *API) publish(response http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(response, request.Body, maxBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input types.PublishRequest
	if err := decoder.Decode(&input); err != nil {
		writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: "invalid request: " + err.Error()})
		return
	}
	if err := requireEOF(decoder); err != nil {
		writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: "invalid request: " + err.Error()})
		return
	}
	ttl, err := api.resolveTTL(input.TTLSeconds)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: err.Error()})
		return
	}
	id, err := randomID()
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, types.ErrorResponse{Error: "could not allocate handoff"})
		return
	}
	now := time.Now().UTC()
	handoff, err := card.BuildFromSections(id, input.Goal, input.Source, input.Sections, input.Generator, now, now.Add(ttl))
	if err != nil {
		writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: err.Error()})
		return
	}
	var contextAttachment *types.ContextAttachment
	if input.ContextAttachment != nil {
		sanitized, sanitizeErr := card.SanitizeContextAttachment(*input.ContextAttachment)
		if sanitizeErr != nil {
			writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: sanitizeErr.Error()})
			return
		}
		contextAttachment = &sanitized
		handoff.Context = card.ContextMetadata(sanitized)
		handoff.Markdown = card.Render(handoff, input.Sections)
	}
	api.saveCreated(response, handoff, contextAttachment)
}

func (api *API) compact(response http.ResponseWriter, request *http.Request) {
	if !api.requireOpenGroveUser(response, request) {
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input types.CompactRequest
	if err := decoder.Decode(&input); err != nil {
		writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: "invalid request: " + err.Error()})
		return
	}
	if err := requireEOF(decoder); err != nil {
		writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: "invalid request: " + err.Error()})
		return
	}
	input.Goal = card.SanitizeGoal(input.Goal)
	input.Intent = card.SanitizeIntent(input.Intent)
	input.Context = card.SanitizeContext(input.Context)
	if input.Intent == "" || input.Goal == "" || input.Context.Source == "" || !hasContext(input.Context) {
		writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: "goal, context.source, and context summary or messages are required"})
		return
	}
	ttl, err := api.resolveTTL(input.TTLSeconds)
	if err != nil {
		writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: err.Error()})
		return
	}
	id, err := randomID()
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, types.ErrorResponse{Error: "could not allocate handoff"})
		return
	}
	now := time.Now().UTC()
	handoff, compactError := card.Build(request.Context(), api.Compactor, id, input.Intent, input.Goal, input.Context, now, now.Add(ttl))
	if compactError != nil {
		api.logger().Warn("model generation unavailable; using deterministic handoff", "error", compactError)
	}
	api.saveCreated(response, handoff, nil)
}

func (api *API) compactPreview(response http.ResponseWriter, request *http.Request) {
	if !api.requireOpenGroveUser(response, request) {
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input types.CompactRequest
	if err := decoder.Decode(&input); err != nil {
		writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: "invalid request: " + err.Error()})
		return
	}
	if err := requireEOF(decoder); err != nil {
		writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: "invalid request: " + err.Error()})
		return
	}
	input.Goal = card.SanitizeGoal(input.Goal)
	input.Intent = card.SanitizeIntent(input.Intent)
	input.Context = card.SanitizeContext(input.Context)
	if input.Intent == "" || input.Goal == "" || input.Context.Source == "" || !hasContext(input.Context) {
		writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: "goal, context.source, and context summary or messages are required"})
		return
	}
	sections, generator, compactError := card.GenerateSections(request.Context(), api.Compactor, input.Intent, input.Goal, input.Context)
	warning := ""
	if api.Compactor == nil {
		warning = "server compactor is not configured; deterministic sections were used"
	} else if compactError != nil {
		warning = card.Redact(compactError.Error())
		api.logger().Warn("preview model generation unavailable; using deterministic handoff", "error", compactError)
	}
	writeJSON(response, http.StatusOK, types.CompactPreviewResponse{
		Sections: sections, Generator: generator, Warning: warning,
	})
}

func hasContext(input types.Context) bool {
	return strings.TrimSpace(input.Summary) != "" || len(input.Messages) > 0
}

func (api *API) saveCreated(response http.ResponseWriter, handoff types.Handoff, contextAttachment *types.ContextAttachment) {
	deleteToken, deleteTokenHash, err := newDeleteCredential()
	if err != nil {
		api.logger().Error("allocate delete credential", "error", err)
		writeJSON(response, http.StatusInternalServerError, types.ErrorResponse{Error: "could not create handoff ownership"})
		return
	}
	if err := api.Store.SaveOwnedWithContext(handoff, contextAttachment, deleteTokenHash); err != nil {
		api.logger().Error("save handoff", "error", err)
		writeJSON(response, http.StatusInternalServerError, types.ErrorResponse{Error: "could not save handoff"})
		return
	}
	result := api.createResponse(handoff)
	result.DeleteToken = deleteToken
	writeJSON(response, http.StatusCreated, result)
}

func (api *API) getContext(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	contextAttachment, err := api.Store.GetContext(id)
	if err != nil {
		writeJSON(response, http.StatusNotFound, types.ErrorResponse{Error: "attached context not found or expired"})
		return
	}
	writeJSON(response, http.StatusOK, types.ContextResponse{HandoffID: id, Context: contextAttachment})
}

func (api *API) get(response http.ResponseWriter, request *http.Request) {
	handoff, err := api.Store.Get(request.PathValue("id"))
	if err != nil {
		writeJSON(response, http.StatusNotFound, types.ErrorResponse{Error: "handoff not found or expired"})
		return
	}
	writeJSON(response, http.StatusOK, api.createResponse(handoff))
}

func (api *API) delete(response http.ResponseWriter, request *http.Request) {
	id := request.PathValue("id")
	if api.authorizedAdmin(request) {
		if err := api.Store.Delete(id); err != nil && !errors.Is(err, os.ErrNotExist) {
			writeJSON(response, http.StatusInternalServerError, types.ErrorResponse{Error: "could not delete handoff"})
			return
		}
		response.WriteHeader(http.StatusNoContent)
		return
	}
	authorized, err := api.Store.DeleteOwned(id, request.Header.Get("X-Handoff-Delete-Token"))
	if err != nil {
		api.logger().Error("delete owned handoff", "error", err)
		writeJSON(response, http.StatusInternalServerError, types.ErrorResponse{Error: "could not delete handoff"})
		return
	}
	if !authorized {
		writeJSON(response, http.StatusUnauthorized, types.ErrorResponse{Error: "unauthorized"})
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (api *API) page(response http.ResponseWriter, request *http.Request) {
	reference := request.PathValue("id")
	rawMarkdown := strings.HasSuffix(reference, ".md")
	id := strings.TrimSuffix(reference, ".md")
	handoff, err := api.Store.Get(id)
	if err != nil {
		http.NotFound(response, request)
		return
	}
	if rawMarkdown {
		response.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		response.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"handoff-%s.md\"", id))
		_, _ = io.WriteString(response, handoff.Markdown)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(response, card.HTML(handoff))
}

func (api *API) requireOpenGroveUser(response http.ResponseWriter, request *http.Request) bool {
	token := bearerToken(request)
	if token == "" {
		writeJSON(response, http.StatusUnauthorized, types.ErrorResponse{Error: "OpenGrove login required"})
		return false
	}
	if api.VerifyOpenGroveUser == nil {
		writeJSON(response, http.StatusServiceUnavailable, types.ErrorResponse{Error: "OpenGrove authentication is unavailable"})
		return false
	}
	ok, err := api.VerifyOpenGroveUser(request.Context(), token)
	if err != nil {
		api.logger().Warn("verify OpenGrove user", "error", err)
		writeJSON(response, http.StatusServiceUnavailable, types.ErrorResponse{Error: "OpenGrove authentication is temporarily unavailable"})
		return false
	}
	if !ok {
		writeJSON(response, http.StatusUnauthorized, types.ErrorResponse{Error: "OpenGrove login required"})
		return false
	}
	return true
}

func (api *API) authorizedAdmin(request *http.Request) bool {
	if api.Token == "" {
		return false
	}
	provided := bearerToken(request)
	return len(provided) == len(api.Token) && subtle.ConstantTimeCompare([]byte(provided), []byte(api.Token)) == 1
}

func bearerToken(request *http.Request) string {
	header := strings.TrimSpace(request.Header.Get("Authorization"))
	if !strings.HasPrefix(header, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
}

func (api *API) shareURL(id string) string {
	if api.PublicURL == "" {
		return ""
	}
	return strings.TrimRight(api.PublicURL, "/") + "/h/" + id
}

func (api *API) markdownURL(id string) string {
	if shareURL := api.shareURL(id); shareURL != "" {
		return shareURL + ".md"
	}
	return ""
}

func (api *API) createResponse(handoff types.Handoff) types.CreateResponse {
	return types.CreateResponse{
		Handoff:     handoff,
		ShareURL:    api.shareURL(handoff.ID),
		MarkdownURL: api.markdownURL(handoff.ID),
	}
}

func (api *API) defaultTTL() time.Duration {
	if api.DefaultTTL > 0 {
		return api.DefaultTTL
	}
	return 7 * 24 * time.Hour
}

func (api *API) maxTTL() time.Duration {
	if api.MaxTTL > 0 {
		return api.MaxTTL
	}
	return 30 * 24 * time.Hour
}

func (api *API) resolveTTL(seconds int64) (time.Duration, error) {
	ttl := api.defaultTTL()
	if seconds > 0 {
		ttl = time.Duration(seconds) * time.Second
	}
	if ttl < 5*time.Minute || ttl > api.maxTTL() {
		return 0, fmt.Errorf("ttl must be between 5m and %s", api.maxTTL())
	}
	return ttl, nil
}

func (api *API) logger() *slog.Logger {
	if api.Logger != nil {
		return api.Logger
	}
	return slog.Default()
}

func (api *API) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		next.ServeHTTP(response, request)
		api.logger().Info("request", "method", request.Method, "path", safeLogPath(request.URL.Path), "duration_ms", time.Since(started).Milliseconds())
	})
}

func safeLogPath(path string) string {
	if path == "/v1/handoffs/compact" || path == "/v1/handoffs/compact-preview" {
		return path
	}
	if strings.HasPrefix(path, "/v1/handoffs/") {
		return "/v1/handoffs/:id"
	}
	if strings.HasPrefix(path, "/h/") {
		return "/h/:id"
	}
	return path
}

func (api *API) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(response, request)
	})
}

func randomID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func newDeleteCredential() (string, string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", "", err
	}
	token := base64.RawURLEncoding.EncodeToString(data)
	return token, hashDeleteToken(token), nil
}

func hashDeleteToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request must contain one JSON object")
		}
		return err
	}
	return nil
}

func RunCleanup(ctx context.Context, store *Store, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if removed := store.Cleanup(); removed > 0 {
				logger.Info("expired handoffs removed", "count", removed)
			}
		}
	}
}
