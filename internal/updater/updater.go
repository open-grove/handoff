package updater

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultRepository = "open-grove/handoff"
	maxDownloadBytes  = 128 << 20
)

type Asset struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type githubRelease struct {
	TagName string  `json:"tag_name"`
	HTMLURL string  `json:"html_url"`
	Assets  []Asset `json:"assets"`
}

type Result struct {
	CurrentVersion  string `json:"current_version"`
	LatestVersion   string `json:"latest_version"`
	UpdateAvailable bool   `json:"update_available"`
	ReleaseURL      string `json:"release_url,omitempty"`
	AssetName       string `json:"asset_name,omitempty"`
	release         githubRelease
	asset           Asset
	checksums       Asset
}

type Client struct {
	HTTP       *http.Client
	Token      string
	Repository string
	APIBaseURL string
	GOOS       string
	GOARCH     string
}

func GitHubToken() string {
	for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if token := strings.TrimSpace(os.Getenv(name)); token != "" {
			return token
		}
	}
	path, err := exec.LookPath("gh")
	if err != nil {
		return ""
	}
	command := exec.Command(path, "auth", "token")
	output, err := command.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func (client Client) Check(ctx context.Context, currentVersion string) (Result, error) {
	repository := strings.Trim(strings.TrimSpace(client.Repository), "/")
	if repository == "" {
		repository = defaultRepository
	}
	apiBase := strings.TrimRight(strings.TrimSpace(client.APIBaseURL), "/")
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/repos/"+repository+"/releases/latest", nil)
	if err != nil {
		return Result{}, err
	}
	client.setGitHubHeaders(request, "application/vnd.github+json")
	response, err := client.httpClient().Do(request)
	if err != nil {
		return Result{}, fmt.Errorf("check GitHub release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return Result{}, errors.New("no GitHub release found; private repositories require `gh auth login` or GH_TOKEN")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Result{}, fmt.Errorf("check GitHub release: HTTP %d", response.StatusCode)
	}
	var release githubRelease
	if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&release); err != nil {
		return Result{}, fmt.Errorf("decode GitHub release: %w", err)
	}
	latest := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
	if _, err := parseVersion(latest); err != nil {
		return Result{}, fmt.Errorf("latest release has invalid version %q", release.TagName)
	}
	current := strings.TrimPrefix(strings.TrimSpace(currentVersion), "v")
	if _, err := parseVersion(current); err != nil {
		return Result{}, fmt.Errorf("current CLI has invalid version %q", currentVersion)
	}
	goos, goarch := client.GOOS, client.GOARCH
	if goos == "" {
		goos = runtime.GOOS
	}
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	extension := ".tar.gz"
	if goos == "windows" {
		extension = ".zip"
	}
	assetName := fmt.Sprintf("handoff_%s_%s%s", goos, goarch, extension)
	var binaryAsset, checksums Asset
	for _, asset := range release.Assets {
		switch asset.Name {
		case assetName:
			binaryAsset = asset
		case "SHA256SUMS":
			checksums = asset
		}
	}
	result := Result{
		CurrentVersion:  current,
		LatestVersion:   latest,
		UpdateAvailable: compareVersions(latest, current) > 0,
		ReleaseURL:      release.HTMLURL,
		AssetName:       assetName,
		release:         release,
		asset:           binaryAsset,
		checksums:       checksums,
	}
	if result.UpdateAvailable && (binaryAsset.URL == "" || checksums.URL == "") {
		return Result{}, fmt.Errorf("release %s does not contain %s and SHA256SUMS", release.TagName, assetName)
	}
	return result, nil
}

func (client Client) Apply(ctx context.Context, result Result, executablePath string) error {
	if result.asset.URL == "" || result.checksums.URL == "" {
		return errors.New("release assets are unavailable")
	}
	if runtime.GOOS == "windows" {
		return errors.New("automatic replacement is not yet supported on Windows; download the release asset manually")
	}
	checksums, err := client.download(ctx, result.checksums)
	if err != nil {
		return err
	}
	expected, err := checksumFor(checksums, result.asset.Name)
	if err != nil {
		return err
	}
	archive, err := client.download(ctx, result.asset)
	if err != nil {
		return err
	}
	actual := sha256.Sum256(archive)
	if !strings.EqualFold(expected, hex.EncodeToString(actual[:])) {
		return errors.New("downloaded release failed SHA-256 verification")
	}
	binary, err := extractBinary(result.asset.Name, archive)
	if err != nil {
		return err
	}
	return replaceExecutable(executablePath, binary)
}

func (client Client) download(ctx context.Context, asset Asset) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.URL, nil)
	if err != nil {
		return nil, err
	}
	client.setGitHubHeaders(request, "application/octet-stream")
	response, err := client.httpClient().Do(request)
	if err != nil {
		return nil, fmt.Errorf("download %s: %w", asset.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("download %s: HTTP %d", asset.Name, response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDownloadBytes {
		return nil, fmt.Errorf("download %s is too large", asset.Name)
	}
	return data, nil
}

func (client Client) setGitHubHeaders(request *http.Request, accept string) {
	request.Header.Set("Accept", accept)
	request.Header.Set("User-Agent", "opengrove-handoff")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if token := strings.TrimSpace(client.Token); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
}

func (client Client) httpClient() *http.Client {
	if client.HTTP != nil {
		return client.HTTP
	}
	return &http.Client{Timeout: 2 * time.Minute}
}

func checksumFor(data []byte, name string) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 && strings.TrimPrefix(fields[len(fields)-1], "*") == name {
			if _, err := hex.DecodeString(fields[0]); err != nil || len(fields[0]) != sha256.Size*2 {
				return "", fmt.Errorf("invalid SHA-256 entry for %s", name)
			}
			return fields[0], nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("SHA256SUMS does not contain %s", name)
}

func extractBinary(name string, archive []byte) ([]byte, error) {
	if strings.HasSuffix(name, ".tar.gz") {
		reader, err := gzip.NewReader(bytes.NewReader(archive))
		if err != nil {
			return nil, err
		}
		defer reader.Close()
		tarReader := tar.NewReader(reader)
		for {
			header, err := tarReader.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, err
			}
			if header.Typeflag == tar.TypeReg && filepath.Base(header.Name) == "handoff" {
				return readBinary(tarReader)
			}
		}
	}
	if strings.HasSuffix(name, ".zip") {
		reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, file := range reader.File {
			if filepath.Base(file.Name) != "handoff.exe" {
				continue
			}
			opened, err := file.Open()
			if err != nil {
				return nil, err
			}
			defer opened.Close()
			return readBinary(opened)
		}
	}
	return nil, errors.New("release archive does not contain the handoff binary")
}

func readBinary(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, maxDownloadBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDownloadBytes {
		return nil, errors.New("release binary is too large")
	}
	return data, nil
}

func replaceExecutable(path string, binary []byte) error {
	path, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".handoff-update-*")
	if err != nil {
		return fmt.Errorf("create update beside %s: %w", path, err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	mode := info.Mode().Perm()
	if mode&0o111 == 0 {
		mode = 0o755
	}
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(binary); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func parseVersion(value string) ([3]int, error) {
	core := strings.SplitN(value, "-", 2)[0]
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return [3]int{}, errors.New("expected semantic version")
	}
	var parsed [3]int
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return [3]int{}, errors.New("expected semantic version")
		}
		parsed[index] = number
	}
	return parsed, nil
}

func compareVersions(left, right string) int {
	a, _ := parseVersion(left)
	b, _ := parseVersion(right)
	for index := range a {
		if a[index] > b[index] {
			return 1
		}
		if a[index] < b[index] {
			return -1
		}
	}
	return 0
}
