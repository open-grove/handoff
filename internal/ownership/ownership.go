package ownership

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type file struct {
	Version     int               `json:"version"`
	Credentials map[string]string `json:"credentials"`
}

func Path() (string, error) {
	if value := strings.TrimSpace(os.Getenv("HANDOFF_OWNERSHIP_FILE")); value != "" {
		return filepath.Abs(value)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "handoff", "ownership.json"), nil
}

func Save(server, id, token string) error {
	server, id, token = normalizeServer(server), strings.TrimSpace(id), strings.TrimSpace(token)
	if server == "" || id == "" || token == "" {
		return errors.New("server, handoff id, and delete credential are required")
	}
	path, err := Path()
	if err != nil {
		return err
	}
	stored, err := load(path)
	if err != nil {
		return err
	}
	stored.Credentials[key(server, id)] = token
	return save(path, stored)
}

func Get(server, id string) (string, error) {
	path, err := Path()
	if err != nil {
		return "", err
	}
	stored, err := load(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(stored.Credentials[key(normalizeServer(server), strings.TrimSpace(id))]), nil
}

func Remove(server, id string) error {
	path, err := Path()
	if err != nil {
		return err
	}
	stored, err := load(path)
	if err != nil {
		return err
	}
	delete(stored.Credentials, key(normalizeServer(server), strings.TrimSpace(id)))
	return save(path, stored)
}

func load(path string) (file, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return file{Version: 1, Credentials: map[string]string{}}, nil
	}
	if err != nil {
		return file{}, err
	}
	var stored file
	if err := json.Unmarshal(data, &stored); err != nil {
		return file{}, fmt.Errorf("read ownership credentials: %w", err)
	}
	if stored.Version == 0 {
		stored.Version = 1
	}
	if stored.Credentials == nil {
		stored.Credentials = map[string]string{}
	}
	return stored, nil
}

func save(path string, stored file) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".ownership-*.tmp")
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
	return os.Rename(tempPath, path)
}

func normalizeServer(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return strings.TrimRight(strings.TrimSpace(value), "/")
	}
	return strings.ToLower(parsed.Scheme + "://" + parsed.Host)
}

func key(server, id string) string {
	return server + "/h/" + id
}
