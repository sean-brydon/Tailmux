package tailmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTerminalDefaultPersistsWithoutLaunching(t *testing.T) {
	dir := t.TempDir()
	if err := terminalCLI(dir, []string{"--default", "zellij"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TerminalBackend != "zellij" {
		t.Fatal(cfg.TerminalBackend)
	}
	if err := addProfile(dir, "lab"); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(dir)
	if err != nil || cfg.TerminalBackend != "zellij" {
		t.Fatalf("lost preference: %+v %v", cfg, err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	for _, args := range [][]string{{"--default", "invalid"}, {"--default", "tmux", "--backend", "zellij"}, {"--backend", "invalid"}} {
		if err := terminalCLI(dir, args); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	after, _ := os.ReadFile(filepath.Join(dir, "config.json"))
	if string(before) != string(after) {
		t.Fatal("invalid flags changed config")
	}
}
func TestTerminalRoutingAndIsolation(t *testing.T) {
	cfg := fixtureConfig()
	name, err := canonicalTarget(cfg, "worker-a")
	if err != nil || name != "alpha/worker-a" {
		t.Fatalf("%s %v", name, err)
	}
	a := terminalID("config-a")
	b := terminalID("config-b")
	if a == b || len(a) != 12 {
		t.Fatal("session identity collision")
	}
	t.Setenv("TMUX", "other-server")
	t.Setenv("ZELLIJ_SESSION_NAME", "other-session")
	t.Setenv("SSH_AUTH_SOCK", "/agent.sock")
	term := terminal{dir: "/tmp/test", session: "tailmux-test"}
	env := term.environment()
	hasAgent := false
	for _, e := range env {
		if strings.HasPrefix(e, "TMUX=") || strings.HasPrefix(e, "ZELLIJ") {
			t.Fatal(e)
		}
		if e == "SSH_AUTH_SOCK=/agent.sock" {
			hasAgent = true
		}
	}
	if !hasAgent {
		t.Fatal("lost SSH agent")
	}
}

func TestTerminalBackendOverrideDoesNotChangePreference(t *testing.T) {
	dir := t.TempDir()
	if err := terminalCLI(dir, []string{"--default", "zellij"}); err != nil {
		t.Fatal(err)
	}
	// An unavailable one-off backend must leave the persistent selection intact.
	t.Setenv("PATH", t.TempDir())
	if err := terminalCLI(dir, []string{"--backend", "tmux"}); err == nil {
		t.Fatal("expected missing backend")
	}
	cfg, err := loadConfig(dir)
	if err != nil || cfg.TerminalBackend != "zellij" {
		t.Fatalf("preference changed: %+v %v", cfg, err)
	}
}

func TestLocalTerminalWithoutProfiles(t *testing.T) {
	name, err := terminalTarget(Config{}, "local")
	if err != nil || name != "local" {
		t.Fatalf("local requires no host configuration: %q %v", name, err)
	}
	if _, err := terminalTarget(Config{}, "unknown"); err == nil {
		t.Fatal("unknown remote accepted")
	}
	t.Setenv("SHELL", "/bin/sh")
	cmd := localTerminalShell()
	if cmd.Path != "/bin/sh" || strings.Join(cmd.Args, " ") != "/bin/sh -l" {
		t.Fatalf("unexpected local command: %v", cmd.Args)
	}
}

func TestPickerDisplayPreservesTarget(t *testing.T) {
	names := []string{"local", "alpha/worker-a", "beta/offline"}
	rows := terminalPickerRows(names, map[string]string{"local": "this machine", "alpha/worker-a": "online", "beta/offline": "offline"}, map[string]bool{"alpha/worker-a": true})
	for i, row := range rows {
		target, _, ok := strings.Cut(row, "\t")
		if !ok || strings.TrimSpace(target) != names[i] {
			t.Fatalf("display lost routing identity: %q", row)
		}
	}
	if !strings.Contains(rows[1], "online · saved") || !strings.Contains(rows[0], "this machine") {
		t.Fatal(rows)
	}
}
