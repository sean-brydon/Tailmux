package tailmux

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeSetupPreservesSettingsAndCustomStatusline(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, "settings.json")
	original := []byte("{\"theme\":\"dark\",\"permissions\":{\"deny\":[\"Read(.env)\"]}}")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := setupClaudeMonitor(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if string(settings["theme"]) != "\"dark\"" || settings["permissions"] == nil || settings["statusLine"] == nil {
		t.Fatal("lost settings")
	}
	if err := setupClaudeMonitor(); err != nil {
		t.Fatal("setup not idempotent", err)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "settings.tailmux-backup-*.json"))
	if len(backups) != 1 {
		t.Fatal("expected one backup")
	}
	saved, _ := os.ReadFile(backups[0])
	if string(saved) != string(original) {
		t.Fatal("backup differs")
	}
	custom := []byte("{\"statusLine\":{\"type\":\"command\",\"command\":\"custom-status\"}}")
	_ = os.WriteFile(path, custom, 0600)
	if err := setupClaudeMonitor(); err == nil {
		t.Fatal("overwrote custom status line")
	}
	data, _ = os.ReadFile(path)
	if string(data) != string(custom) {
		t.Fatal("custom settings changed")
	}
}
