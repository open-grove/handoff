package config

import "testing"

func TestDefaultServerIsOpenGroveService(t *testing.T) {
	if defaultServer != "https://legacy-origin.example" {
		t.Fatalf("defaultServer = %q", defaultServer)
	}
}
