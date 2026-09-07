package tailmux

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Install only into an unused status line. Existing custom commands remain intact.
func setupClaudeMonitor() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	base := os.Getenv("CLAUDE_CONFIG_DIR")
	if base == "" {
		base = filepath.Join(home, ".claude")
	}
	path := filepath.Join(base, "settings.json")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	settings := map[string]json.RawMessage{}
	if len(data) > 0 {
		if err = json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("read Claude settings: %w", err)
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	command := shellQuote(exe) + " monitor claude-statusline"
	if existing, ok := settings["statusLine"]; ok && string(existing) != "null" {
		var hook struct{ Command string }
		_ = json.Unmarshal(existing, &hook)
		if hook.Command == command {
			fmt.Println("Claude monitor status line is already configured.")
			return nil
		}
		return fmt.Errorf("Claude already has a custom status line; preserve it and pipe a copy of its input to %s", command)
	}
	settings["statusLine"], _ = json.Marshal(map[string]string{"type": "command", "command": command})
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err = os.MkdirAll(base, 0700); err != nil {
		return err
	}
	if len(data) > 0 {
		backup, err := os.CreateTemp(base, "settings.tailmux-backup-*.json")
		if err != nil {
			return err
		}
		_, writeErr := backup.Write(data)
		closeErr := backup.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	tmp, err := os.CreateTemp(base, ".tailmux-settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(append(encoded, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	fmt.Println("Claude monitor enabled. Open a new Claude Code session; telemetry appears after its first response.")
	fmt.Println("To disable it, remove the statusLine entry containing 'tailmux monitor claude-statusline' from " + path + ".")
	return nil
}
