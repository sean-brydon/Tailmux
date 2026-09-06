package tailmux

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const herdrSession = "tailmux"

type herdrWorkspace struct {
	ID    string `json:"workspace_id"`
	Label string `json:"label"`
}
type herdrPane struct {
	ID        string `json:"pane_id"`
	Workspace string `json:"workspace_id"`
}
type herdrResult struct {
	Result struct {
		Workspaces  []herdrWorkspace `json:"workspaces"`
		Panes       []herdrPane      `json:"panes"`
		RootPane    herdrPane        `json:"root_pane"`
		ProcessInfo struct {
			ShellPID  int `json:"shell_pid"`
			Processes []struct {
				PID int `json:"pid"`
			} `json:"foreground_processes"`
		} `json:"process_info"`
	} `json:"result"`
}

func herdrCommand(args ...string) *exec.Cmd {
	cmd := exec.Command("herdr", append([]string{"--session", herdrSession}, args...)...)
	// Never let an enclosing Herdr pane redirect control to its own session.
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "HERDR_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	return cmd
}
func herdrQuery(args ...string) (herdrResult, error) {
	var result herdrResult
	out, err := herdrCommand(args...).CombinedOutput()
	if err != nil {
		return result, fmt.Errorf("herdr %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	if len(strings.TrimSpace(string(out))) == 0 && len(args) >= 2 && args[0] == "pane" && args[1] == "run" {
		return result, nil
	}
	if err = json.Unmarshal(out, &result); err != nil {
		return result, fmt.Errorf("invalid Herdr response: %w", err)
	}
	return result, nil
}
func selectHerdrHosts(cfg Config, args []string) ([]string, error) {
	if len(args) == 0 {
		args = hostNames(cfg)
	}
	if len(args) == 0 {
		return nil, fmt.Errorf("no saved hosts; use tailmux hosts add <profile/host> or specify hosts")
	}
	seen := map[string]bool{}
	names := []string{}
	for _, name := range args {
		h, err := cfg.host(name)
		if err != nil {
			return nil, err
		}
		if !strings.Contains(name, "/") {
			name = h.Profile + "/" + name
		}
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	return names, nil
}
func startHerdr(dir string) error {
	if _, err := herdrQuery("workspace", "list"); err == nil {
		return nil
	}
	log, err := os.OpenFile(filepath.Join(dir, "herdr.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := herdrCommand("server")
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err = cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	for i := 0; i < 100; i++ {
		if _, err := herdrQuery("workspace", "list"); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("Herdr did not start; see %s", log.Name())
}
func openHerdr(dir string, cfg Config, targets []string) error {
	names, err := selectHerdrHosts(cfg, targets)
	if err != nil {
		return err
	}
	if _, err = exec.LookPath("herdr"); err != nil {
		return fmt.Errorf("install Herdr locally first: https://herdr.dev/docs/install/")
	}
	// Serialize creation across concurrent opens; release before the interactive attach.
	lock, err := os.OpenFile(filepath.Join(dir, "herdr.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if err = startHerdr(dir); err != nil {
		return err
	}
	state, err := herdrQuery("workspace", "list")
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	firstWorkspace := ""
	for _, name := range names {
		workspaceID := ""
		for _, w := range state.Result.Workspaces {
			if w.Label == name {
				workspaceID = w.ID
				break
			}
		}
		paneID := ""
		if workspaceID == "" {
			created, err := herdrQuery("workspace", "create", "--label", name, "--no-focus")
			if err != nil {
				return err
			}
			paneID = created.Result.RootPane.ID
			workspaceID = created.Result.RootPane.Workspace
			if paneID == "" {
				return fmt.Errorf("Herdr returned no root pane for %s", name)
			}
		} else {
			if firstWorkspace == "" {
				firstWorkspace = workspaceID
			}
			panes, err := herdrQuery("pane", "list")
			if err != nil {
				return err
			}
			for _, p := range panes.Result.Panes {
				if p.Workspace == workspaceID {
					paneID = p.ID
					break
				}
			}
			if paneID == "" {
				return fmt.Errorf("workspace %s has no pane", name)
			}
			info, err := herdrQuery("pane", "process-info", "--pane", paneID)
			if err != nil {
				return err
			}
			p := info.Result.ProcessInfo
			// Reuse active connections and reconnect prompts; don't send text to running work.
			if p.ShellPID == 0 || len(p.Processes) != 1 || p.Processes[0].PID != p.ShellPID {
				continue
			}
		}
		if firstWorkspace == "" {
			firstWorkspace = workspaceID
		}
		command := "env TAILMUX_HOME=" + shellQuote(dir) + " " + shellQuote(exe) + " herdr connect " + shellQuote(name)
		if _, err = herdrQuery("pane", "run", paneID, command); err != nil {
			return err
		}
	}
	if firstWorkspace != "" {
		if _, err = herdrQuery("workspace", "focus", firstWorkspace); err != nil {
			return err
		}
	}
	syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	fmt.Fprintln(os.Stderr, "Opening local Herdr. Ctrl+B, W selects a box; Ctrl+B, Q detaches the local view.")
	cmd := herdrCommand()
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
func connectHerdr(host string) error {
	reader := bufio.NewReader(os.Stdin)
	for {
		fmt.Fprintf(os.Stdout, "\nConnecting to %s…\n", host)
		if err := Run([]string{"attach", host}); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		fmt.Fprintf(os.Stdout, "\n%s disconnected. Press Enter to reconnect, or type q to return to the local shell: ", host)
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil
		}
		if strings.TrimSpace(line) == "q" {
			return nil
		}
	}
}
