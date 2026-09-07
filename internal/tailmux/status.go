package tailmux

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"text/tabwriter"
	"time"
)

type statusBox struct {
	Target  string `json:"target"`
	Address string `json:"address,omitempty"`
	State   string `json:"state"`
	Saved   bool   `json:"saved"`
}
type statusRuntime struct {
	Target    string `json:"target"`
	LocalPort int    `json:"local_port"`
	State     string `json:"state"`
}
type statusSnapshot struct {
	Loopbacks    loopbackConfig  `json:"loopbacks"`
	Daemon       bool            `json:"daemon_running"`
	Boxes        []statusBox     `json:"boxes"`
	Forwards     []ForwardInfo   `json:"forwards"`
	Runtimes     []statusRuntime `json:"orca"`
	Dependencies map[string]bool `json:"dependencies"`
	Errors       []string        `json:"errors,omitempty"`
}

// Inspect only: opening the dashboard does not start networking or restore tunnels.
func statusRequest(dir string, req request) (response, error) {
	var res response
	c, err := net.DialTimeout("unix", socketPath(dir), time.Second)
	if err != nil {
		return res, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if err = json.NewEncoder(c).Encode(req); err != nil {
		return res, err
	}
	err = readLine(bufio.NewReader(c), &res)
	if err == nil && res.Kind == "error" {
		err = fmt.Errorf("%s", res.Message)
	}
	return res, err
}
func collectStatus(dir string, cfg Config) statusSnapshot {
	s := statusSnapshot{Boxes: []statusBox{{Target: "local", State: "this machine"}}, Forwards: []ForwardInfo{}, Runtimes: []statusRuntime{}, Dependencies: map[string]bool{}}
	if bindings, err := readLoopbacks(dir); err != nil {
		s.Errors = append(s.Errors, "Loopback settings: "+err.Error())
	} else {
		s.Loopbacks = bindings
	}
	for _, tool := range []string{"ssh", "tmux", "zellij", "fzf", "cloudflared", "ngrok", orcaExecutable()} {
		_, err := exec.LookPath(tool)
		s.Dependencies[tool] = err == nil
	}
	boxes := map[string]statusBox{}
	for _, name := range hostNames(cfg) {
		target, err := canonicalTarget(cfg, name)
		if err == nil {
			h, _ := cfg.host(name)
			boxes[target] = statusBox{Target: target, Address: h.Address, State: "not checked", Saved: true}
		}
	}
	c, err := connect(dir)
	if err == nil {
		s.Daemon = true
		c.Close()
	}
	if s.Daemon {
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, profile := range cfg.Profiles {
			wg.Add(1)
			go func(p string) {
				defer wg.Done()
				res, err := statusRequest(dir, request{Op: "hosts", Target: p})
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					s.Errors = append(s.Errors, p+": "+err.Error())
					return
				}
				for _, h := range res.Hosts {
					state := "offline"
					if h.Online {
						state = "online"
					}
					address := h.Address
					if boxes[h.Target].Saved {
						address = boxes[h.Target].Address
					}
					boxes[h.Target] = statusBox{Target: h.Target, Address: address, State: state, Saved: boxes[h.Target].Saved}
				}
			}(profile)
		}
		wg.Wait()
		res, err := statusRequest(dir, request{Op: "forwards"})
		if err != nil {
			s.Errors = append(s.Errors, err.Error())
		} else if res.Forwards != nil {
			s.Forwards = res.Forwards
		}
	}
	if !s.Daemon {
		b, err := os.ReadFile(filepath.Join(dir, savedForwardsFile))
		if err == nil {
			var specs []ForwardSpec
			if err = json.Unmarshal(b, &specs); err != nil {
				s.Errors = append(s.Errors, "Cannot read saved forwards: "+err.Error())
			} else {
				for _, spec := range specs {
					s.Forwards = append(s.Forwards, ForwardInfo{ID: spec.Save, Spec: spec, State: "stopped"})
				}
			}
		} else if !os.IsNotExist(err) {
			s.Errors = append(s.Errors, "Cannot read saved forwards: "+err.Error())
		}
	}

	names := make([]string, 0, len(boxes))
	for name := range boxes {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := boxes[names[i]], boxes[names[j]]
		if a.Saved != b.Saved {
			return a.Saved
		}
		if (a.State == "online") != (b.State == "online") {
			return a.State == "online"
		}
		return a.Target < b.Target
	})
	for _, name := range names {
		s.Boxes = append(s.Boxes, boxes[name])
	}
	routes, err := readOrcaRoutes(dir)
	if err != nil {
		s.Errors = append(s.Errors, err.Error())
	}
	for target, r := range routes {
		state := "paired · tunnel stopped"
		for _, f := range s.Forwards {
			for _, p := range f.Spec.Ports {
				if f.Spec.Target == target && p.Local == r.LocalPort && f.State == "running" {
					state = "paired · tunnel running (runtime unchecked)"
				}
			}
		}
		s.Runtimes = append(s.Runtimes, statusRuntime{Target: target, LocalPort: r.LocalPort, State: state})
	}
	sort.Slice(s.Runtimes, func(i, j int) bool { return s.Runtimes[i].Target < s.Runtimes[j].Target })
	sort.Strings(s.Errors)
	return s
}
func statusCLI(dir string, cfg Config, args []string) error {
	if len(args) > 1 || len(args) == 1 && args[0] != "--json" {
		return fmt.Errorf("usage: tailmux status [--json]")
	}
	s := collectStatus(dir, cfg)
	if len(args) == 1 {
		return json.NewEncoder(os.Stdout).Encode(s)
	}
	fmt.Fprintf(os.Stdout, "Tailmux · daemon running: %t\n", s.Daemon)
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "BOX\tSTATE\tADDRESS")
	for _, b := range s.Boxes {
		fmt.Fprintf(w, "%s\t%s\t%s\n", b.Target, b.State, b.Address)
	}
	fmt.Fprintln(w, "\nFORWARD\tSTATE\tTARGET")
	for _, f := range s.Forwards {
		fmt.Fprintf(w, "%s\t%s\t%s %s\n", f.ID, f.State, f.Spec.Target, forwardSummary(f))
	}
	fmt.Fprintln(w, "\nORCA\tSTATE\tLOCAL PORT")
	for _, r := range s.Runtimes {
		fmt.Fprintf(w, "%s\t%s\t%d\n", r.Target, r.State, r.LocalPort)
	}
	w.Flush()
	for _, e := range s.Errors {
		fmt.Fprintln(os.Stdout, "Check:", e)
	}
	return nil
}
func forwardSummary(f ForwardInfo) string {
	ports := []string{}
	for _, p := range f.Spec.Ports {
		ports = append(ports, fmt.Sprintf("%d → %d", p.Local, p.Remote))
	}
	result := strings.Join(ports, ", ")
	if f.Spec.Name != "" {
		result = f.Spec.Name + " · " + result
	}
	if f.Spec.Public != nil {
		result += " · " + f.Spec.Public.URL + " (" + f.PublicState + ")"
	}
	return result
}
