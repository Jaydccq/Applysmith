package main

import (
	"encoding/json"
	"os"
	"testing"
)

// mygo derives the user data directory, the bundle name and the process name
// from the app name. The legacy desktop app owns "Auto Job" (its profile lives
// in ~/Library/Application Support/Auto Job), so this app must never take it.
func TestAppNameDoesNotCollideWithLegacyDesktopApp(t *testing.T) {
	raw, err := os.ReadFile("mygo.json")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Name       string `json:"name"`
		Identifier string `json:"identifier"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Name == "" || cfg.Name == "Auto Job" {
		t.Errorf("app name %q collides with the legacy desktop app", cfg.Name)
	}
	if cfg.Identifier != "dev.applysmith.app" {
		t.Errorf("bundle identifier %q, want the provisional dev.applysmith.app", cfg.Identifier)
	}
}
