package opengroveauth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const DefaultWWBaseURL = "https://opengrove.creativefitting.cn"

var ErrLoginRequired = errors.New("云端压缩需要先登录 OpenGrove；请打开 OpenGrove 完成登录后重试")

type cookieFile struct {
	Cookies struct {
		Access struct {
			Value     string `json:"value"`
			ExpiresAt int64  `json:"expiresAt"`
		} `json:"opengrove_auth_access"`
	} `json:"cookies"`
}

func AccessToken(now time.Time) (string, error) {
	for _, file := range authCookiePaths() {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var stored cookieFile
		if json.Unmarshal(data, &stored) != nil {
			continue
		}
		access := stored.Cookies.Access
		if strings.TrimSpace(access.Value) == "" {
			continue
		}
		if access.ExpiresAt > 0 && access.ExpiresAt <= now.Add(30*time.Second).UnixMilli() {
			continue
		}
		return access.Value, nil
	}
	return "", ErrLoginRequired
}

func VerifyAccessToken(ctx context.Context, baseURL, token string, httpClient *http.Client) (bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return false, nil
	}
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultWWBaseURL
	}
	if err := validateBaseURL(baseURL); err != nil {
		return false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/users/me", nil)
	if err != nil {
		return false, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return false, fmt.Errorf("verify OpenGrove session: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
		return false, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false, fmt.Errorf("verify OpenGrove session: HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Data struct {
			UserID string `json:"user_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&envelope); err != nil {
		return false, fmt.Errorf("verify OpenGrove session response: %w", err)
	}
	return strings.TrimSpace(envelope.Data.UserID) != "", nil
}

func authCookiePaths() []string {
	if configured := strings.TrimSpace(os.Getenv("OPENGROVE_AUTH_COOKIES")); configured != "" {
		return []string{configured}
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return nil
	}
	stable, development := "OpenGrove", "OpenGroveDev"
	if runtime.GOOS == "linux" {
		stable, development = "opengrove", "opengrove-dev"
	}
	return []string{
		filepath.Join(root, stable, "auth-cookies.json"),
		filepath.Join(root, development, "auth-cookies.json"),
	}
}

func validateBaseURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return errors.New("OpenGrove account server must be an absolute URL")
	}
	if parsed.Scheme == "https" {
		return nil
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if parsed.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || ip != nil && ip.IsLoopback()) {
		return nil
	}
	return errors.New("OpenGrove account server must use HTTPS")
}
