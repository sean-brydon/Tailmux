package tailmux

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// ListeningPort is a TCP listener discovered on a remote host. Process details
// may be empty when the remote user cannot inspect the owning process.
type ListeningPort struct {
	Port    int    `json:"port"`
	Address string `json:"address"`
	Process string `json:"process,omitempty"`
}

var remoteCommandOutput = func(ctx context.Context, args []string) ([]byte, error) {
	return exec.CommandContext(ctx, "ssh", args...).Output()
}

func discoverPorts(dir, target string, h Host) ([]ListeningPort, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	remote := `if command -v ss >/dev/null 2>&1; then printf 'tailmux:ss\n'; ss -H -ltnp; elif command -v lsof >/dev/null 2>&1; then printf 'tailmux:lsof\n'; lsof -nP -iTCP -sTCP:LISTEN; else printf 'tailmux:none\n'; exit 127; fi`
	args := append(sshOptions(exe, dir, target, h), "-o", "BatchMode=yes", "-o", "ConnectTimeout=30", h.Address, remote)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	out, err := remoteCommandOutput(ctx, args)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("inspect %s: timed out", target)
		}
		return nil, fmt.Errorf("inspect %s: %w", target, err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || lines[0] == "tailmux:none" {
		return nil, fmt.Errorf("%s has neither ss nor lsof; install iproute2 or lsof", target)
	}
	var ports []ListeningPort
	switch lines[0] {
	case "tailmux:ss":
		ports = parseSSListeners(lines[1:])
	case "tailmux:lsof":
		ports = parseLsofListeners(lines[1:])
	default:
		return nil, fmt.Errorf("unexpected listener response from %s", target)
	}
	sort.Slice(ports, func(i, j int) bool {
		if ports[i].Port != ports[j].Port {
			return ports[i].Port < ports[j].Port
		}
		return ports[i].Address < ports[j].Address
	})
	return ports, nil
}

func splitListenAddress(s string) (string, int, bool) {
	i := strings.LastIndexByte(s, ':')
	if i < 0 || i == len(s)-1 {
		return "", 0, false
	}
	p, err := strconv.Atoi(s[i+1:])
	if err != nil || p < 1 || p > 65535 {
		return "", 0, false
	}
	a := strings.Trim(strings.TrimSpace(s[:i]), "[]")
	return a, p, true
}

func parseSSListeners(lines []string) []ListeningPort {
	seen := map[string]bool{}
	var out []ListeningPort
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		address, port, ok := splitListenAddress(fields[3])
		if !ok {
			continue
		}
		process := ""
		if len(fields) > 5 {
			process = strings.Join(fields[5:], " ")
			process = strings.TrimPrefix(process, "users:((\"")
			if i := strings.Index(process, "\""); i >= 0 {
				process = process[:i]
			}
		}
		key := fmt.Sprintf("%d\x00%s\x00%s", port, address, process)
		if !seen[key] {
			seen[key] = true
			out = append(out, ListeningPort{Port: port, Address: address, Process: process})
		}
	}
	return out
}

func parseLsofListeners(lines []string) []ListeningPort {
	var out []ListeningPort
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 9 || fields[0] == "COMMAND" {
			continue
		}
		address, port, ok := splitListenAddress(fields[len(fields)-2])
		if !ok {
			continue
		}
		out = append(out, ListeningPort{Port: port, Address: address, Process: fields[0]})
	}
	return out
}

func writePorts(w io.Writer, target string, ports []ListeningPort) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tPORT\tBIND\tPROCESS")
	for _, p := range ports {
		process := p.Process
		if process == "" {
			process = "—"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", cleanDisplay(target), p.Port, cleanDisplay(p.Address), cleanDisplay(process))
	}
	return tw.Flush()
}

func cleanDisplay(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
}

// flag accepts flags only before positional arguments. Tailmux documents the
// more natural "target --flag" form, so move these boolean flags up front.
func boolFlagsFirst(args []string, allowed map[string]bool) ([]string, error) {
	flags, positional := []string{}, []string{}
	for _, arg := range args {
		if strings.HasPrefix(arg, "-") {
			isFlag, ok := allowed[arg]
			if !ok {
				return nil, fmt.Errorf("unknown option %s", arg)
			}
			if isFlag {
				flags = append(flags, arg)
			} else {
				positional = append(positional, arg)
			}
		} else {
			positional = append(positional, arg)
		}
	}
	return append(flags, positional...), nil
}

func pickPort(ports []ListeningPort) (int, error) {
	if len(ports) == 0 {
		return 0, errors.New("no listening TCP ports found")
	}
	var input bytes.Buffer
	seen := map[int]bool{}
	for _, p := range ports {
		if seen[p.Port] {
			continue
		}
		seen[p.Port] = true
		fmt.Fprintf(&input, "%d\t%s\t%s\n", p.Port, cleanDisplay(p.Process), cleanDisplay(p.Address))
	}
	cmd := exec.Command("fzf", "--prompt=Search ports > ", "--header=Enter: select   /   Esc: cancel", "--layout=reverse", "--border=rounded", "--border-label= Tailmux · Ports ", "--delimiter=\t", "--with-nth=1,2,3")
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "FZF_DEFAULT_") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Stdin = &input
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && (exit.ExitCode() == 1 || exit.ExitCode() == 130) {
			return 0, nil
		}
		return 0, fmt.Errorf("port picker: %w", err)
	}
	field := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)[0]
	port, err := strconv.Atoi(field)
	if err != nil {
		return 0, fmt.Errorf("invalid picker selection %q", field)
	}
	if !seen[port] {
		return 0, fmt.Errorf("picker returned a port outside the discovered inventory")
	}
	return port, nil
}

// portsCLI lists remote listeners, opens an interactive picker, or forwards a
// picked port. --forward implies --pick.
func portsCLI(dir string, cfg Config, args []string) error {
	fs := flag.NewFlagSet("ports", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print JSON")
	pick := fs.Bool("pick", false, "select a port interactively")
	forward := fs.Bool("forward", false, "select and forward a port")
	ordered, err := boolFlagsFirst(args, map[string]bool{"--json": true, "--pick": true, "--forward": true})
	if err != nil {
		return err
	}
	if err := fs.Parse(ordered); err != nil {
		return err
	}
	if fs.NArg() != 1 || (*asJSON && (*pick || *forward)) {
		return fmt.Errorf("usage: tailmux ports <host> [--json|--pick|--forward]")
	}
	target := fs.Arg(0)
	h, err := cfg.host(target)
	if err != nil {
		return err
	}
	ports, err := discoverPorts(dir, target, h)
	if err != nil {
		return err
	}
	if *pick || *forward {
		port, err := pickPort(ports)
		if err != nil {
			return err
		}
		if port == 0 { // picker cancelled
			return nil
		}
		if *forward {
			return forwardCLI(dir, []string{"forward", target, strconv.Itoa(port)})
		}
		fmt.Println(port)
		return nil
	}
	if *asJSON {
		if ports == nil {
			ports = []ListeningPort{}
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			Host  string          `json:"host"`
			Ports []ListeningPort `json:"ports"`
		}{target, ports})
	}
	return writePorts(os.Stdout, target, ports)
}
