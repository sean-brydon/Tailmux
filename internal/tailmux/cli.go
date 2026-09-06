package tailmux

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const usage = `Tailmux: remote agent sessions across separate Tailscale accounts.

  tailmux init                    Create an empty config (optional)
  tailmux hosts                   Discover machines across connected profiles
  tailmux hosts add <profile/host> [--user USER] [--address DNS] [--port PORT]
  tailmux login <profile>         Create/reuse a profile and authenticate it
  tailmux doctor [host|--all]     Check SSH-port connectivity (default: all)
  tailmux ssh <host> [command...] Open SSH or execute a remote command
  tailmux herdr attach <host> [session] Attach to remote Herdr (default: agents)
  tailmux herdr open [hosts...]   Open one local Herdr view across saved/selected hosts
  tailmux orca serve <host>       Start a configured remote Orca service and connect
  tailmux orca connect <host>     Pair a running Orca runtime over Tailmux
  tailmux orca status <host>      Check the paired runtime and restore its tunnel
  tailmux orca exec <host> -- <command...>   Run an Orca CLI command on that runtime
  tailmux terminal [local|host]        Open tmux or Zellij with a searchable box picker
  tailmux terminal --backend tmux|zellij [local|host]   Override the backend for this launch
  tailmux terminal --default tmux|zellij   Save the default without launching
  tailmux herdr sessions <host|--all> [--json]   List remote Herdr sessions
  tailmux forward <host> <ports...> [--name NAME] [--no-rewrite] [--json]
  tailmux forwards [--json]      List running port forwards
  tailmux unforward <id>         Stop a port forward
  tailmux proxy <host>            Internal SSH stdio transport
  tailmux daemon                  Run the shared tsnet daemon in foreground
  tailmux stop                    Stop local networking; remote agents stay running
  tailmux version                 Show the installed version
  tailmux help [command]          Show help and command examples

TAILMUX_HOME overrides the config/state directory. SSH uses your normal keys,
agent, and config. Targets use <profile>/<hostname>; profile names are yours.
Use hosts add to save SSH overrides and include a host in --all operations.
`

// Version is set by the release build; local source builds report dev.
var Version = "dev"

func Run(args []string) error {
	if len(args) >= 2 && args[0] == "herdr" && (args[1] == "attach" || args[1] == "sessions") {
		return Run(append([]string{args[1]}, args[2:]...))
	}
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Println("tailmux", Version)
		return nil
	}
	if len(args) > 0 && args[0] == "help" {
		if len(args) == 1 {
			fmt.Print(usage)
			return nil
		}
		if len(args) == 2 {
			return printCommandHelp(args[1])
		}
		return fmt.Errorf("usage: tailmux help [command]")
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		return printCommandHelp(args[0])
	}
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Print(usage)
		return nil
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	if args[0] == "init" {
		if len(args) != 1 {
			return fmt.Errorf("usage: tailmux init")
		}
		if err = initConfig(dir); err != nil {
			return err
		}
		fmt.Println("Created", filepath.Join(dir, "config.json"))
		return nil
	}
	if args[0] == "login" {
		if len(args) != 2 {
			return fmt.Errorf("usage: tailmux login <profile>")
		}
		if err = addProfile(dir, args[1]); err != nil {
			return err
		}
		_, _, err = rpc(dir, "login", args[1])
		return err
	}
	if args[0] == "terminal" {
		return terminalCLI(dir, args[1:])
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		return err
	}
	switch args[0] {
	case "forward", "forwards", "unforward":
		return forwardCLI(dir, args)
	case "orca":
		return orcaCLI(dir, cfg, args[1:])
	case "herdr":
		if len(args) >= 2 && args[1] == "open" {
			return openHerdr(dir, cfg, args[2:])
		}
		if len(args) == 3 && args[1] == "connect" {
			if _, err := cfg.host(args[2]); err != nil {
				return err
			}
			return connectHerdr(args[2])
		}
		return fmt.Errorf("usage: tailmux herdr <open|attach|sessions>; run tailmux help herdr")
	case "stop":
		if len(args) != 1 {
			return fmt.Errorf("usage: tailmux stop")
		}
		c, e := connect(dir)
		if e != nil {
			fmt.Println("Tailmux daemon is not running")
			return nil
		}
		c.Close()
		_, _, err = rpc(dir, "stop", "")
		return err
	case "daemon":
		if len(args) != 1 {
			return fmt.Errorf("usage: tailmux daemon")
		}
		return serve(dir)
	case "hosts":
		if len(args) > 1 && args[1] == "add" {
			return configureHost(dir, cfg, args[2:])
		}
		if len(args) != 1 {
			return fmt.Errorf("usage: tailmux hosts [add <profile/host> --user USER --address DNS --port PORT]")
		}
		return listHosts(dir, cfg)
	case "doctor":
		if len(args) > 2 {
			return fmt.Errorf("usage: tailmux doctor [host|--all]")
		}
		names := hostNames(cfg)
		if len(args) == 2 && args[1] != "--all" {
			if _, err = cfg.host(args[1]); err != nil {
				return err
			}
			names = []string{args[1]}
		}
		var errs []error
		if len(names) == 0 {
			return fmt.Errorf("no saved hosts; use doctor <profile/host> or hosts add <profile/host>")
		}
		for _, name := range names {
			_, _, e := rpc(dir, "doctor", name)
			if e != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", name, e)
				errs = append(errs, e)
			}
		}
		if len(errs) > 0 {
			return fmt.Errorf("%d of %d hosts failed connectivity checks", len(errs), len(names))
		}
		return nil
	case "proxy":
		if len(args) != 2 {
			return fmt.Errorf("usage: tailmux proxy <host>")
		}
		if _, err = cfg.host(args[1]); err != nil {
			return err
		}
		c, r, err := rpc(dir, "dial", args[1])
		if err != nil {
			return err
		}
		if c == nil {
			return fmt.Errorf("daemon did not open a stream")
		}
		defer c.Close()
		go func() {
			io.Copy(c, os.Stdin)
			if h, ok := c.(interface{ CloseWrite() error }); ok {
				h.CloseWrite()
			}
		}()
		_, err = io.Copy(os.Stdout, r)
		return err
	case "sessions":
		return sessionsCLI(dir, cfg, args[1:])
	case "ssh", "attach":
		if len(args) < 2 {
			return fmt.Errorf("usage: tailmux %s <host>", args[0])
		}
		h, err := cfg.host(args[1])
		if err != nil {
			return err
		}
		if args[0] == "attach" && len(args) > 3 {
			return fmt.Errorf("usage: tailmux herdr attach <host> [session]")
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		sshArgs := sshOptions(exe, dir, args[1], h)
		switch args[0] {
		case "attach":
			session := "agents"
			if len(args) == 3 {
				session = args[2]
			}
			if !safeName.MatchString(session) {
				return fmt.Errorf("invalid session name %q", session)
			}
			sshArgs = append(sshArgs, "-t", h.Address, "herdr session attach "+shellQuote(session))
		case "ssh":
			sshArgs = append(sshArgs, h.Address)
			if len(args) > 2 {
				parts := make([]string, 0, len(args)-2)
				for _, arg := range args[2:] {
					parts = append(parts, shellQuote(arg))
				}
				sshArgs = append(sshArgs, strings.Join(parts, " "))
			}
		}
		cmd := exec.Command("ssh", sshArgs...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err = cmd.Run(); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return fmt.Errorf("SSH exited with status %d", exit.ExitCode())
			}
			return err
		}
		return nil
	default:
		return fmt.Errorf("unknown command %q; run tailmux help", args[0])
	}
}
func hostNames(c Config) []string {
	names := make([]string, 0, len(c.Hosts))
	for name := range c.Hosts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func sshOptions(exe, dir, name string, h Host) []string {
	// OpenSSH expands percent tokens before invoking the shell.
	proxy := "env TAILMUX_HOME=" + shellQuote(dir) + " " + shellQuote(exe) + " proxy " + shellQuote(name)
	proxy = strings.ReplaceAll(proxy, "%", "%%")
	args := []string{"-o", "ProxyCommand=" + proxy, "-o", "HostKeyAlias=tailmux-" + h.Profile + "-" + h.Address, "-o", "StrictHostKeyChecking=accept-new", "-o", "ControlMaster=no", "-o", "ControlPath=none", "-p", fmt.Sprint(h.Port)}
	if h.User != "" {
		args = append(args, "-l", h.User)
	}
	return args
}
