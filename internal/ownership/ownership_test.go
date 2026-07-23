package ownership

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialsRoundTripWithoutLeakingPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ownership.json")
	t.Setenv("HANDOFF_OWNERSHIP_FILE", path)
	if err := Save("https://Handoff.Example/", "abcdefghijklmnopqrstuv", "delete-secret"); err != nil {
		t.Fatal(err)
	}
	token, err := Get("https://handoff.example", "abcdefghijklmnopqrstuv")
	if err != nil || token != "delete-secret" {
		t.Fatalf("Get() = %q, %v", token, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("ownership mode = %o", info.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "delete-secret") {
		t.Fatal("credential was not persisted")
	}
	if err := Remove("https://handoff.example", "abcdefghijklmnopqrstuv"); err != nil {
		t.Fatal(err)
	}
	token, err = Get("https://handoff.example", "abcdefghijklmnopqrstuv")
	if err != nil || token != "" {
		t.Fatalf("credential was not removed: %q, %v", token, err)
	}
}
