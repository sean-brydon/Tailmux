package tailmux

import (
	"reflect"
	"strings"
	"testing"
)

func TestSelectHerdrHosts(t *testing.T) {
	cfg := fixtureConfig()
	tests := []struct {
		name string
		args []string
		want []string
		bad  bool
	}{
		{"saved", nil, []string{"alpha/worker-a", "beta/worker-b"}, false},
		{"explicit order", []string{"beta/other", "alpha/worker-a"}, []string{"beta/other", "alpha/worker-a"}, false},
		{"aliases deduplicate", []string{"worker-a", "alpha/worker-a"}, []string{"alpha/worker-a"}, false},
		{"unknown", []string{"unknown/box"}, nil, true},
		{"invalid", []string{"alpha/box;id"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := selectHerdrHosts(cfg, tt.args)
			if (err != nil) != tt.bad {
				t.Fatalf("error %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v want %v", got, tt.want)
			}
		})
	}
	if _, err := selectHerdrHosts(defaultConfig(), nil); err == nil {
		t.Fatal("accepted empty selection")
	}
}
func TestHerdrCommandIsolatesParentSession(t *testing.T) {
	t.Setenv("HERDR_SOCKET_PATH", "/another/session.sock")
	t.Setenv("HERDR_SESSION", "unrelated")
	t.Setenv("SSH_AUTH_SOCK", "/agent.sock")
	cmd := herdrCommand("workspace", "list")
	if !reflect.DeepEqual(cmd.Args[1:], []string{"--session", "tailmux", "workspace", "list"}) {
		t.Fatal(cmd.Args)
	}
	foundAgent := false
	for _, env := range cmd.Env {
		if strings.HasPrefix(env, "HERDR_") {
			t.Fatalf("inherited parent: %s", env)
		}
		if env == "SSH_AUTH_SOCK=/agent.sock" {
			foundAgent = true
		}
	}
	if !foundAgent {
		t.Fatal("lost SSH agent")
	}
}
