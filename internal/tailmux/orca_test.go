package tailmux

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOrcaReadyRouting(t *testing.T) {
	r := orcaReady{Type: "orca_server_ready", Schema: 1, RuntimeID: "runtime-example", Bound: "ws://0.0.0.0:6768", Advertised: "ws://127.0.0.1:16768"}
	r.Pairing.Available = true
	r.Pairing.URL = "orca://pair?code=private"
	r.Pairing.Endpoint = r.Advertised
	b, _ := json.Marshal(r)
	_, route, e := parseOrcaReady(append([]byte("startup noise\n"), b...))
	if e != nil || route.LocalPort != 16768 || route.RemotePort != 6768 {
		t.Fatalf("%+v %v", route, e)
	}
	for _, endpoint := range []string{"ws://example.com:16768", "ws://127.0.0.1:0", "ws://127.0.0.1:70000", "ws://127.0.0.1:16768/path", "ws://secret@127.0.0.1:16768", "http://127.0.0.1:16768"} {
		bad := r
		bad.Advertised = endpoint
		b, _ = json.Marshal(bad)
		if _, _, e = parseOrcaReady(b); e == nil {
			t.Fatal(endpoint)
		} else if strings.Contains(e.Error(), "private") {
			t.Fatal("credential leaked")
		}
	}
	r.Pairing.Available = false
	b, _ = json.Marshal(r)
	if _, _, e = parseOrcaReady(b); e == nil {
		t.Fatal("unavailable pairing accepted")
	}
}
func TestOrcaRoutesContainNoPairing(t *testing.T) {
	dir := t.TempDir()
	routes := map[string]orcaRoute{"lab/worker": {Environment: "environment-id", RuntimeID: "runtime-id", LocalPort: 16768, RemotePort: 6768, ReadyFile: orcaReadyFile}}
	if e := saveOrcaRoutes(dir, routes); e != nil {
		t.Fatal(e)
	}
	r, e := readOrcaRoutes(dir)
	if e != nil || r["lab/worker"] != routes["lab/worker"] {
		t.Fatalf("%+v %v", r, e)
	}
	info, _ := os.Stat(filepath.Join(dir, "orca.json"))
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
}
func TestOrcaEnvironmentIsolation(t *testing.T) {
	t.Setenv("ORCA_CLI_COMMAND", "/chosen/orca")
	t.Setenv("ORCA_ENVIRONMENT", "unrelated")
	t.Setenv("ORCA_PAIRING_CODE", "credential")
	cmd := orcaCommand(context.Background(), "status")
	if cmd.Path != "/chosen/orca" {
		t.Fatal(cmd.Path)
	}
	for _, e := range cmd.Env {
		if strings.HasPrefix(e, "ORCA_ENVIRONMENT=") || strings.HasPrefix(e, "ORCA_PAIRING_CODE=") {
			t.Fatal("inherited unrelated routing")
		}
	}
}
