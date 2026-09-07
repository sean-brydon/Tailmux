package tailmux

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
)

const loopbackHostsMarker = "# tailmux loopback "

var (
	loopbackEUID      = os.Geteuid
	loopbackOS        = runtime.GOOS
	loopbackHostsPath = "/etc/hosts"
	loopbackOutput    = func(name string, args ...string) ([]byte, error) { return exec.Command(name, args...).Output() }
	loopbackRun       = func(name string, args ...string) error { return exec.Command(name, args...).Run() }
	loopbackReplace   = replaceHostsFile
)

func validateLoopbackSystem(ip, name string) error {
	parsed := net.ParseIP(ip)
	if parsed == nil || parsed.To4() == nil || !parsed.IsLoopback() || ip == "127.0.0.1" || !strings.HasPrefix(ip, "127.") {
		return fmt.Errorf("loopback address must be an IPv4 127/8 address other than 127.0.0.1")
	}
	canonicalName := strings.TrimSuffix(name, ".")
	if canonicalName == "" || len(canonicalName) > 253 || canonicalName != strings.ToLower(canonicalName) || net.ParseIP(canonicalName) != nil || !strings.Contains(canonicalName, ".") || canonicalName == "localhost" || strings.HasSuffix(canonicalName, ".localhost") {
		return fmt.Errorf("invalid loopback hostname %q", name)
	}
	for _, label := range strings.Split(canonicalName, ".") {
		if !safeName.MatchString(label) || strings.Contains(label, "_") || strings.HasSuffix(label, "-") {
			return fmt.Errorf("invalid loopback hostname %q", name)
		}
	}
	return nil
}

// updateLoopbackHosts preserves existing bytes and appends one managed line.
// An existing equivalent mapping is accepted, managed or otherwise.
func updateLoopbackHosts(contents []byte, ip, name string) ([]byte, bool, error) {
	if err := validateLoopbackSystem(ip, name); err != nil {
		return nil, false, err
	}
	name = strings.TrimSuffix(name, ".")
	equivalent := false
	for _, line := range strings.Split(string(contents), "\n") {
		body := line
		if before, _, found := strings.Cut(body, "#"); found {
			body = before
		}
		fields := strings.Fields(body)
		if len(fields) < 2 {
			continue
		}
		for _, host := range fields[1:] {
			if strings.ToLower(strings.TrimSuffix(host, ".")) != name {
				continue
			}
			if fields[0] == ip {
				equivalent = true
				continue
			}
			return nil, false, fmt.Errorf("hostname %s is already mapped to %s in /etc/hosts", name, fields[0])
		}
	}
	if equivalent {
		return append([]byte(nil), contents...), false, nil
	}
	updated := append([]byte(nil), contents...)
	if len(updated) > 0 && updated[len(updated)-1] != '\n' {
		updated = append(updated, '\n')
	}
	updated = append(updated, []byte(fmt.Sprintf("%s\t%s\t%s%s\n", ip, name, loopbackHostsMarker, name))...)
	return updated, true, nil
}

func loopbackAliasExists(ip string) (bool, error) {
	var output []byte
	var err error
	switch loopbackOS {
	case "darwin":
		output, err = loopbackOutput("ifconfig", "lo0")
	case "linux":
		output, err = loopbackOutput("ip", "-o", "address", "show", "dev", "lo")
	default:
		return false, fmt.Errorf("loopback setup is unsupported on %s", loopbackOS)
	}
	if err != nil {
		return false, fmt.Errorf("inspect loopback interface: %w", err)
	}
	for _, field := range strings.Fields(string(output)) {
		if field == ip || strings.HasPrefix(field, ip+"/") {
			return true, nil
		}
	}
	return false, nil
}

func addLoopbackAlias(ip string) error {
	switch loopbackOS {
	case "darwin":
		return loopbackRun("ifconfig", "lo0", "alias", ip, "netmask", "255.255.255.255", "up")
	case "linux":
		return loopbackRun("ip", "address", "add", ip+"/32", "dev", "lo")
	default:
		return fmt.Errorf("loopback setup is unsupported on %s", loopbackOS)
	}
}

func removeLoopbackAlias(ip string) error {
	switch loopbackOS {
	case "darwin":
		return loopbackRun("ifconfig", "lo0", "-alias", ip)
	case "linux":
		return loopbackRun("ip", "address", "del", ip+"/32", "dev", "lo")
	default:
		return nil
	}
}

func replaceHostsFile(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tailmux-hosts-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err = tmp.Chmod(info.Mode().Perm()); err == nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			err = tmp.Chown(int(stat.Uid), int(stat.Gid))
		}
	}
	if err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// loopbackSystemSetup is the root-only half of loopback setup. It changes only
// the loopback interface and /etc/hosts; it never touches Tailmux state.
func loopbackSystemSetup(ip, name string) error {
	if loopbackEUID() != 0 {
		return fmt.Errorf("loopback system setup must run as root")
	}
	if err := validateLoopbackSystem(ip, name); err != nil {
		return err
	}
	name = strings.TrimSuffix(name, ".")
	lockPath := filepath.Join(filepath.Dir(loopbackHostsPath), ".tailmux-loopback.lock")
	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open loopback setup lock: %w", err)
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("lock loopback setup: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	contents, err := os.ReadFile(loopbackHostsPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", loopbackHostsPath, err)
	}
	updated, changed, err := updateLoopbackHosts(contents, ip, name)
	if err != nil {
		return err
	}
	existed, err := loopbackAliasExists(ip)
	if err != nil {
		return err
	}
	if !existed {
		if err = addLoopbackAlias(ip); err != nil {
			return fmt.Errorf("add loopback alias: %w", err)
		}
	}
	if changed {
		if err = loopbackReplace(loopbackHostsPath, updated); err != nil {
			if !existed {
				if rollbackErr := removeLoopbackAlias(ip); rollbackErr != nil {
					return fmt.Errorf("update %s: %v; rollback loopback alias: %w", loopbackHostsPath, err, rollbackErr)
				}
			}
			return fmt.Errorf("update %s: %w", loopbackHostsPath, err)
		}
	}
	if err := installLoopbackBoot(ip); err != nil {
		return fmt.Errorf("address is configured but boot restoration failed: %w", err)
	}
	return nil
}

// configureLoopbackSystem runs only the narrow privileged helper through sudo.
func configureLoopbackSystem(ip, name string) error {
	if err := validateLoopbackSystem(ip, name); err != nil {
		return err
	}
	name = strings.TrimSuffix(name, ".")
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command("sudo", exe, "loopback", "system-setup", ip, name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err = cmd.Run(); err != nil {
		return fmt.Errorf("privileged loopback setup failed: %w", err)
	}
	return nil
}
