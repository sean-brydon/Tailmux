package tailmux

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const orcaReadyFile = ".local/share/tailmux/orca/runtime/ready.log"
const orcaUsage = `Usage:
  tailmux orca serve <host> [--ready-file PATH]
  tailmux orca connect <host> [--ready-file PATH]
  tailmux orca status <host>
  tailmux orca exec <host> -- <orca command...>

serve starts the preconfigured remote systemd user service tailmux-orca.service.
It never restarts a running runtime. Install Orca and configure that service first.
connect pairs an already-running orca serve runtime using its private JSON ready log.
The server must advertise ws://127.0.0.1:LOCAL_PORT; use a different local port per box.
The actual server port comes from its bound endpoint, not the advertised port.

status and exec restore the saved tunnel when needed and verify runtime identity.
Examples:
  tailmux orca serve lab/worker
  tailmux orca status lab/worker
  tailmux orca exec lab/worker -- repo list --json
  tailmux orca exec lab/worker -- terminal list --json

Orca stores pairing credentials. Tailmux stores only routing and runtime IDs.
Stopping Tailmux closes local tunnels; it does not stop remote Orca or its agents.
The current Orca serve listener requires a host firewall to restrict non-loopback
access. See the Orca guide before starting a remote service.
`

type orcaRoute struct {
	Environment string `json:"environment_id"`
	RuntimeID   string `json:"runtime_id"`
	LocalPort   int    `json:"local_port"`
	RemotePort  int    `json:"remote_port"`
	ReadyFile   string `json:"ready_file"`
}
type orcaReady struct {
	Type       string `json:"type"`
	Schema     int    `json:"schemaVersion"`
	RuntimeID  string `json:"runtimeId"`
	Bound      string `json:"boundEndpoint"`
	Advertised string `json:"advertisedEndpoint"`
	Pairing    struct {
		Available bool   `json:"available"`
		URL       string `json:"url"`
		Endpoint  string `json:"endpoint"`
	} `json:"pairing"`
}
type orcaEnvironment struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	RuntimeID string `json:"runtimeId"`
	Endpoints []struct {
		Endpoint string `json:"endpoint"`
	} `json:"endpoints"`
}
type orcaReply struct {
	OK     bool `json:"ok"`
	Result struct {
		Environment  orcaEnvironment   `json:"environment"`
		Environments []orcaEnvironment `json:"environments"`
		Runtime      struct {
			ID        string `json:"runtimeId"`
			Reachable bool   `json:"reachable"`
			State     string `json:"state"`
		} `json:"runtime"`
	} `json:"result"`
}

func orcaExecutable() string {
	if value := os.Getenv("ORCA_CLI_COMMAND"); value != "" {
		return value
	}
	if os.Getenv("ORCA_DEV_REPO_ROOT") != "" {
		return "orca-dev"
	}
	if runtime.GOOS == "linux" {
		return "orca-ide"
	}
	return "orca"
}
func orcaCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, orcaExecutable(), args...)
	// Never inherit routing to an unrelated paired runtime.
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "ORCA_ENVIRONMENT=") && !strings.HasPrefix(e, "ORCA_PAIRING_CODE=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	return cmd
}
func orcaQuery(args ...string) (orcaReply, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	b, err := orcaCommand(ctx, append(args, "--json")...).Output()
	// A pairing URL is a credential; never include command argv or raw output in errors.
	if err != nil {
		return orcaReply{}, fmt.Errorf("Orca request failed (%s); check the runtime and installed CLI", args[0])
	}
	var r orcaReply
	if json.Unmarshal(b, &r) != nil || !r.OK {
		return r, fmt.Errorf("Orca returned an unsuccessful or invalid response")
	}
	return r, nil
}
func readOrcaRoutes(dir string) (map[string]orcaRoute, error) {
	routes := map[string]orcaRoute{}
	b, err := os.ReadFile(filepath.Join(dir, "orca.json"))
	if errors.Is(err, os.ErrNotExist) {
		return routes, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &routes); err != nil {
		return nil, err
	}
	if routes == nil {
		routes = map[string]orcaRoute{}
	}
	return routes, nil
}
func saveOrcaRoutes(dir string, routes map[string]orcaRoute) error {
	b, err := json.MarshalIndent(routes, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "orca-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "orca.json"))
}
func parseOrcaReady(b []byte) (orcaReady, orcaRoute, error) {
	var ready orcaReady
	for _, line := range strings.Split(string(b), "\n") {
		var r orcaReady
		if json.Unmarshal([]byte(line), &r) == nil && r.Type == "orca_server_ready" {
			ready = r
		}
	}
	fail := func() (orcaReady, orcaRoute, error) {
		return orcaReady{}, orcaRoute{}, fmt.Errorf("no valid Orca ready record: require schema 1, runtime pairing, and ws://127.0.0.1:LOCAL_PORT advertised endpoint")
	}
	if ready.Schema != 1 || ready.RuntimeID == "" || !ready.Pairing.Available || ready.Pairing.URL == "" {
		return fail()
	}
	local, e := url.Parse(ready.Advertised)
	if e != nil || local.Scheme != "ws" || local.Hostname() != "127.0.0.1" || local.User != nil || local.RawQuery != "" || local.Fragment != "" || (local.Path != "" && local.Path != "/") {
		return fail()
	}
	bound, e := url.Parse(ready.Bound)
	if e != nil || bound.Scheme != "ws" {
		return fail()
	}
	lp, e1 := strconv.Atoi(local.Port())
	rp, e2 := strconv.Atoi(bound.Port())
	if e1 != nil || e2 != nil || lp < 1 || lp > 65535 || rp < 1 || rp > 65535 {
		return fail()
	}
	if ready.Pairing.Endpoint != ready.Advertised {
		return fail()
	}
	return ready, orcaRoute{LocalPort: lp, RemotePort: rp, RuntimeID: ready.RuntimeID}, nil
}
func orcaSSH(dir string, cfg Config, target, command string) ([]byte, error) {
	h, err := cfg.host(target)
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	args := append(sshOptions(exe, dir, target, h), "-o", "BatchMode=yes", h.Address, command)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	b, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("remote Orca setup/read failed on %s; check SSH and tailmux-orca.service", target)
	}
	return b, nil
}
func orcaTunnel(dir, target string, r orcaRoute) (string, bool, error) {
	if r.LocalPort < 1 || r.LocalPort > 65535 || r.RemotePort < 1 || r.RemotePort > 65535 {
		return "", false, fmt.Errorf("invalid saved Orca port mapping")
	}
	forwards, err := forwardRPC(dir, request{Op: "forwards"})
	if err != nil {
		return "", false, err
	}
	for _, f := range forwards {
		if f.State == "running" && f.Spec.Target == target && f.Spec.Name == "" && len(f.Spec.Ports) == 1 && f.Spec.Ports[0] == (PortMap{Local: r.LocalPort, Remote: r.RemotePort}) {
			return f.ID, false, nil
		}
	}
	s := ForwardSpec{Target: target, Ports: []PortMap{{Local: r.LocalPort, Remote: r.RemotePort}}}
	forwards, err = forwardRPC(dir, request{Op: "forward", Forward: &s})
	if err != nil {
		return "", false, err
	}
	if len(forwards) != 1 {
		return "", false, fmt.Errorf("invalid forward response")
	}
	return forwards[0].ID, true, nil
}
func verifyOrca(r orcaRoute) error {
	reply, err := orcaQuery("status", "--environment", r.Environment)
	if err != nil {
		return err
	}
	if !reply.Result.Runtime.Reachable || reply.Result.Runtime.ID != r.RuntimeID {
		return fmt.Errorf("Orca runtime identity did not match the selected host")
	}
	return nil
}
func connectOrca(dir string, cfg Config, target, path string, routes map[string]orcaRoute) (orcaRoute, error) {
	b, err := orcaSSH(dir, cfg, target, "tail -c 1048576 -- "+shellQuote(path))
	if err != nil {
		return orcaRoute{}, err
	}
	ready, route, err := parseOrcaReady(b)
	if err != nil {
		return route, err
	}
	route.ReadyFile = path
	if previous, ok := routes[target]; ok && previous.RuntimeID == route.RuntimeID && previous.LocalPort == route.LocalPort && previous.RemotePort == route.RemotePort {
		route.Environment = previous.Environment
	}
	id, created, err := orcaTunnel(dir, target, route)
	if err != nil {
		return route, err
	}
	success := false
	newEnvironment := ""
	defer func() {
		if !success {
			if created {
				forwardRPC(dir, request{Op: "unforward", Target: id})
			}
			if newEnvironment != "" {
				orcaQuery("environment", "rm", "--environment", newEnvironment)
			}
		}
	}()
	if route.Environment == "" {
		list, err := orcaQuery("environment", "list")
		if err != nil {
			return route, err
		}
		for _, e := range list.Result.Environments {
			if e.Name == target {
				if e.RuntimeID != "" && e.RuntimeID != route.RuntimeID {
					return route, fmt.Errorf("Orca environment %s already belongs to a different runtime", target)
				}
				for _, ep := range e.Endpoints {
					if ep.Endpoint == ready.Advertised {
						route.Environment = e.ID
					}
				}
				if route.Environment == "" {
					return route, fmt.Errorf("Orca environment %s already uses a different endpoint", target)
				}
			}
		}
		if route.Environment == "" {
			r, err := orcaQuery("environment", "add", "--name", target, "--pairing-code", ready.Pairing.URL)
			if err != nil {
				return route, err
			}
			route.Environment = r.Result.Environment.ID
			if route.Environment == "" {
				return route, fmt.Errorf("Orca returned no environment ID")
			}
			newEnvironment = route.Environment
		}
	}
	if err = verifyOrca(route); err != nil {
		return route, err
	}
	routes[target] = route
	if err = saveOrcaRoutes(dir, routes); err != nil {
		return route, err
	}
	success = true
	return route, nil
}
func orcaCLI(dir string, cfg Config, args []string) error {
	if len(args) == 0 || (len(args) == 1 && (args[0] == "--help" || args[0] == "help")) {
		fmt.Print(orcaUsage)
		return nil
	}
	if len(args) < 2 {
		return fmt.Errorf("specify an Orca action and host; run tailmux help orca")
	}
	action := args[0]
	if action != "serve" && action != "connect" && action != "status" && action != "exec" {
		return fmt.Errorf("unknown Orca action %q", action)
	}
	target, err := canonicalTarget(cfg, args[1])
	if err != nil {
		return err
	}
	if _, err = exec.LookPath(orcaExecutable()); err != nil {
		return fmt.Errorf("Orca CLI %q is not installed", orcaExecutable())
	}
	path := ""
	rest := args[2:]
	if action == "serve" || action == "connect" {
		fs := flag.NewFlagSet("orca "+action, flag.ContinueOnError)
		fs.StringVar(&path, "ready-file", "", "remote JSON ready log (relative to SSH home or absolute)")
		if err = fs.Parse(rest); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("unexpected Orca connection arguments")
		}
	}
	if action == "status" && len(rest) != 0 {
		return fmt.Errorf("usage: tailmux orca status <host>")
	}
	if action == "exec" {
		if len(rest) > 0 && rest[0] == "--" {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return fmt.Errorf("usage: tailmux orca exec <host> -- <orca command...>")
		}
		for _, a := range rest {
			if a == "--environment" || strings.HasPrefix(a, "--environment=") || a == "--pairing-code" || strings.HasPrefix(a, "--pairing-code=") {
				return fmt.Errorf("Orca routing is selected by the Tailmux host")
			}
		}
	}
	lock, err := os.OpenFile(filepath.Join(dir, "orca.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	routes, err := readOrcaRoutes(dir)
	if err != nil {
		return err
	}
	route, known := routes[target]
	if action == "serve" {
		if _, err = orcaSSH(dir, cfg, target, "systemctl --user start tailmux-orca.service"); err != nil {
			return err
		}
	}
	if action == "serve" || action == "connect" {
		if path == "" {
			path = route.ReadyFile
		}
		if path == "" {
			path = orcaReadyFile
		}
		// A first service start may take a few seconds to publish readiness.
		for i := 0; i < 15; i++ {
			route, err = connectOrca(dir, cfg, target, path, routes)
			if err == nil || action != "serve" {
				break
			}
			time.Sleep(time.Second)
		}
		if err != nil {
			return err
		}
	} else {
		if !known {
			return fmt.Errorf("no Orca route for %s; run tailmux orca connect %s first", target, target)
		}
		id, created, e := orcaTunnel(dir, target, route)
		if e != nil {
			return e
		}
		if err = verifyOrca(route); err != nil {
			if created {
				forwardRPC(dir, request{Op: "unforward", Target: id})
			}
			return err
		}
	}
	syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if action == "exec" {
		cmd := orcaCommand(context.Background(), append(rest, "--environment", route.Environment)...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	fmt.Printf("%s: Orca ready via localhost:%d → remote localhost:%d\n", target, route.LocalPort, route.RemotePort)
	return nil
}
