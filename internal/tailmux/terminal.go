package tailmux

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type terminal struct{ dir, backend, session, exe string }

func backendName(s string) bool  { return s == "tmux" || s == "zellij" }
func terminalID(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s)))[:12] }
func terminalCLI(dir string, args []string) error {
	fs := flag.NewFlagSet("terminal", flag.ContinueOnError)
	backend := fs.String("backend", "", "tmux or zellij for this launch")
	def := fs.String("default", "", "save tmux or zellij as the default; do not launch")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *def != "" {
		if !backendName(*def) || *backend != "" || fs.NArg() != 0 {
			return fmt.Errorf("usage: tailmux terminal --default tmux|zellij")
		}
		if err := updateConfig(dir, func(c *Config) error { c.TerminalBackend = *def; return nil }); err != nil {
			return err
		}
		fmt.Println("Default terminal:", *def)
		return nil
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		return err
	}
	if *backend == "" {
		*backend = cfg.TerminalBackend
	}
	if *backend == "" {
		*backend = "tmux"
	}
	if !backendName(*backend) {
		return fmt.Errorf("unknown backend %q; choose tmux or zellij", *backend)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	t := terminal{dir: dir, backend: *backend, session: "tailmux-" + terminalID(dir), exe: exe}
	rest := fs.Args()
	if len(rest) > 0 && strings.HasPrefix(rest[0], "_") {
		switch rest[0] {
		case "_pick":
			if len(rest) != 1 {
				return fmt.Errorf("invalid picker arguments")
			}
			return t.pick(cfg)
		case "_shell":
			if len(rest) != 1 {
				return fmt.Errorf("invalid shell arguments")
			}
			return t.shell(cfg)
		}
		return fmt.Errorf("unknown internal terminal action")
	}
	if len(rest) > 1 {
		return fmt.Errorf("usage: tailmux terminal [--backend tmux|zellij] [local|profile/host]")
	}
	target := ""
	if len(rest) == 1 {
		target, err = terminalTarget(cfg, rest[0])
		if err != nil {
			return err
		}
	}
	for _, name := range []string{t.backend, "fzf"} {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("%s is required locally; install it and retry", name)
		}
	}
	if err = t.prepare(); err != nil {
		return err
	}
	if target != "" && t.backend == "tmux" {
		if err = t.selectHost(target); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "Tailmux %s: Alt+B opens the box picker (Zellij: also Ctrl+B or F2). Remote panes use tmux with Ctrl+A.\n", t.backend)
	var cmd *exec.Cmd
	if t.backend == "tmux" {
		cmd = t.command("attach-session", "-t", t.session)
	} else {
		cmd = t.zellij("--config", filepath.Join(t.dir, "terminal", "zellij.kdl"), "attach", t.session)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if t.backend == "zellij" && target != "" {
		return t.attachZellij(cmd, target)
	}
	return cmd.Run()
}
func (t terminal) attachZellij(cmd *exec.Cmd, target string) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	for i := 0; i < 100; i++ {
		select {
		case err := <-done:
			return err
		case <-time.After(100 * time.Millisecond):
		}
		if err := t.selectHost(target); err != nil {
			continue
		}
		out, err := runOutput(t.action("query-tab-names"))
		if err != nil {
			continue
		}
		for _, name := range strings.Split(strings.TrimSpace(out), "\n") {
			if name == target {
				return <-done
			}
		}
	}
	cmd.Process.Signal(os.Interrupt)
	<-done
	return fmt.Errorf("Zellij did not open target %s", target)
}

// "local" is reserved only by the terminal command, never by SSH host routing.
func terminalTarget(c Config, target string) (string, error) {
	if target == "local" {
		return target, nil
	}
	return canonicalTarget(c, target)
}
func canonicalTarget(c Config, target string) (string, error) {
	h, err := c.host(target)
	if err != nil {
		return "", err
	}
	if !strings.Contains(target, "/") {
		target = h.Profile + "/" + target
	}
	return target, nil
}
func (t terminal) invoke(action string) string {
	return "env TAILMUX_HOME=" + shellQuote(t.dir) + " " + shellQuote(t.exe) + " terminal --backend " + t.backend + " " + action
}
func (t terminal) environment() []string {
	env := []string{}
	for _, v := range os.Environ() {
		if strings.HasPrefix(v, "TMUX=") || strings.HasPrefix(v, "TMUX_PANE=") || strings.HasPrefix(v, "ZELLIJ") {
			continue
		}
		env = append(env, v)
	}
	return append(env, "TAILMUX_HOME="+t.dir)
}
func (t terminal) command(args ...string) *exec.Cmd {
	cmd := exec.Command("tmux", append([]string{"-L", t.session, "-f", "/dev/null"}, args...)...)
	cmd.Env = t.environment()
	return cmd
}
func (t terminal) zellij(args ...string) *exec.Cmd {
	cmd := exec.Command("zellij", args...)
	cmd.Env = t.environment()
	return cmd
}
func (t terminal) action(args ...string) *exec.Cmd {
	return t.zellij(append([]string{"--session", t.session, "action"}, args...)...)
}
func runOutput(cmd *exec.Cmd) (string, error) {
	b, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", filepath.Base(cmd.Path), err, strings.TrimSpace(string(b)))
	}
	return string(b), nil
}
func (t terminal) prepare() error {
	if err := os.MkdirAll(filepath.Join(t.dir, "terminal"), 0700); err != nil {
		return err
	}
	if t.backend == "tmux" {
		if t.command("has-session", "-t", t.session).Run() != nil {
			if _, err := runOutput(t.command("new-session", "-d", "-s", t.session, "-n", "Boxes", t.invoke("_shell"))); err != nil {
				return err
			}
		}
		for _, opt := range [][2]string{{"default-command", t.invoke("_shell")}, {"status-left", " Tailmux | "}, {"status-right", "Alt+B boxes | #{window_name}"}, {"mouse", "on"}} {
			if _, err := runOutput(t.command("set-option", "-t", t.session, opt[0], opt[1])); err != nil {
				return err
			}
		}
		if _, err := runOutput(t.command("set-window-option", "-g", "automatic-rename", "off")); err != nil {
			return err
		}
		if _, err := runOutput(t.command("set-window-option", "-g", "allow-rename", "off")); err != nil {
			return err
		}
		for _, key := range []string{"M-b", "b"} {
			args := []string{"bind-key"}
			if key == "M-b" {
				args = append(args, "-n")
			}
			args = append(args, key, "display-popup", "-E", "-w", "80%", "-h", "70%", t.invoke("_pick"))
			if _, err := runOutput(t.command(args...)); err != nil {
				return err
			}
		}
		_, err := runOutput(t.command("bind-key", "c", "new-window", "-n", "#{window_name}"))
		return err
	}
	// An isolated configuration leaves the user's normal Zellij config untouched.
	script := filepath.Join(t.dir, "terminal", "zellij-shell")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexec "+t.invoke("_shell")+"\n"), 0700); err != nil {
		return err
	}
	config := t.zellijConfig(script)
	path := filepath.Join(t.dir, "terminal", "zellij.kdl")
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		return err
	}
	if _, err := runOutput(t.action("query-tab-names")); err == nil {
		return nil
	}
	// Never resurrect a disk snapshot after the user has closed the last tab.
	// Delete only an exited session; without --force Zellij protects a live session.
	sessionsRaw, listErr := t.zellij("list-sessions", "--short", "--no-formatting").CombinedOutput()
	sessions := strings.TrimSpace(string(sessionsRaw))
	if listErr != nil && sessions != "No active zellij sessions found." {
		return fmt.Errorf("list Zellij sessions: %w: %s", listErr, sessions)
	}
	for _, name := range strings.Fields(sessions) {
		if name == t.session {
			if _, err := runOutput(t.zellij("delete-session", t.session)); err != nil {
				if _, liveErr := runOutput(t.action("query-tab-names")); liveErr == nil {
					return nil
				}
				return err
			}
		}
	}
	_, err := runOutput(t.zellij("--config", path, "attach", "--create-background", t.session))
	return err
}
func (t terminal) zellijConfig(script string) string {
	run := "Run \"sh\" \"-c\" " + strconv.Quote(t.invoke("_pick")) + " {\n   floating true\n   close_on_exit true\n   name \"Boxes\"\n  }\n"
	return "default_shell " + strconv.Quote(script) + "\nsession_serialization false\nsimplified_ui true\nshow_startup_tips false\nshow_release_notes false\non_force_close \"detach\"\nkeybinds {\n shared_except \"locked\" {\n  bind \"Alt b\" \"F2\" { " + run + "  }\n }\n normal {\n  bind \"Ctrl b\" { " + run + "  }\n }\n}\n"
}
func (t terminal) namePane(pane, name string) {
	if t.backend == "zellij" {
		if _, err := runOutput(t.action("rename-pane", "--pane-id", "terminal_"+pane, name)); err != nil {
			fmt.Fprintln(os.Stderr, "Could not name pane:", err)
		}
	}
}
func (t terminal) selectHost(target string) error {
	if t.backend == "tmux" {
		out, err := runOutput(t.command("list-windows", "-t", t.session, "-F", "#{window_id}\t#{window_name}"))
		if err != nil {
			return err
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			id, name, ok := strings.Cut(line, "\t")
			if ok && name == target {
				_, err = runOutput(t.command("select-window", "-t", id))
				return err
			}
		}
		_, err = runOutput(t.command("new-window", "-t", t.session, "-n", target, t.invoke("_shell")))
		return err
	}
	_, err := runOutput(t.action("go-to-tab-name", "--create", target))
	return err
}
func (t terminal) pick(cfg Config) error {
	fmt.Fprintln(os.Stderr, "Loading boxes…")
	rows := map[string]string{"local": "this machine"}
	saved := map[string]bool{}
	for _, name := range hostNames(cfg) {
		target, err := canonicalTarget(cfg, name)
		if err == nil {
			rows[target] = "saved"
			saved[target] = true
		}
	}
	type result struct {
		hosts   []discoveredHost
		err     error
		profile string
	}
	ch := make(chan result, len(cfg.Profiles))
	for _, p := range cfg.Profiles {
		go func(p string) { h, e := discover(t.dir, p); ch <- result{h, e, p} }(p)
	}
	for range cfg.Profiles {
		r := <-ch
		if r.err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", r.profile, r.err)
			continue
		}
		for _, h := range r.hosts {
			state := "offline"
			if h.Online {
				state = "online"
			}
			rows[h.Target] = state
		}
	}
	names := make([]string, 0, len(rows))
	for name := range rows {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if names[i] == "local" || names[j] == "local" {
			return names[i] == "local"
		}
		if saved[names[i]] != saved[names[j]] {
			return saved[names[i]]
		}
		if (rows[names[i]] == "online") != (rows[names[j]] == "online") {
			return rows[names[i]] == "online"
		}
		return names[i] < names[j]
	})
	lines := terminalPickerRows(names, rows, saved)
	cmd := exec.Command("fzf", "--prompt=Search boxes > ", "--header=LOCAL + TAILNETS   /   Enter: open or switch   /   Esc: cancel", "--layout=reverse", "--border=rounded", "--border-label= Tailmux · Boxes ", "--info=inline", "--pointer=>", "--marker=+", "--no-hscroll", "--delimiter=\t", "--with-nth=1,2", "--nth=1", "--sync", "--print-query")
	// Prevent user fzf defaults from turning the picker into a different command flow.
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "FZF_DEFAULT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Stdin = strings.NewReader(strings.Join(lines, "\n"))
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		var ex *exec.ExitError
		if errors.As(err, &ex) && (ex.ExitCode() == 130 || ex.ExitCode() == 1) {
			return nil
		}
		return err
	}
	query, selection, ok := strings.Cut(string(out), "\n")
	if !ok {
		return fmt.Errorf("picker returned no selection")
	}
	target, _, _ := strings.Cut(strings.TrimSpace(selection), "\t")
	target = strings.TrimSpace(target)
	if _, ok := rows[target]; !ok {
		return fmt.Errorf("invalid picker selection")
	}
	// Refuse a stale row if Enter raced with an asynchronously filtered query.
	if strings.TrimSpace(query) != "" {
		check := exec.Command("fzf", "--filter="+query)
		check.Env = cmd.Env
		check.Stdin = strings.NewReader(target + "\n")
		if err := check.Run(); err != nil {
			return fmt.Errorf("picker selection changed while filtering; reopen the picker and select again")
		}
	}
	return t.selectHost(target)
}
func terminalPickerRows(names []string, states map[string]string, saved map[string]bool) []string {
	width := 20
	for _, name := range names {
		if len(name) > width {
			width = len(name)
		}
	}
	lines := make([]string, 0, len(names))
	for _, name := range names {
		detail := states[name]
		if saved[name] && detail != "saved" {
			detail += " · saved"
		}
		lines = append(lines, fmt.Sprintf("%-*s\t%s", width, name, detail))
	}
	return lines
}

func localTerminalShell() *exec.Cmd {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	// Keep multiplexer context so commands in the shell can address their pane.
	cmd := exec.Command(shell, "-l")
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd
}

func (t terminal) currentTarget() (string, string, error) {
	if t.backend == "tmux" {
		pane := os.Getenv("TMUX_PANE")
		if pane == "" {
			return "", "", fmt.Errorf("missing tmux pane context")
		}
		out, err := runOutput(t.command("display-message", "-p", "-t", pane, "#{window_name}"))
		return strings.TrimSpace(out), pane, err
	}
	pane := os.Getenv("ZELLIJ_PANE_ID")
	if pane == "" {
		return "", "", fmt.Errorf("missing Zellij pane context")
	}
	out, err := runOutput(t.action("list-panes", "--json", "--tab"))
	if err != nil {
		return "", "", err
	}
	var panes []struct {
		ID       int    `json:"id"`
		PaneID   int    `json:"pane_id"`
		TabName  string `json:"tab_name"`
		IsPlugin bool   `json:"is_plugin"`
	}
	if err = json.Unmarshal([]byte(out), &panes); err != nil {
		return "", "", err
	}
	for _, p := range panes {
		if !p.IsPlugin && (strconv.Itoa(p.ID) == pane) {
			return p.TabName, pane, nil
		}
	}
	return "", "", fmt.Errorf("Zellij pane %s not found", pane)
}
func (t terminal) shell(cfg Config) error {
	var target, pane string
	var err error
	for i := 0; i < 20; i++ {
		target, pane, err = t.currentTarget()
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	if target == "local" {
		t.namePane(pane, "Local")
		return localTerminalShell().Run()
	}
	if _, err := cfg.host(target); err != nil {
		t.namePane(pane, "Boxes")
		fmt.Print("Tailmux boxes\n\nAlt+B: search boxes (Zellij: Ctrl+B or F2). Each host opens in its own window/tab.\nNew panes inside a host connect to that host.\n")
		reader := bufio.NewReader(os.Stdin)
		for {
			fmt.Print("Press Enter to pick a box: ")
			if _, err := reader.ReadString('\n'); err != nil {
				return nil
			}
			if err := t.pick(cfg); err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		}
	}
	t.namePane(pane, target)
	return t.remote(cfg, target, "pane-"+terminalID(t.dir+t.backend+target+pane))
}
func (t terminal) remote(cfg Config, target, id string) error {
	h, err := cfg.host(target)
	if err != nil {
		return err
	}
	// Separate remote server: no changes to existing tmux sessions or configuration.
	remote := "tmux -L tailmux -f /dev/null new-session -Ad -s " + shellQuote(id) + " && tmux -L tailmux set-option -g status off && tmux -L tailmux set-option -g prefix C-a && tmux -L tailmux bind-key C-a send-prefix && exec tmux -L tailmux attach-session -t " + shellQuote(id)
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Fprintf(os.Stdout, "Connecting to %s (remote tmux)…\n", target)
		args := append(sshOptions(t.exe, t.dir, target, h), "-t", h.Address, remote)
		cmd := exec.Command("ssh", args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		fmt.Print("Disconnected. Enter reconnects; q closes this pane: ")
		line, err := reader.ReadString('\n')
		if err != nil || strings.TrimSpace(line) == "q" {
			return nil
		}
	}
}
