package tailmux

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

type Host struct {
	Profile string `json:"profile"`
	Address string `json:"address"`
	User    string `json:"user,omitempty"`
	Port    int    `json:"port"`
}
type Config struct {
	TerminalBackend string          `json:"terminal_backend,omitempty"`
	Profiles        []string        `json:"profiles"`
	Hosts           map[string]Host `json:"hosts"`
}

var safeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)
var safeAddress = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:-]*$`)

func configDir() (string, error) {
	if v := os.Getenv("TAILMUX_HOME"); v != "" {
		return filepath.Abs(v)
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "tailmux"), nil
}
func defaultConfig() Config {
	return Config{Profiles: []string{}, Hosts: map[string]Host{}}
}
func (c Config) validate() error {
	if c.TerminalBackend != "" && c.TerminalBackend != "tmux" && c.TerminalBackend != "zellij" {
		return fmt.Errorf("invalid terminal backend %q", c.TerminalBackend)
	}
	profiles := map[string]bool{}
	for _, p := range c.Profiles {
		if !safeName.MatchString(p) || profiles[p] {
			return fmt.Errorf("invalid or duplicate profile %q", p)
		}
		profiles[p] = true
	}
	for name, h := range c.Hosts {
		_, alias, qualified := strings.Cut(name, "/")
		validName := safeName.MatchString(name)
		if qualified {
			validName = strings.HasPrefix(name, h.Profile+"/") && safeAddress.MatchString(alias)
		}
		if !validName || !profiles[h.Profile] || !safeAddress.MatchString(h.Address) || h.Port < 1 || h.Port > 65535 {
			return fmt.Errorf("invalid host %q", name)
		}
		if h.User != "" && !safeName.MatchString(h.User) {
			return fmt.Errorf("invalid SSH user for %q", name)
		}
	}
	return nil
}
func loadConfig(dir string) (Config, error) {
	var c Config
	b, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if err != nil {
		return c, fmt.Errorf("read config (run tailmux init): %w", err)
	}
	if err = json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	return c, c.validate()
}
func initConfig(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(defaultConfig(), "", "  ")
	b = append(b, '\n')
	f, err := os.OpenFile(filepath.Join(dir, "config.json"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return fmt.Errorf("config already exists at %s", filepath.Join(dir, "config.json"))
	}
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}
func (c Config) host(name string) (Host, error) {
	h, ok := c.Hosts[name]
	if ok {
		return h, nil
	}
	if profile, address, qualified := strings.Cut(name, "/"); qualified && c.hasProfile(profile) && safeAddress.MatchString(address) {
		// Also honor a preexisting short alias in local configuration.
		if h, ok := c.Hosts[address]; ok && h.Profile == profile {
			return h, nil
		}
		return Host{Profile: profile, Address: address, Port: 22}, nil
	}
	return Host{}, fmt.Errorf("unknown host %q; use <profile>/<hostname> or run tailmux hosts", name)
}
func (c Config) hasProfile(name string) bool {
	for _, p := range c.Profiles {
		if p == name {
			return true
		}
	}
	return false
}

// Serialize CLI updates so simultaneous logins cannot lose each other's profile.
func updateConfig(dir string, edit func(*Config) error) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "config.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	cfg, err := loadConfig(dir)
	if errors.Is(err, os.ErrNotExist) {
		cfg = defaultConfig()
	} else if err != nil {
		return err
	}
	if cfg.Hosts == nil {
		cfg.Hosts = map[string]Host{}
	}
	if err = edit(&cfg); err != nil {
		return err
	}
	if err = cfg.validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "config.json"))
}

func addProfile(dir, profile string) error {
	if !safeName.MatchString(profile) {
		return fmt.Errorf("invalid profile name %q", profile)
	}
	return updateConfig(dir, func(c *Config) error {
		if !c.hasProfile(profile) {
			c.Profiles = append(c.Profiles, profile)
		}
		return nil
	})
}
