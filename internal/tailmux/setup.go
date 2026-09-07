package tailmux

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

type SetupCheck struct {
	Host     string            `json:"host"`
	OS       string            `json:"os,omitempty"`
	Tools    map[string]string `json:"tools"`
	Warnings []string          `json:"warnings,omitempty"`
	Error    string            `json:"error,omitempty"`
}

func checkHostSetup(dir, target string, h Host) SetupCheck {
	r := SetupCheck{Host: target, Tools: map[string]string{}}
	exe, err := os.Executable()
	if err != nil {
		r.Error = err.Error()
		return r
	}
	remote := `printf 'os=%s\n' "$(uname -srm 2>/dev/null)"; for x in tmux zellij herdr orca-ide systemctl ss lsof; do if command -v "$x" >/dev/null 2>&1; then printf '%s=found\n' "$x"; else printf '%s=missing\n' "$x"; fi; done`
	args := append(sshOptions(exe, dir, target, h), "-o", "BatchMode=yes", "-o", "ConnectTimeout=30", h.Address, remote)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	out, err := remoteCommandOutput(ctx, args)
	cancel()
	if err != nil {
		r.Error = fmt.Sprintf("SSH check failed: %v. Check connectivity and unlock your SSH agent, then retry", err)
		return r
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if key == "os" {
			r.OS = value
		} else {
			r.Tools[key] = value
		}
	}
	if r.Tools["herdr"] == "missing" {
		r.Warnings = append(r.Warnings, "Herdr commands are unavailable until Herdr is installed")
	}
	if r.Tools["orca-ide"] == "missing" {
		r.Warnings = append(r.Warnings, "Orca remote runtime is optional and must be installed separately")
	}
	if r.Tools["ss"] == "missing" && r.Tools["lsof"] == "missing" {
		r.Warnings = append(r.Warnings, "port discovery needs iproute2 (ss) or lsof")
	}
	return r
}

func writeSetupChecks(w io.Writer, checks []SetupCheck) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tSSH\tTMUX\tZELLIJ\tHERDR\tORCA\tPORTS")
	for _, r := range checks {
		ssh := "ok"
		if r.Error != "" {
			ssh = "error"
		}
		ports := "missing"
		if r.Tools["ss"] == "found" || r.Tools["lsof"] == "found" {
			ports = "ready"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Host, ssh, r.Tools["tmux"], r.Tools["zellij"], r.Tools["herdr"], r.Tools["orca-ide"], ports)
		if r.Error != "" {
			fmt.Fprintf(tw, "  %s\t%s\n", "error", r.Error)
		}
		for _, warning := range r.Warnings {
			fmt.Fprintf(tw, "  %s\t%s\n", "note", warning)
		}
	}
	return tw.Flush()
}

func installRemotePrerequisites(dir, target string, h Host, tmux, ports bool) error {
	if !tmux && !ports {
		return fmt.Errorf("choose at least one package: --tmux or --ports")
	}
	packages := []string{}
	if tmux {
		packages = append(packages, "tmux")
	}
	if ports {
		packages = append(packages, "iproute2")
	}
	pkg := strings.Join(packages, " ")
	aptPackages := pkg
	rpmPackages := pkg
	if ports {
		rpmPackages = strings.ReplaceAll(rpmPackages, "iproute2", "iproute")
	}
	remote := `set -eu; if command -v apt-get >/dev/null 2>&1; then sudo apt-get update && sudo apt-get install -y ` + aptPackages + `; elif command -v dnf >/dev/null 2>&1; then sudo dnf install -y ` + rpmPackages + `; elif command -v yum >/dev/null 2>&1; then sudo yum install -y ` + rpmPackages + `; else echo 'unsupported package manager; install prerequisites manually' >&2; exit 2; fi`
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := append(sshOptions(exe, dir, target, h), "-t", h.Address, remote)
	cmd := exec.Command("ssh", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

func parseOrcaSetupArgs(args []string) (target string, localPort, remotePort int, apply, replace bool, err error) {
	localPort, remotePort = 16768, 6768
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--apply":
			apply = true
		case "--replace":
			replace = true
		case "--local-port", "--remote-port":
			name := args[i]
			i++
			if i == len(args) {
				return "", 0, 0, false, false, fmt.Errorf("%s needs a port", name)
			}
			port, e := strconv.Atoi(args[i])
			if e != nil || port < 1 || port > 65535 {
				return "", 0, 0, false, false, fmt.Errorf("invalid %s %q", name, args[i])
			}
			if name == "--local-port" {
				localPort = port
			} else {
				remotePort = port
			}
		default:
			if strings.HasPrefix(args[i], "-") {
				return "", 0, 0, false, false, fmt.Errorf("unknown option %s", args[i])
			}
			if target != "" {
				return "", 0, 0, false, false, fmt.Errorf("unexpected argument %q", args[i])
			}
			target = args[i]
		}
	}
	if target == "" {
		return "", 0, 0, false, false, fmt.Errorf("usage: tailmux setup orca <host> [--local-port PORT] [--remote-port PORT] [--apply] [--replace]")
	}
	if replace && !apply {
		return "", 0, 0, false, false, fmt.Errorf("--replace requires --apply")
	}
	return target, localPort, remotePort, apply, replace, nil
}

func writeOrcaSetupPlan(w io.Writer, target string, localPort, remotePort int, applied bool) {
	fmt.Fprintf(w, "Orca runtime setup for %s\n", target)
	if applied {
		fmt.Fprintln(w, "User service written: ~/.config/systemd/user/tailmux-orca.service")
	} else {
		fmt.Fprintln(w, "Review mode: no remote files or services were changed. Add --apply to write the user service.")
	}
	fmt.Fprintf(w, "Runtime port: %d; local SSH endpoint: 127.0.0.1:%d\n", remotePort, localPort)
	fmt.Fprintln(w, "The service is deliberately disabled and stopped until inbound access is restricted.")
	fmt.Fprintf(w, "Firewall rule to run on the host:\n  sudo iptables -C INPUT ! -i lo -p tcp --dport %d -j REJECT || sudo iptables -I INPUT ! -i lo -p tcp --dport %d -j REJECT\n", remotePort, remotePort)
	fmt.Fprintln(w, "Persist that rule using the host's firewall manager before enabling boot startup.")
	fmt.Fprintln(w, "Then verify the rule and run: systemctl --user start tailmux-orca.service")
}

func applyOrcaService(dir, target string, h Host, localPort, remotePort int, replace bool) error {
	// Ports are validated integers. The remote discovers the executable itself,
	// creates a private runtime directory, and atomically installs one user unit.
	replaceValue := "0"
	if replace {
		replaceValue = "1"
	}
	remote := fmt.Sprintf(`set -eu
orca_bin=$(command -v orca-ide || true)
if [ -z "$orca_bin" ]; then echo 'orca-ide is missing; install Orca first' >&2; exit 2; fi
if systemctl --user is-active --quiet tailmux-orca.service; then echo 'tailmux-orca.service is running; stop it before replacing its configuration' >&2; exit 3; fi
if [ -e "$HOME/.config/systemd/user/tailmux-orca.service" ] && [ %s != 1 ]; then echo 'service already exists; review it and rerun with --apply --replace' >&2; exit 4; fi
mkdir -p "$HOME/.config/systemd/user" "$HOME/.local/share/tailmux/orca/runtime"
chmod 700 "$HOME/.local/share/tailmux/orca/runtime"
unit=$(mktemp "$HOME/.config/systemd/user/.tailmux-orca.XXXXXX")
trap 'rm -f "$unit"' EXIT
printf '%%s\n' '[Unit]' 'Description=Tailmux Orca runtime' '' '[Service]' 'Type=simple' 'WorkingDirectory=%%h' 'Environment=LIBGL_ALWAYS_SOFTWARE=1' "ExecStart=$orca_bin serve --port %d --pairing-address ws://127.0.0.1:%d --json" 'StandardOutput=append:%%h/.local/share/tailmux/orca/runtime/ready.log' 'StandardError=append:%%h/.local/share/tailmux/orca/runtime/error.log' 'UMask=0077' 'KillMode=mixed' '' '[Install]' 'WantedBy=default.target' >"$unit"
chmod 600 "$unit"
if [ -e "$HOME/.config/systemd/user/tailmux-orca.service" ]; then
  backup="$HOME/.config/systemd/user/tailmux-orca.service.bak.$(date +%%Y%%m%%d%%H%%M%%S)"
  cp "$HOME/.config/systemd/user/tailmux-orca.service" "$backup"
  chmod 600 "$backup"
fi
mv "$unit" "$HOME/.config/systemd/user/tailmux-orca.service"
trap - EXIT
systemctl --user daemon-reload
systemctl --user disable tailmux-orca.service >/dev/null 2>&1 || true
printf 'service=written-disabled\n'`, replaceValue, remotePort, localPort)
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := append(sshOptions(exe, dir, target, h), "-o", "BatchMode=yes", "-o", "ConnectTimeout=30", h.Address, remote)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, err := remoteCommandOutput(ctx, args)
	if err != nil {
		return fmt.Errorf("configure Orca service on %s: %w", target, err)
	}
	if !strings.Contains(string(out), "service=written-disabled") {
		return fmt.Errorf("configure Orca service on %s: unexpected response", target)
	}
	return nil
}

// setupCLI checks remote prerequisites or installs explicitly selected system
// packages. Herdr and Orca are reported only because their release/auth setup
// should not be guessed by a generic package installer.
func setupCLI(dir string, cfg Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: tailmux setup <check|install> ...")
	}
	switch args[0] {
	case "check":
		fs := flag.NewFlagSet("setup check", flag.ContinueOnError)
		asJSON := fs.Bool("json", false, "print JSON")
		ordered, err := boolFlagsFirst(args[1:], map[string]bool{"--json": true, "--all": false})
		if err != nil {
			return err
		}
		for i := range ordered {
			if ordered[i] == "--all" {
				ordered[i] = "__tailmux_all_hosts__"
			}
		}
		if err := fs.Parse(ordered); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: tailmux setup check <host|--all> [--json]")
		}
		target := fs.Arg(0)
		names := hostNames(cfg)
		if target != "__tailmux_all_hosts__" {
			if _, err := cfg.host(target); err != nil {
				return err
			}
			names = []string{target}
		}
		if len(names) == 0 {
			return fmt.Errorf("no saved hosts; use hosts add <profile/host> first")
		}
		sort.Strings(names)
		checks := make([]SetupCheck, 0, len(names))
		failed := false
		for _, name := range names {
			h, _ := cfg.host(name)
			r := checkHostSetup(dir, name, h)
			failed = failed || r.Error != ""
			checks = append(checks, r)
		}
		if *asJSON {
			if err := json.NewEncoder(os.Stdout).Encode(checks); err != nil {
				return err
			}
		} else if err := writeSetupChecks(os.Stdout, checks); err != nil {
			return err
		}
		if failed {
			return fmt.Errorf("some hosts failed setup checks")
		}
		return nil
	case "install":
		fs := flag.NewFlagSet("setup install", flag.ContinueOnError)
		tmux := fs.Bool("tmux", false, "install tmux")
		ports := fs.Bool("ports", false, "install port discovery support")
		ordered, err := boolFlagsFirst(args[1:], map[string]bool{"--tmux": true, "--ports": true})
		if err != nil {
			return err
		}
		if err := fs.Parse(ordered); err != nil {
			return err
		}
		if fs.NArg() != 1 {
			return fmt.Errorf("usage: tailmux setup install <host> [--tmux] [--ports]")
		}
		h, err := cfg.host(fs.Arg(0))
		if err != nil {
			return err
		}
		return installRemotePrerequisites(dir, fs.Arg(0), h, *tmux, *ports)
	case "orca":
		target, localPort, remotePort, apply, replace, err := parseOrcaSetupArgs(args[1:])
		if err != nil {
			return err
		}
		h, err := cfg.host(target)
		if err != nil {
			return err
		}
		if apply {
			if err = applyOrcaService(dir, target, h, localPort, remotePort, replace); err != nil {
				return err
			}
		}
		writeOrcaSetupPlan(os.Stdout, target, localPort, remotePort, apply)
		return nil
	default:
		return fmt.Errorf("unknown setup action %q; use check, install, or orca", args[0])
	}
}
