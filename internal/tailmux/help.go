package tailmux

import (
	"fmt"
	"strings"
)

const forwardUsage = `Usage: tailmux forward <host> <ports...> [--name NAME] [--no-rewrite] [--json]

Ports: a single port, inclusive range, comma-separated list, or local:remote.
At most 100 ports per group. Options follow the host; ports may surround options.

  --name NAME     Serve HTTP by hostname; multiple names can share a local port
  --no-rewrite    Disable redirect, JSON URL, cookie, Origin and Referer rewriting
  --json          Print the created forward as JSON, including its ID

Examples:
  tailmux forward lab/worker 3000
  tailmux forward lab/worker 3000-3005 --name worker.localhost
  tailmux forward lab/worker 8080:3000,9090:9000
  tailmux forward lab/worker 3000 --name worker.local

Without --name: raw TCP to remote loopback, preserving response bytes.
With --name: HTTP proxy with localhost redirect rewriting enabled by default.
.localhost works in modern browsers; custom names (including .local) need DNS
or a hosts-file entry pointing to 127.0.0.1. All listeners bind to 127.0.0.1.
OAuth callback allowlists and HTTPS requirements may still need app configuration.

Forwards persist after this command exits, until stopped or the daemon exits.
Use tailmux forwards [--json] to list IDs; tailmux unforward <id> to stop one.
`
const terminalUsage = `Usage: tailmux terminal [--backend tmux|zellij] [host]
       tailmux terminal --default tmux|zellij

  --backend NAME  Override the backend for this launch; put flags before the host
  --default NAME  Save the backend preference without launching

Uses the saved default, otherwise tmux. Requires fzf and the selected backend
locally, plus tmux on each remote host for persistent shells.

Examples:
  tailmux terminal
  tailmux terminal --backend zellij lab/worker
  tailmux terminal --default tmux

Alt+B opens the box picker in either backend; tmux also supports Ctrl+B, B.
Zellij also supports Ctrl+B in normal mode, or F2 when unlocked.
Splits in a host tab/window open independent persistent shells on that host.
Detach: Ctrl+B, D (tmux), or Ctrl+O, D (Zellij).
`

func printCommandHelp(command string) error {
	switch command {
	case "orca":
		fmt.Print(orcaUsage)
		return nil
	case "attach":
		fmt.Println("Usage: tailmux herdr attach <host> [session]\n\nAttach to remote Herdr; session defaults to agents.")
		return nil
	case "sessions":
		fmt.Println("Usage: tailmux herdr sessions <host|--all> [--json]\n\nList remote Herdr sessions as a readable table. --json preserves machine-readable output.\n--all queries saved hosts only. This does not list local tmux/Zellij tabs.")
		return nil

	case "forward":
		fmt.Print(forwardUsage)
		return nil
	case "terminal":
		fmt.Print(terminalUsage)
		return nil
	}
	var lines []string
	for _, line := range strings.Split(usage, "\n") {
		if strings.HasPrefix(line, "  tailmux "+command+" ") {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return fmt.Errorf("unknown command %q; run tailmux help", command)
	}
	fmt.Println(strings.Join(lines, "\n"))
	return nil
}
