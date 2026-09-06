package tailmux

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestInitPreservesExistingConfig(t *testing.T) {
	dir := t.TempDir()
	if err := initConfig(dir); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != 0 || len(cfg.Hosts) != 0 {
		t.Fatal(cfg)
	}
	path := filepath.Join(dir, "config.json")
	before, _ := os.ReadFile(path)
	if err = initConfig(dir); err == nil {
		t.Fatal("overwrote config")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("config changed")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions %v", info.Mode())
	}
}
func TestRejectInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"path escape", func(c *Config) { c.Profiles = []string{"../beta"} }},
		{"duplicate profile", func(c *Config) { c.Profiles = append(c.Profiles, "alpha") }},
		{"unknown profile", func(c *Config) { h := c.Hosts["worker-a"]; h.Profile = "typo"; c.Hosts["worker-a"] = h }},
		{"shell address", func(c *Config) { h := c.Hosts["worker-a"]; h.Address = "host;touch /tmp/bad"; c.Hosts["worker-a"] = h }},
		{"option user", func(c *Config) { h := c.Hosts["worker-a"]; h.User = "-oProxyCommand=id"; c.Hosts["worker-a"] = h }},
		{"invalid port", func(c *Config) { h := c.Hosts["worker-a"]; h.Port = 65536; c.Hosts["worker-a"] = h }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := fixtureConfig()
			tt.mutate(&cfg)
			if cfg.validate() == nil {
				t.Fatal("accepted invalid config")
			}
		})
	}
}
func TestProtocolPreservesSSHBannner(t *testing.T) {
	const payload = "SSH-2.0-OpenSSH_10.0\r\n\x00\xff\x00"
	r := bufio.NewReader(strings.NewReader("{\"kind\":\"ready\"}\n" + payload))
	var res response
	if err := readLine(r, &res); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("lost bytes: %q", got)
	}
}
func TestProtocolRejectsOversizedHandshake(t *testing.T) {
	r := bufio.NewReader(strings.NewReader(strings.Repeat("x", (1<<20)+1) + "\n"))
	if err := readLine(r, &request{}); err == nil {
		t.Fatal("accepted oversized handshake")
	}
}
func TestSSHProxyQuoting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spaces ' quote $dollar `ticks` %h")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "tailmux")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nprintf '%s\\n' \"$TAILMUX_HOME\" \"$1\" \"$2\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	h := fixtureConfig().Hosts["worker-a"]
	args := sshOptions(exe, dir, "worker-a", h)
	proxy := strings.TrimPrefix(args[1], "ProxyCommand=")
	// Simulate OpenSSH's %% -> % expansion before its shell invocation.
	proxy = strings.ReplaceAll(proxy, "%%", "%")
	out, err := exec.Command("sh", "-c", proxy).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	if string(out) != dir+"\nproxy\nworker-a\n" {
		t.Fatalf("wrong argument boundary: %q", out)
	}
	work := h
	work.Profile = "beta"
	if reflect.DeepEqual(args, sshOptions(exe, dir, "worker-a", work)) {
		t.Fatal("profiles share SSH identity")
	}
}
func TestDaemonRejectsUnknownHostWithoutStartingNode(t *testing.T) {
	dir := t.TempDir()
	if err := initConfig(dir); err != nil {
		t.Fatal(err)
	}
	d := &daemon{dir: dir}
	client, server := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() { defer server.Close(); d.handle(ctx, server) }()
	if err := json.NewEncoder(client).Encode(request{Op: "dial", Target: "other"}); err != nil {
		t.Fatal(err)
	}
	var res response
	if err := readLine(bufio.NewReader(client), &res); err != nil {
		t.Fatal(err)
	}
	if res.Kind != "error" || !strings.Contains(res.Message, "unknown host") {
		t.Fatalf("unexpected response %+v", res)
	}
	if len(d.nodes) != 0 {
		t.Fatal("unexpected tsnet node")
	}
}
func TestDaemonPingAndCancellation(t *testing.T) {
	d := &daemon{}
	client, server := net.Pipe()
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); defer server.Close(); d.handle(ctx, server) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("daemon request ignored cancellation")
	}
}

func fixtureConfig() Config {
	return Config{Profiles: []string{"alpha", "beta"}, Hosts: map[string]Host{
		"worker-a": {Profile: "alpha", Address: "worker-a", Port: 22},
		"worker-b": {Profile: "beta", Address: "worker-b", Port: 22},
	}}
}
func TestGenericProfilesAndQualifiedTargets(t *testing.T) {
	dir := t.TempDir()
	if err := addProfile(dir, "customer-42"); err != nil {
		t.Fatal(err)
	}
	if err := addProfile(dir, "lab"); err != nil {
		t.Fatal(err)
	}
	if err := addProfile(dir, "lab"); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != 2 || len(cfg.Hosts) != 0 {
		t.Fatal(cfg)
	}
	for _, target := range []string{"customer-42/worker", "lab/worker"} {
		h, err := cfg.host(target)
		if err != nil {
			t.Fatal(err)
		}
		if h.Profile != strings.Split(target, "/")[0] || h.Address != "worker" {
			t.Fatal(h)
		}
	}
	if _, err := cfg.host("missing/worker"); err == nil {
		t.Fatal("accepted unknown profile")
	}
	if _, err := cfg.host("lab/../escape"); err == nil {
		t.Fatal("accepted invalid target")
	}
	if err := configureHost(dir, cfg, []string{"lab/worker", "--user", "builder", "--port", "2222"}); err != nil {
		t.Fatal(err)
	}
	cfg, err = loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := cfg.host("lab/worker")
	if err != nil || h.User != "builder" || h.Port != 2222 {
		t.Fatalf("%+v %v", h, err)
	}
}

func TestConcurrentProfileCreation(t *testing.T) {
	dir := t.TempDir()
	names := []string{"team-a", "team-b", "sandbox", "client-c"}
	errs := make(chan error, len(names))
	for _, name := range names {
		go func(n string) { errs <- addProfile(dir, n) }(name)
	}
	for range names {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := loadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Profiles) != len(names) {
		t.Fatalf("lost profile update: %v", cfg.Profiles)
	}
	for _, name := range names {
		if !cfg.hasProfile(name) {
			t.Fatalf("missing %s", name)
		}
	}
}
