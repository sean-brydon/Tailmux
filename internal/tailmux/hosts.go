package tailmux

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

func configureHost(dir string, cfg Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: tailmux hosts add <profile/host> [--user USER] [--address DNS] [--port PORT]")
	}
	target := args[0]
	if !strings.Contains(target, "/") {
		return fmt.Errorf("use a qualified target: <profile>/<hostname>")
	}
	h, err := cfg.host(target)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("hosts add", flag.ContinueOnError)
	fs.StringVar(&h.User, "user", h.User, "SSH username")
	fs.StringVar(&h.Address, "address", h.Address, "DNS name or IP")
	fs.IntVar(&h.Port, "port", h.Port, "SSH port")
	if err = fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected host arguments: %v", fs.Args())
	}
	err = updateConfig(dir, func(c *Config) error {
		c.Hosts[target] = h
		// Normalize a legacy short alias without discarding its configured settings.
		_, alias, _ := strings.Cut(target, "/")
		if old, ok := c.Hosts[alias]; ok && old.Profile == h.Profile {
			delete(c.Hosts, alias)
		}
		return nil
	})
	if err == nil {
		fmt.Println("Saved", target)
	}
	return err
}
func discover(dir, profile string) ([]discoveredHost, error) {
	if err := ensureDaemon(dir); err != nil {
		return nil, err
	}
	c, err := connect(dir)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	if err = json.NewEncoder(c).Encode(request{Op: "hosts", Target: profile}); err != nil {
		return nil, err
	}
	var res response
	if err = readLine(bufio.NewReader(c), &res); err != nil {
		return nil, err
	}
	if res.Kind == "error" {
		return nil, errors.New(res.Message)
	}
	if res.Kind != "inventory" {
		return nil, fmt.Errorf("unexpected daemon response: %s", res.Kind)
	}
	return res.Hosts, nil
}
func listHosts(dir string, cfg Config) error {
	if len(cfg.Profiles) == 0 {
		fmt.Println("No profiles yet. Run tailmux login <profile>.")
		return nil
	}
	type result struct {
		profile string
		hosts   []discoveredHost
		err     error
	}
	ch := make(chan result, len(cfg.Profiles))
	for _, profile := range cfg.Profiles {
		go func(p string) { h, e := discover(dir, p); ch <- result{p, h, e} }(profile)
	}
	hosts := []discoveredHost{}
	var errs []error
	for range cfg.Profiles {
		r := <-ch
		if r.err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", r.profile, r.err))
		} else {
			hosts = append(hosts, r.hosts...)
		}
	}
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Target < hosts[j].Target })
	fmt.Printf("%-36s %-8s %s\n", "HOST", "ONLINE", "ADDRESS")
	for _, h := range hosts {
		fmt.Printf("%-36s %-8t %s\n", h.Target, h.Online, h.Address)
	}
	return errors.Join(errs...)
}
func listSessions(dir string, cfg Config, names []string, asJSON, all bool) error {
	if len(names) == 0 {
		return fmt.Errorf("no saved hosts; use hosts add <profile/host> first")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	type result struct {
		Sessions json.RawMessage `json:"sessions,omitempty"`
		Error    string          `json:"error,omitempty"`
	}
	results := map[string]result{}
	failed := false
	for _, name := range names {
		h, err := cfg.host(name)
		if err != nil {
			return err
		}
		args := sshOptions(exe, dir, name, h)
		args = append(args, "-o", "BatchMode=yes", "-o", "ConnectTimeout=30", h.Address, "herdr session list --json")
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		cmd := exec.CommandContext(ctx, "ssh", args...)
		cmd.Stderr = os.Stderr
		out, e := cmd.Output()
		cancel()
		if e != nil {
			results[name] = result{Error: e.Error()}
			failed = true
			continue
		}
		if !json.Valid(out) {
			results[name] = result{Error: "Herdr did not return valid JSON"}
			failed = true
			continue
		}
		results[name] = result{Sessions: out}
	}
	if asJSON {
		if all {
			err = json.NewEncoder(os.Stdout).Encode(results)
		} else if result := results[names[0]]; result.Error == "" {
			err = json.NewEncoder(os.Stdout).Encode(result.Sessions)
		} else {
			err = fmt.Errorf("%s", result.Error)
		}
		if err != nil {
			return err
		}
	} else {
		fmt.Fprintln(os.Stdout, "Remote Herdr sessions (not local terminal tabs)")
		table := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(table, "HOST\tSESSION\tSTATE")
		for _, name := range names {
			r := results[name]
			if r.Error != "" {
				fmt.Fprintf(table, "%s\t—\tERROR: %s\n", name, r.Error)
				continue
			}
			if e := writeSessionRows(table, name, r.Sessions); e != nil {
				failed = true
				fmt.Fprintf(table, "%s\t—\tERROR: %v\n", name, e)
			}
		}
		if err = table.Flush(); err != nil {
			return err
		}
	}
	if failed {
		return fmt.Errorf("some hosts failed; see per-host errors")
	}
	return nil
}

func sessionsCLI(dir string, cfg Config, args []string) error {
	asJSON := false
	target := ""
	for _, a := range args {
		if a == "--json" {
			asJSON = true
		} else if target == "" {
			target = a
		} else {
			return fmt.Errorf("usage: tailmux herdr sessions <host|--all> [--json]")
		}
	}
	if target == "" {
		return fmt.Errorf("usage: tailmux herdr sessions <host|--all> [--json]")
	}
	names := hostNames(cfg)
	if target != "--all" {
		if _, e := cfg.host(target); e != nil {
			return e
		}
		names = []string{target}
	}
	return listSessions(dir, cfg, names, asJSON, target == "--all")
}
func writeSessionRows(w io.Writer, host string, raw json.RawMessage) error {
	var result struct {
		Sessions []struct {
			Name    string `json:"name"`
			Running bool   `json:"running"`
		} `json:"sessions"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return fmt.Errorf("invalid Herdr session list: %w", err)
	}
	if result.Sessions == nil {
		return fmt.Errorf("Herdr response has no sessions array")
	}
	if len(result.Sessions) == 0 {
		_, err := fmt.Fprintf(w, "%s\t—\tnone\n", host)
		return err
	}
	for _, s := range result.Sessions {
		state := "stopped"
		if s.Running {
			state = "running"
		}
		if _, err := fmt.Fprintf(w, "%s\t%s\t%s\n", host, s.Name, state); err != nil {
			return err
		}
	}
	return nil
}
