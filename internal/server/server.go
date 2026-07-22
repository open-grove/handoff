package server

import (
	"context"
	"crypto/rand"
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

type API struct {
	Store      *Store
	Compactor  card.Compactor
	Token      string
	PublicURL  string
	DefaultTTL time.Duration
	MaxTTL     time.Duration
	Logger     *slog.Logger
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
	if !validID.MatchString(handoff.ID) {
		return errors.New("invalid handoff id")
	}
	data, err := json.Marshal(handoff)
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
	var handoff types.Handoff
	if err := json.Unmarshal(data, &handoff); err != nil {
		return types.Handoff{}, err
	}
	if time.Now().After(handoff.ExpiresAt) {
		_ = store.Delete(id)
		return types.Handoff{}, os.ErrNotExist
	}
	return handoff, nil
}

func (store *Store) Delete(id string) error {
	if !validID.MatchString(id) {
		return os.ErrNotExist
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return os.Remove(store.path(id))
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
	mux.HandleFunc("POST /v1/handoffs", api.create)
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
		"auth":     "Bearer token",
		"required": []string{"goal", "context.source", "context.messages"},
		"limits":   map[string]any{"body_bytes": maxBodyBytes, "max_ttl_seconds": int64(api.maxTTL().Seconds())},
	})
}

func (api *API) create(response http.ResponseWriter, request *http.Request) {
	if !api.authorized(request) {
		writeJSON(response, http.StatusUnauthorized, types.ErrorResponse{Error: "unauthorized"})
		return
	}
	request.Body = http.MaxBytesReader(response, request.Body, maxBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	var input types.CreateRequest
	if err := decoder.Decode(&input); err != nil {
		writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: "invalid request: " + err.Error()})
		return
	}
	if err := requireEOF(decoder); err != nil {
		writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: "invalid request: " + err.Error()})
		return
	}
	input.Goal = card.SanitizeGoal(input.Goal)
	input.Context = card.SanitizeContext(input.Context)
	if input.Goal == "" || input.Context.Source == "" || len(input.Context.Messages) == 0 {
		writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: "goal, context.source, and context.messages are required"})
		return
	}
	ttl := api.defaultTTL()
	if input.TTLSeconds > 0 {
		ttl = time.Duration(input.TTLSeconds) * time.Second
	}
	if ttl < 5*time.Minute || ttl > api.maxTTL() {
		writeJSON(response, http.StatusBadRequest, types.ErrorResponse{Error: fmt.Sprintf("ttl must be between 5m and %s", api.maxTTL())})
		return
	}
	id, err := randomID()
	if err != nil {
		writeJSON(response, http.StatusInternalServerError, types.ErrorResponse{Error: "could not allocate handoff"})
		return
	}
	now := time.Now().UTC()
	handoff, compactError := card.Build(request.Context(), api.Compactor, id, input.Goal, input.Context, now, now.Add(ttl))
	if compactError != nil {
		api.logger().Warn("model compaction unavailable; using deterministic handoff", "error", compactError)
	}
	if err := api.Store.Save(handoff); err != nil {
		api.logger().Error("save handoff", "error", err)
		writeJSON(response, http.StatusInternalServerError, types.ErrorResponse{Error: "could not save handoff"})
		return
	}
	writeJSON(response, http.StatusCreated, types.CreateResponse{Handoff: handoff, ShareURL: api.shareURL(id)})
}

func (api *API) get(response http.ResponseWriter, request *http.Request) {
	handoff, err := api.Store.Get(request.PathValue("id"))
	if err != nil {
		writeJSON(response, http.StatusNotFound, types.ErrorResponse{Error: "handoff not found or expired"})
		return
	}
	writeJSON(response, http.StatusOK, types.CreateResponse{Handoff: handoff, ShareURL: api.shareURL(handoff.ID)})
}

func (api *API) delete(response http.ResponseWriter, request *http.Request) {
	if !api.authorized(request) {
		writeJSON(response, http.StatusUnauthorized, types.ErrorResponse{Error: "unauthorized"})
		return
	}
	if err := api.Store.Delete(request.PathValue("id")); err != nil && !errors.Is(err, os.ErrNotExist) {
		writeJSON(response, http.StatusInternalServerError, types.ErrorResponse{Error: "could not delete handoff"})
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (api *API) page(response http.ResponseWriter, request *http.Request) {
	handoff, err := api.Store.Get(request.PathValue("id"))
	if err != nil {
		http.NotFound(response, request)
		return
	}
	response.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(response, card.HTML(handoff))
}

func (api *API) authorized(request *http.Request) bool {
	if api.Token == "" {
		return true
	}
	provided := strings.TrimSpace(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "))
	return len(provided) == len(api.Token) && subtle.ConstantTimeCompare([]byte(provided), []byte(api.Token)) == 1
}

func (api *API) shareURL(id string) string {
	if api.PublicURL == "" {
		return ""
	}
	return strings.TrimRight(api.PublicURL, "/") + "/h/" + id
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
