package tailmux

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

type loopbackBinding struct {
	Address string   `json:"address"`
	Names   []string `json:"names"`
}
type loopbackConfig map[string]loopbackBinding

func readLoopbacks(dir string) (loopbackConfig, error) {
	data, err := os.ReadFile(filepath.Join(dir, "loopbacks.json"))
	if os.IsNotExist(err) {
		return loopbackConfig{}, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg loopbackConfig
	if err = json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg == nil {
		cfg = loopbackConfig{}
	}
	for _, b := range cfg {
		ip := net.ParseIP(b.Address)
		if ip == nil || ip.To4() == nil || !ip.IsLoopback() || b.Address == "127.0.0.1" {
			return nil, fmt.Errorf("invalid saved loopback address")
		}
	}
	return cfg, nil
}
func allocateLoopback(bindings loopbackConfig, target string) string {
	if b, ok := bindings[target]; ok {
		return b.Address
	}
	used := map[string]bool{}
	for _, b := range bindings {
		used[b.Address] = true
	}
	sum := sha256.Sum256([]byte(target))
	start := int(sum[0])*256 + int(sum[1])
	for n := 0; n < 65536; n++ {
		slot := (start + n) % 65536
		ip := fmt.Sprintf("127.77.%d.%d", slot/256, slot%256)
		if !used[ip] {
			return ip
		}
	}
	return ""
}
func namedLoopbackAddress(dir, target, name string) (string, error) {
	if !strings.Contains(target, "/") {
		cfg, err := loadConfig(dir)
		if err != nil {
			return "", err
		}
		target, err = canonicalTarget(cfg, target)
		if err != nil {
			return "", err
		}
	}
	// .localhost is resolved internally by browsers and cannot be isolated reliably.
	name = strings.ToLower(name)
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return "", fmt.Errorf(".localhost names cannot reliably use a dedicated IP; use %s.test and run tailmux loopback setup %s --name %s.test", strings.TrimSuffix(name, ".localhost"), target, strings.TrimSuffix(name, ".localhost"))
	}
	bindings, err := readLoopbacks(dir)
	if err != nil {
		return "", err
	}
	b, ok := bindings[target]
	if ok {
		for _, n := range b.Names {
			if n == name {
				return b.Address, nil
			}
		}
	}
	return "", fmt.Errorf("isolated hostname is not configured; run: tailmux loopback setup %s --name %s", target, name)
}
func loopbackCLI(dir string, cfg Config, args []string) error {
	if len(args) == 0 || len(args) == 1 && args[0] == "list" {
		bindings, err := readLoopbacks(dir)
		if err != nil {
			return err
		}
		targets := []string{}
		for target := range bindings {
			targets = append(targets, target)
		}
		sort.Strings(targets)
		for _, target := range targets {
			b := bindings[target]
			fmt.Printf("%s  %s  %s\n", target, b.Address, strings.Join(b.Names, ", "))
		}
		if len(targets) == 0 {
			fmt.Println("No isolated box addresses. Use tailmux loopback setup <host> --name <host>.test")
		}
		return nil
	}
	if args[0] != "setup" || (len(args) != 2 && len(args) != 4) || len(args) == 4 && args[2] != "--name" {
		return fmt.Errorf("usage: tailmux loopback setup <host> [--name NAME] | list")
	}
	target, err := canonicalTarget(cfg, args[1])
	if err != nil {
		return err
	}
	if _, err = cfg.host(target); err != nil {
		return err
	}
	_, alias, _ := strings.Cut(target, "/")
	name := alias + ".test"
	if len(args) == 4 {
		name = strings.ToLower(args[3])
	}
	// Validate using ForwardSpec's hostname grammar; no shell fragments or IP literals.
	spec := ForwardSpec{Target: target, Name: name, Ports: []PortMap{{Local: 3000, Remote: 3000}}}
	if err = spec.validate(); err != nil {
		return err
	}
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return fmt.Errorf("use a .test or custom hostname; browsers special-case .localhost")
	}
	lock, err := os.OpenFile(filepath.Join(dir, "loopbacks.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	bindings, err := readLoopbacks(dir)
	if err != nil {
		return err
	}
	for other, b := range bindings {
		for _, n := range b.Names {
			if n == name && other != target {
				return fmt.Errorf("%s is already assigned to %s", name, other)
			}
		}
	}
	ip := allocateLoopback(bindings, target)
	if ip == "" {
		return fmt.Errorf("no loopback addresses available")
	}
	fmt.Printf("Box: %s\nHostname: %s\nDedicated local address: %s\n\n", target, name, ip)
	fmt.Printf("This will add a loopback interface address and map %s to %s in /etc/hosts.\n", name, ip)
	fmt.Println("Sudo is needed because these are protected OS network settings. Only the small setup helper runs as root; Tailmux networking continues as your user.")
	fmt.Println("Existing applications keep their ports on 127.0.0.1. A wildcard listener on 0.0.0.0 may still conflict.")
	if loopbackOS == "darwin" {
		fmt.Println("Also installs a root-owned launchd job in /Library/LaunchDaemons to restore this address at every boot. It runs /sbin/ifconfig once; it does not start Tailmux or a forward.")
	}
	fmt.Print("Apply these changes? [y/N] ")
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("confirmation requires an interactive terminal")
	}
	if strings.ToLower(strings.TrimSpace(answer)) != "y" && strings.ToLower(strings.TrimSpace(answer)) != "yes" {
		return fmt.Errorf("setup cancelled; no system changes made")
	}
	if err = configureLoopbackSystem(ip, name); err != nil {
		return fmt.Errorf("loopback setup needs administrator access; rerun this command in your terminal: %w", err)
	}
	b := bindings[target]
	b.Address = ip
	found := false
	for _, n := range b.Names {
		if n == name {
			found = true
		}
	}
	if !found {
		b.Names = append(b.Names, name)
	}
	bindings[target] = b
	data, err := json.MarshalIndent(bindings, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".loopbacks-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(append(data, '\n'))
	if err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), filepath.Join(dir, "loopbacks.json")); err != nil {
		return err
	}
	fmt.Printf("Ready: tailmux forward %s 3000-3015 --name %s\n", target, name)
	return nil
}
