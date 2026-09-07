package tailmux

import (
	"fmt"
	"strings"
)

const forwardUsage = `Usage: tailmux forward <host> <ports...> [--name NAME] [--save NAME] [--no-rewrite] [--json]
       tailmux forward --resume NAME

Ports: a single port, inclusive range, comma-separated list, or local:remote.
At most 100 ports per group. Options follow the host; ports may surround options.

  --name NAME     Serve HTTP by hostname; multiple names can share a local port
  --no-rewrite    Disable redirect, JSON URL, cookie, Origin and Referer rewriting
  --json          Print the created forward as JSON, including its ID
  --save NAME     Choose a friendly saved name (all groups persist automatically)
  --cloudflare TUNNEL  Manage a locally configured named tunnel; requires --url
  --ngrok         Manage an ngrok endpoint; requires --url
  --url HTTPS_URL Explicit public origin (one port only; provider setup required)

Examples:
  tailmux forward lab/worker 3000
  tailmux loopback setup lab/worker --name worker.test
  tailmux forward lab/worker 3000-3005 --name worker.test
  tailmux forward lab/worker 8080:3000,9090:9000

Without --name: raw TCP to remote loopback, preserving response bytes.
With --name: HTTP proxy with localhost redirect rewriting enabled by default.
Private named routes require tailmux loopback setup first. Each box gets a
dedicated 127.77.x.y address, allowing boxes to use the same port. Browsers
force .localhost to 127.0.0.1, so use .test or another custom name instead.
Raw TCP and public forwards continue to bind to 127.0.0.1.
OAuth callback allowlists and HTTPS requirements may still need app configuration.

Forwards run after this command exits. Saved groups restore on daemon startup;
SSH disconnections retry with capped backoff. --resume retries a saved group.
Use tailmux forwards [--json] to list IDs; tailmux unforward <id|name> stops and
forgets a group, including its managed public connector. stop preserves saved
specs. Provider credentials/DNS setup and OAuth registrations remain separate.
Public mode rewrites direct loopback URLs to --url; it does not rewrite JS/HTML.
`
const terminalUsage = `Usage: tailmux terminal [--backend tmux|zellij] [local|host]
       tailmux terminal --default tmux|zellij

  --backend NAME  Override the backend for this launch; put flags before the host
  --default NAME  Save the backend preference without launching

Uses the saved default, otherwise tmux. Requires fzf and the selected backend
locally, plus tmux on each remote host for persistent shells.

Examples:
  tailmux terminal local
  tailmux terminal --backend zellij lab/worker
  tailmux terminal --default tmux

Alt+B opens the box picker in either backend; tmux also supports Ctrl+B, B.
Zellij also supports Ctrl+B in normal mode, or F2 when unlocked.
Splits in a host tab/window open independent persistent shells on that host.
The local target opens your login shell. Routing survives tab/window renames.
Detach: Ctrl+B, D (tmux), or Ctrl+O, D (Zellij).
`

func printCommandHelp(command string) error {
	switch command {
	case "dashboard":
		fmt.Println("Usage: tailmux dashboard\n\nBare tailmux also opens the dashboard in an interactive terminal.\n0 Monitor, 1–4 panels, Enter terminal, h hide box, H show hidden, a account, e host settings,\nf forward, Shift+L Setup loopback, p ports,\nc host check, i install prerequisites, u Orca setup, n start networking, q quit.")
		return nil
	case "monitor":
		fmt.Println("Usage: tailmux monitor [--json]\n       tailmux monitor claude-setup\n       tailmux monitor claude-statusline\n\nInspect RAM, Orca/Herdr sessions and available Codex/Claude usage on local and saved boxes.\nRead-only; does not start networking. The dashboard opens on 0 Monitor.")
		return nil
	case "status":
		fmt.Println("Usage: tailmux status [--json]\n\nInspect boxes, forwards, saved Orca routes and local tools without starting networking.")
		return nil
	case "loopback":
		fmt.Println("Usage: tailmux loopback setup <host> [--name NAME]\n       tailmux loopback list\n\nAssign a stable dedicated 127.77.x.y loopback address to a canonical box and map\nits private HTTP hostname in /etc/hosts. Setup previews every local change and asks\nApply changes? y/N before sudo. The default name is <host>.test. On macOS, setup installs a launchd job\nto restore the alias automatically at boot. .localhost is rejected because\nbrowsers force it to 127.0.0.1.")
		return nil
	case "setup":
		fmt.Println("Usage: tailmux setup check <host|--all> [--json]\n       tailmux setup install <host> [--tmux] [--ports]\n       tailmux setup orca <host> [--local-port PORT] [--remote-port PORT] [--apply] [--replace]\n\nInstall requires explicit package flags. Orca setup defaults to a review-only plan;\napply writes a stopped, disabled user service. Orca installation and persistent\nfirewall configuration are separate. Replace backs up an existing stopped unit.")
		return nil
	case "ports":
		fmt.Println("Usage: tailmux ports <host> [--json|--pick|--forward]\n\nList remote TCP ports/processes using ss or lsof. Pick prints the selected port;\nforward selects and forwards it to the same local port. Process visibility depends\non the SSH user's permissions. The remote service must accept loopback traffic.")
		return nil
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
