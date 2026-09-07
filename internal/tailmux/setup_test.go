package tailmux

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestWriteSetupChecks(t *testing.T) {
	var b bytes.Buffer
	checks := []SetupCheck{{Host: "lab/dev", Tools: map[string]string{"tmux": "found", "zellij": "missing", "herdr": "found", "orca-ide": "missing", "ss": "found"}, Warnings: []string{"Orca is optional"}}}
	if err := writeSetupChecks(&b, checks); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"lab/dev", "found", "ready", "Orca is optional"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("missing %q in %s", want, b.String())
		}
	}
}

func TestCheckHostSetupReportsOptionalTools(t *testing.T) {
	original := remoteCommandOutput
	t.Cleanup(func() { remoteCommandOutput = original })
	remoteCommandOutput = func(context.Context, []string) ([]byte, error) {
		return []byte("os=Linux x86_64\ntmux=found\nzellij=missing\nherdr=missing\norca-ide=missing\nsystemctl=found\nss=found\nlsof=missing\n"), nil
	}
	r := checkHostSetup(t.TempDir(), "lab/dev", Host{Profile: "lab", Address: "dev", Port: 22})
	if r.OS != "Linux x86_64" || r.Tools["tmux"] != "found" || len(r.Warnings) != 2 {
		t.Fatalf("unexpected check: %+v", r)
	}
}

func TestSetupInstallRequiresExplicitPackage(t *testing.T) {
	cfg := fixtureConfig()
	if err := setupCLI(t.TempDir(), cfg, []string{"install", "worker-a"}); err == nil || !strings.Contains(err.Error(), "choose at least one") {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := setupCLI(t.TempDir(), cfg, []string{"check"}); err == nil {
		t.Fatal("missing check target accepted")
	}
}

func TestParseOrcaSetupArgs(t *testing.T) {
	target, local, remote, apply, replace, err := parseOrcaSetupArgs([]string{"lab/dev", "--remote-port", "7000", "--apply", "--replace", "--local-port", "17000"})
	if err != nil || target != "lab/dev" || local != 17000 || remote != 7000 || !apply || !replace {
		t.Fatalf("unexpected parse: %q %d %d %t %t %v", target, local, remote, apply, replace, err)
	}
	if _, _, _, _, _, err := parseOrcaSetupArgs([]string{"lab/dev", "--remote-port", "0"}); err == nil {
		t.Fatal("invalid port accepted")
	}
	if _, _, _, _, _, err := parseOrcaSetupArgs([]string{"lab/dev", "--replace"}); err == nil {
		t.Fatal("replace without apply accepted")
	}
}

func TestOrcaPlanIsReviewFirst(t *testing.T) {
	var b bytes.Buffer
	writeOrcaSetupPlan(&b, "lab/dev", 16768, 6768, false)
	for _, want := range []string{"no remote files", "--apply", "disabled and stopped", "iptables", "6768"} {
		if !strings.Contains(b.String(), want) {
			t.Fatalf("missing %q in %s", want, b.String())
		}
	}
}
