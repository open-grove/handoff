package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const defaultServer = "https://legacy-origin.example"

type Profile struct {
	Server string `json:"server"`
	Token  string `json:"token,omitempty"`
}

type File struct {
	Current  string             `json:"current"`
	Profiles map[string]Profile `json:"profiles"`
}

func Path() (string, error) {
	if value := strings.TrimSpace(os.Getenv("HANDOFF_CONFIG")); value != "" {
		return filepath.Abs(value)
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "handoff", "config.json"), nil
}

func Load() (File, error) {
	path, err := Path()
	if err != nil {
		return File{}, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return File{Current: "default", Profiles: map[string]Profile{"default": {Server: defaultServer}}}, nil
	}
	if err != nil {
		return File{}, err
	}
	var cfg File
	if err := json.Unmarshal(data, &cfg); err != nil {
		return File{}, fmt.Errorf("read config: %w", err)
	}
	if cfg.Current == "" {
		cfg.Current = "default"
	}
	if cfg.Profiles == nil {
		cfg.Profiles = map[string]Profile{}
	}
	if _, ok := cfg.Profiles[cfg.Current]; !ok {
		cfg.Profiles[cfg.Current] = Profile{Server: defaultServer}
	}
	return cfg, nil
}

func Save(cfg File) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".config-*.tmp")
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

func Resolve(cfg File, name string) (string, Profile, error) {
	if name == "" {
		name = cfg.Current
	}
	profile, ok := cfg.Profiles[name]
	if !ok {
		return "", Profile{}, fmt.Errorf("profile %q not found", name)
	}
	if value := strings.TrimSpace(os.Getenv("HANDOFF_SERVER")); value != "" {
		profile.Server = value
	}
	if value := strings.TrimSpace(os.Getenv("HANDOFF_TOKEN")); value != "" {
		profile.Token = value
	}
	if profile.Server == "" {
		profile.Server = defaultServer
	}
	profile.Server = strings.TrimRight(profile.Server, "/")
	return name, profile, nil
}
