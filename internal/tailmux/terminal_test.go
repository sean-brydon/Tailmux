package tailmux

import (
	"os"
	"os/exec"
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

func TestTerminalWindowTargetSurvivesRename(t *testing.T) {
	if got := terminalWindowTarget("alpha/worker-a", "my renamed tab"); got != "alpha/worker-a" {
		t.Fatalf("visible rename changed routing: %q", got)
	}
	if got := terminalWindowTarget("", "alpha/worker-a"); got != "alpha/worker-a" {
		t.Fatalf("older window was not migrated by label: %q", got)
	}
}

func TestZellijTargetMetadataSurvivesRenameAndCanReset(t *testing.T) {
	term := terminal{dir: t.TempDir(), backend: "zellij"}
	if err := os.MkdirAll(filepath.Join(term.dir, "terminal"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := term.saveZellijTarget(7, "alpha/worker-a"); err != nil {
		t.Fatal(err)
	}
	targets, err := term.loadZellijTargets()
	if err != nil || targets["7"] != "alpha/worker-a" {
		t.Fatalf("lost stable target: %#v %v", targets, err)
	}
	// The display name is deliberately absent from the metadata: changing it
	// cannot alter the route associated with tab 7.
	if err := term.clearZellijTargets(); err != nil {
		t.Fatal(err)
	}
	targets, err = term.loadZellijTargets()
	if err != nil || len(targets) != 0 {
		t.Fatalf("stale session metadata remains: %#v %v", targets, err)
	}
}

func TestTmuxSelectHostReusesRenamedWindow(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	term := terminal{dir: t.TempDir(), backend: "tmux", session: "tmux-route-" + terminalID(t.Name())}
	t.Cleanup(func() { _ = term.command("kill-server").Run() })
	if out, err := runOutput(term.command("new-session", "-d", "-s", term.session, "-n", "original", "sleep 30")); err != nil {
		t.Fatalf("create isolated tmux: %v: %s", err, out)
	}
	if _, err := runOutput(term.command("set-window-option", "-t", term.session, "@tailmux_target", "alpha/worker-a")); err != nil {
		t.Fatal(err)
	}
	if _, err := runOutput(term.command("rename-window", "-t", term.session, "a friendly label")); err != nil {
		t.Fatal(err)
	}
	if err := term.selectHost("alpha/worker-a"); err != nil {
		t.Fatal(err)
	}
	out, err := runOutput(term.command("list-windows", "-t", term.session, "-F", "#{window_name}\t#{@tailmux_target}"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out) != "a friendly label\talpha/worker-a" {
		t.Fatalf("picker created a duplicate or lost route after rename: %q", out)
	}
}
