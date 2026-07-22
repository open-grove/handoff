package client

import "testing"

func TestValidateServerRequiresHTTPSOutsideLoopback(t *testing.T) {
	for _, value := range []string{"https://handoff.example", "http://127.0.0.1:7391", "http://localhost:7391", "http://[::1]:7391"} {
		if err := validateServer(value); err != nil {
			t.Fatalf("validateServer(%q): %v", value, err)
		}
	}
	for _, value := range []string{"http://handoff.example", "handoff.example", ""} {
		if err := validateServer(value); err == nil {
			t.Fatalf("validateServer(%q) accepted insecure value", value)
		}
	}
}
