package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckAndApplyVerifiedRelease(t *testing.T) {
	var archive bytes.Buffer
	gzipWriter := gzip.NewWriter(&archive)
	tarWriter := tar.NewWriter(gzipWriter)
	binary := []byte("new-handoff-binary")
	if err := tarWriter.WriteHeader(&tar.Header{Name: "handoff", Mode: 0o755, Size: int64(len(binary))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tarWriter.Write(binary); err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(archive.Bytes())
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/open-grove/handoff/releases/latest":
			fmt.Fprintf(response, `{"tag_name":"v0.8.0","html_url":"%s/release","assets":[{"name":"handoff_darwin_arm64.tar.gz","url":"%s/asset"},{"name":"SHA256SUMS","url":"%s/checksums"}]}`, server.URL, server.URL, server.URL)
		case "/asset":
			_, _ = response.Write(archive.Bytes())
		case "/checksums":
			fmt.Fprintf(response, "%s  handoff_darwin_arm64.tar.gz\n", hex.EncodeToString(sum[:]))
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client := Client{HTTP: server.Client(), APIBaseURL: server.URL, GOOS: "darwin", GOARCH: "arm64"}
	result, err := client.Check(context.Background(), "0.7.2")
	if err != nil {
		t.Fatal(err)
	}
	if !result.UpdateAvailable || result.LatestVersion != "0.8.0" {
		t.Fatalf("unexpected update result: %#v", result)
	}
	target := filepath.Join(t.TempDir(), "handoff")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := client.Apply(context.Background(), result, target); err != nil {
		t.Fatal(err)
	}
	updated, _ := os.ReadFile(target)
	if string(updated) != string(binary) {
		t.Fatalf("updated binary = %q", updated)
	}
}

func TestChecksumMismatchFailsClosed(t *testing.T) {
	if _, err := checksumFor([]byte("bad  asset.tar.gz\n"), "asset.tar.gz"); err == nil {
		t.Fatal("accepted invalid checksum")
	}
}

func TestVersionComparison(t *testing.T) {
	if compareVersions("0.8.0", "0.7.2") <= 0 || compareVersions("1.0.0", "1.0.0") != 0 || compareVersions("1.0.0", "1.1.0") >= 0 {
		t.Fatal("semantic version comparison failed")
	}
}
