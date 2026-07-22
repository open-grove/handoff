package config

import "testing"

func TestDefaultServerIsOpenGroveService(t *testing.T) {
	if defaultServer != "https://handoff.openmau.com" {
		t.Fatalf("defaultServer = %q", defaultServer)
	}
}
