# Worktree Environments — Agent Side (Plan A)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give tailmux the ability to run a project's declared lifecycle for a git worktree on a remote host — allocating a port and database name, running setup, supervising the dev server, reporting readiness, and cleaning up on removal.

**Architecture:** A new `worktree` noun inside `internal/tailmux`. A repo-committed `tailmux.yaml` declares hooks and port ranges; a host-local JSON file supplies machine paths and secrets and wins field-by-field. Worktree state is persisted per project under the tailmux config directory. `setup` and `teardown` run to completion (setup serialized per project); `start` runs under a generated systemd **user** unit so the agent needs no root.

**Tech Stack:** Go 1.26.6, `gopkg.in/yaml.v3` (new direct dependency), stdlib `os/exec`, `net/http` for health checks, systemd user units via `systemctl --user`.

**Spec:** `docs/superpowers/specs/2026-09-08-tailmux-worktree-environments-design.md`

## Global Constraints

- Module path is `github.com/sean-brydon/Tailmux`; all new code is `package tailmux` in `internal/tailmux/`.
- Go 1.26.6 (from `go.mod`). Do not lower it.
- No root. Everything runs as the development user; services are `systemctl --user` units.
- The config directory is resolved by the existing `configDir()` in `config.go`, which honours `TAILMUX_HOME`. Never hardcode `~/.config/tailmux` — tests rely on `TAILMUX_HOME`.
- Existing naming conventions: file-per-concern with a matching `_test.go` (see `loopback.go` / `loopback_test.go`).
- Tailmux writes config files with mode `0o600` and directories `0o700`. Match that.
- Hook scripts are untrusted user input executed deliberately. They run via `sh -c` with an explicit environment — never interpolate values into the script text.
- `services:` / on-demand auxiliary processes and `tailscale serve` sharing are **out of scope** for this plan.

---

### Task 1: Repo config parsing (`tailmux.yaml`)

**Files:**
- Create: `internal/tailmux/worktree_config.go`
- Test: `internal/tailmux/worktree_config_test.go`
- Modify: `go.mod` (add `gopkg.in/yaml.v3`)

**Interfaces:**
- Consumes: nothing.
- Produces: `type ProjectConfig struct` with fields `Version int`, `Project string`, `Ports PortRanges`, `Hooks Hooks`, `Health Health`, `Reconcile Reconcile`; `type Hooks struct { Setup, Start, Teardown string }`; `type PortRanges struct { App string }`; `type Health struct { Path string; Timeout time.Duration }`; `type Reconcile struct { ArchivedMarker string }`; `func loadProjectConfig(path string) (ProjectConfig, error)`.

- [ ] **Step 1: Add the dependency**

```bash
cd ~/work/tailmux
go get gopkg.in/yaml.v3@v3.0.1
```

- [ ] **Step 2: Write the failing test**

Create `internal/tailmux/worktree_config_test.go`:

```go
package tailmux

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadProjectConfigParsesHooksAndHealth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tailmux.yaml")
	body := `
version: 1
project: cal
ports:
  app: 3100-3199
hooks:
  setup: |
    yarn install
    echo done
  start: yarn dev --port "$TAILMUX_PORT"
  teardown: dropdb "$TAILMUX_DATABASE_NAME"
health:
  path: /
  timeout: 300s
reconcile:
  archived_marker: ".orca-archived"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProjectConfig(path)
	if err != nil {
		t.Fatalf("loadProjectConfig: %v", err)
	}
	if cfg.Project != "cal" {
		t.Errorf("project = %q, want cal", cfg.Project)
	}
	if cfg.Ports.App != "3100-3199" {
		t.Errorf("ports.app = %q, want 3100-3199", cfg.Ports.App)
	}
	if cfg.Hooks.Start != `yarn dev --port "$TAILMUX_PORT"` {
		t.Errorf("hooks.start = %q", cfg.Hooks.Start)
	}
	if cfg.Health.Timeout != 300*time.Second {
		t.Errorf("health.timeout = %v, want 5m", cfg.Health.Timeout)
	}
	if cfg.Reconcile.ArchivedMarker != ".orca-archived" {
		t.Errorf("archived_marker = %q", cfg.Reconcile.ArchivedMarker)
	}
}

func TestLoadProjectConfigRejectsUnknownVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tailmux.yaml")
	if err := os.WriteFile(path, []byte("version: 2\nproject: cal\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProjectConfig(path); err == nil {
		t.Fatal("expected error for version 2")
	}
}

func TestLoadProjectConfigRejectsMissingProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tailmux.yaml")
	if err := os.WriteFile(path, []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProjectConfig(path); err == nil {
		t.Fatal("expected error for missing project")
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/tailmux/ -run TestLoadProjectConfig -v`
Expected: FAIL — `undefined: loadProjectConfig`

- [ ] **Step 4: Write the implementation**

Create `internal/tailmux/worktree_config.go`:

```go
package tailmux

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Hooks struct {
	Setup    string `yaml:"setup"`
	Start    string `yaml:"start"`
	Teardown string `yaml:"teardown"`
}

type PortRanges struct {
	App string `yaml:"app"`
}

// TimeoutRaw is decoded from YAML ("300s"); Timeout is the parsed value.
// yaml.v3 does not decode duration strings into time.Duration directly.
type Health struct {
	Path       string        `yaml:"path"`
	TimeoutRaw string        `yaml:"timeout"`
	Timeout    time.Duration `yaml:"-"`
}

type Reconcile struct {
	ArchivedMarker string `yaml:"archived_marker"`
}

type ProjectConfig struct {
	Version   int        `yaml:"version"`
	Project   string     `yaml:"project"`
	Ports     PortRanges `yaml:"ports"`
	Hooks     Hooks      `yaml:"hooks"`
	Health    Health     `yaml:"health"`
	Reconcile Reconcile  `yaml:"reconcile"`
}

// loadProjectConfig reads a repo-committed tailmux.yaml.
func loadProjectConfig(path string) (ProjectConfig, error) {
	var cfg ProjectConfig
	body, err := os.ReadFile(path)
	if err != nil {
		return ProjectConfig{}, err
	}
	if err := yaml.Unmarshal(body, &cfg); err != nil {
		return ProjectConfig{}, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.Version != 1 {
		return ProjectConfig{}, fmt.Errorf("%s: unsupported version %d, want 1", path, cfg.Version)
	}
	if !safeName.MatchString(cfg.Project) {
		return ProjectConfig{}, fmt.Errorf("%s: project must match %s", path, safeName)
	}
	if cfg.Health.Path == "" {
		cfg.Health.Path = "/"
	}
	cfg.Health.Timeout = 300 * time.Second
	if cfg.Health.TimeoutRaw != "" {
		d, err := time.ParseDuration(cfg.Health.TimeoutRaw)
		if err != nil {
			return ProjectConfig{}, fmt.Errorf("%s: health.timeout: %w", path, err)
		}
		cfg.Health.Timeout = d
	}
	return cfg, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/tailmux/ -run TestLoadProjectConfig -v`
Expected: PASS (3 tests)

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/tailmux/worktree_config.go internal/tailmux/worktree_config_test.go
git commit -m "feat(worktree): parse repo tailmux.yaml"
```

---

### Task 2: Host-local overrides and precedence

**Files:**
- Modify: `internal/tailmux/worktree_config.go`
- Modify: `internal/tailmux/worktree_config_test.go`

**Interfaces:**
- Consumes: `ProjectConfig`, `loadProjectConfig` from Task 1; `configDir()` from `config.go`.
- Produces: `type HostProject struct { Root string; Env map[string]string }`; `type ResolvedProject struct { ProjectConfig; Root string; Env map[string]string }`; `func loadHostProject(dir, project string) (HostProject, error)`; `func resolveProject(repo ProjectConfig, host HostProject) (ResolvedProject, error)`.

- [ ] **Step 1: Write the failing test**

Append to `internal/tailmux/worktree_config_test.go`:

```go
func TestResolveProjectHostRootWins(t *testing.T) {
	repo := ProjectConfig{Version: 1, Project: "cal"}
	host := HostProject{Root: "/home/sean/work/cal", Env: map[string]string{"DATABASE_URL": "postgres://x"}}
	got, err := resolveProject(repo, host)
	if err != nil {
		t.Fatalf("resolveProject: %v", err)
	}
	if got.Root != "/home/sean/work/cal" {
		t.Errorf("root = %q", got.Root)
	}
	if got.Env["DATABASE_URL"] != "postgres://x" {
		t.Errorf("env not carried through: %v", got.Env)
	}
}

func TestResolveProjectRequiresRoot(t *testing.T) {
	if _, err := resolveProject(ProjectConfig{Version: 1, Project: "cal"}, HostProject{}); err == nil {
		t.Fatal("expected error when host config supplies no root")
	}
}

func TestLoadHostProjectReadsFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"root":"/srv/cal","env":{"PATH":"/usr/bin"}}`
	if err := os.WriteFile(filepath.Join(dir, "projects", "cal.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadHostProject(dir, "cal")
	if err != nil {
		t.Fatalf("loadHostProject: %v", err)
	}
	if got.Root != "/srv/cal" || got.Env["PATH"] != "/usr/bin" {
		t.Errorf("got %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tailmux/ -run 'TestResolveProject|TestLoadHostProject' -v`
Expected: FAIL — `undefined: HostProject`

- [ ] **Step 3: Write the implementation**

Append to `internal/tailmux/worktree_config.go`:

```go
import (
	"encoding/json"
	"path/filepath"
)

type HostProject struct {
	Root string            `json:"root"`
	Env  map[string]string `json:"env,omitempty"`
}

type ResolvedProject struct {
	ProjectConfig
	Root string
	Env  map[string]string
}

// loadHostProject reads <dir>/projects/<project>.json. A missing file is not an
// error; the caller decides whether the resulting empty value is usable.
func loadHostProject(dir, project string) (HostProject, error) {
	var hp HostProject
	body, err := os.ReadFile(filepath.Join(dir, "projects", project+".json"))
	if os.IsNotExist(err) {
		return HostProject{}, nil
	}
	if err != nil {
		return HostProject{}, err
	}
	if err := json.Unmarshal(body, &hp); err != nil {
		return HostProject{}, err
	}
	return hp, nil
}

// resolveProject layers the host-local file over the repo file. The host file
// wins field by field; secrets and machine paths live only there.
func resolveProject(repo ProjectConfig, host HostProject) (ResolvedProject, error) {
	if host.Root == "" {
		return ResolvedProject{}, fmt.Errorf("project %q: host config must set \"root\"", repo.Project)
	}
	env := map[string]string{}
	for k, v := range host.Env {
		env[k] = v
	}
	return ResolvedProject{ProjectConfig: repo, Root: host.Root, Env: env}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tailmux/ -run 'TestResolveProject|TestLoadHostProject' -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/tailmux/worktree_config.go internal/tailmux/worktree_config_test.go
git commit -m "feat(worktree): layer host-local project overrides"
```

---

### Task 3: Port range parsing and allocation

**Files:**
- Create: `internal/tailmux/worktree_ports.go`
- Test: `internal/tailmux/worktree_ports_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func parsePortRange(s string) (lo, hi int, err error)`; `func allocatePort(rangeSpec string, taken map[int]bool) (int, error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/tailmux/worktree_ports_test.go`:

```go
package tailmux

import "testing"

func TestParsePortRange(t *testing.T) {
	lo, hi, err := parsePortRange("3100-3199")
	if err != nil {
		t.Fatalf("parsePortRange: %v", err)
	}
	if lo != 3100 || hi != 3199 {
		t.Errorf("got %d-%d, want 3100-3199", lo, hi)
	}
}

func TestParsePortRangeRejectsInverted(t *testing.T) {
	if _, _, err := parsePortRange("3199-3100"); err == nil {
		t.Fatal("expected error for inverted range")
	}
}

func TestParsePortRangeRejectsPrivileged(t *testing.T) {
	if _, _, err := parsePortRange("80-90"); err == nil {
		t.Fatal("expected error for privileged range")
	}
}

func TestAllocatePortSkipsTaken(t *testing.T) {
	taken := map[int]bool{3100: true, 3101: true}
	got, err := allocatePort("3100-3199", taken)
	if err != nil {
		t.Fatalf("allocatePort: %v", err)
	}
	if got != 3102 {
		t.Errorf("got %d, want 3102", got)
	}
}

func TestAllocatePortExhausted(t *testing.T) {
	taken := map[int]bool{3100: true, 3101: true}
	if _, err := allocatePort("3100-3101", taken); err == nil {
		t.Fatal("expected exhaustion error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tailmux/ -run 'TestParsePortRange|TestAllocatePort' -v`
Expected: FAIL — `undefined: parsePortRange`

- [ ] **Step 3: Write the implementation**

Create `internal/tailmux/worktree_ports.go`:

```go
package tailmux

import (
	"fmt"
	"strconv"
	"strings"
)

// parsePortRange accepts "LOW-HIGH". Privileged ports are rejected because the
// agent runs unprivileged.
func parsePortRange(s string) (int, int, error) {
	lo, hi, ok := strings.Cut(s, "-")
	if !ok {
		return 0, 0, fmt.Errorf("port range %q: want LOW-HIGH", s)
	}
	low, err := strconv.Atoi(strings.TrimSpace(lo))
	if err != nil {
		return 0, 0, fmt.Errorf("port range %q: %w", s, err)
	}
	high, err := strconv.Atoi(strings.TrimSpace(hi))
	if err != nil {
		return 0, 0, fmt.Errorf("port range %q: %w", s, err)
	}
	if low < 1024 || high > 65535 || low > high {
		return 0, 0, fmt.Errorf("port range %q: must be 1024-65535 and ascending", s)
	}
	return low, high, nil
}

// allocatePort returns the lowest free port in the range. Lowest-first keeps
// allocation stable and predictable across restarts.
func allocatePort(rangeSpec string, taken map[int]bool) (int, error) {
	low, high, err := parsePortRange(rangeSpec)
	if err != nil {
		return 0, err
	}
	for p := low; p <= high; p++ {
		if !taken[p] {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free port in range %s", rangeSpec)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tailmux/ -run 'TestParsePortRange|TestAllocatePort' -v`
Expected: PASS (5 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/tailmux/worktree_ports.go internal/tailmux/worktree_ports_test.go
git commit -m "feat(worktree): allocate ports from declared ranges"
```

---

### Task 4: Worktree identity

**Files:**
- Create: `internal/tailmux/worktree_identity.go`
- Test: `internal/tailmux/worktree_identity_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `func worktreeID(path string) string` (12 lowercase hex chars); `func worktreeSlug(name string) string`; `func worktreeHostname(slug, id, namespace, project string) string`; `func worktreeDatabaseName(id string) string`.

Hostname shape matches the existing kit: `<slug>-<id6>.<namespace>.<project>.localhost`, total label length capped so DNS labels stay under 63 characters.

- [ ] **Step 1: Write the failing test**

Create `internal/tailmux/worktree_identity_test.go`:

```go
package tailmux

import (
	"strings"
	"testing"
)

func TestWorktreeIDStableAndHex(t *testing.T) {
	a := worktreeID("/home/sean/orca/workspaces/cal/feat-tags")
	b := worktreeID("/home/sean/orca/workspaces/cal/feat-tags")
	if a != b {
		t.Fatalf("not stable: %q vs %q", a, b)
	}
	if len(a) != 12 {
		t.Errorf("len = %d, want 12", len(a))
	}
	if strings.Trim(a, "0123456789abcdef") != "" {
		t.Errorf("not lowercase hex: %q", a)
	}
	if worktreeID("/other/path") == a {
		t.Error("different paths produced the same id")
	}
}

func TestWorktreeSlugSanitises(t *testing.T) {
	got := worktreeSlug("feat/Tags: assign_tags!")
	if got != "feat-tags-assign-tags" {
		t.Errorf("got %q", got)
	}
}

func TestWorktreeHostnameLabelUnder63(t *testing.T) {
	long := strings.Repeat("a", 200)
	host := worktreeHostname(worktreeSlug(long), "5aaa8a200427", "work", "cal")
	label, _, _ := strings.Cut(host, ".")
	if len(label) > 63 {
		t.Errorf("label %d chars, want <= 63", len(label))
	}
	if !strings.HasSuffix(host, ".work.cal.localhost") {
		t.Errorf("suffix wrong: %q", host)
	}
}

func TestWorktreeDatabaseName(t *testing.T) {
	if got := worktreeDatabaseName("5aaa8a200427"); got != "calwt_5aaa8a200427" {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tailmux/ -run TestWorktree -v`
Expected: FAIL — `undefined: worktreeID`

- [ ] **Step 3: Write the implementation**

Create `internal/tailmux/worktree_identity.go`:

```go
package tailmux

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// worktreeID derives a stable identifier from the worktree's absolute path, so
// re-registering the same path reuses its port, database and hostname.
func worktreeID(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:])[:12]
}

func worktreeSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// worktreeHostname builds "<slug>-<id6>.<namespace>.<project>.localhost". The
// first label is truncated so it stays a valid DNS label.
func worktreeHostname(slug, id, namespace, project string) string {
	short := id
	if len(short) > 6 {
		short = short[:6]
	}
	const maxLabel = 63
	room := maxLabel - len(short) - 1
	if len(slug) > room {
		slug = strings.Trim(slug[:room], "-")
	}
	return slug + "-" + short + "." + namespace + "." + project + ".localhost"
}

func worktreeDatabaseName(id string) string { return "calwt_" + id }
```

Note: `worktreeDatabaseName` keeps the kit's `calwt_` prefix so the one-off adopt migration matches existing databases. It is a plain identifier — tailmux never connects to a database.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tailmux/ -run TestWorktree -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/tailmux/worktree_identity.go internal/tailmux/worktree_identity_test.go
git commit -m "feat(worktree): derive stable ids, slugs and hostnames"
```

---

### Task 5: State persistence and transitions

**Files:**
- Create: `internal/tailmux/worktree_state.go`
- Test: `internal/tailmux/worktree_state_test.go`

**Interfaces:**
- Consumes: `configDir()` from `config.go`.
- Produces: `type WorktreeState string` with constants `StateRegistered`, `StateSettingUp`, `StateStarting`, `StateReady`, `StateStopped`, `StateFailed`, `StateTornDown`; `type Worktree struct { ID, Path, Name, Project, Namespace, Hostname, Database string; Port int; State WorktreeState; Error string }`; `func loadWorktrees(dir, project string) (map[string]Worktree, error)`; `func saveWorktree(dir, project string, w Worktree) error`; `func deleteWorktree(dir, project, id string) error`; `func (w Worktree) canTransition(to WorktreeState) bool`.

- [ ] **Step 1: Write the failing test**

Create `internal/tailmux/worktree_state_test.go`:

```go
package tailmux

import "testing"

func TestSaveAndLoadWorktreeRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w := Worktree{
		ID: "5aaa8a200427", Path: "/w/feat", Name: "feat", Project: "cal",
		Namespace: "work", Hostname: "feat-5aaa8a.work.cal.localhost",
		Database: "calwt_5aaa8a200427", Port: 3111, State: StateReady,
	}
	if err := saveWorktree(dir, "cal", w); err != nil {
		t.Fatalf("saveWorktree: %v", err)
	}
	got, err := loadWorktrees(dir, "cal")
	if err != nil {
		t.Fatalf("loadWorktrees: %v", err)
	}
	if got["5aaa8a200427"].Port != 3111 || got["5aaa8a200427"].State != StateReady {
		t.Errorf("round trip lost data: %+v", got)
	}
}

func TestLoadWorktreesEmptyWhenAbsent(t *testing.T) {
	got, err := loadWorktrees(t.TempDir(), "cal")
	if err != nil {
		t.Fatalf("loadWorktrees: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %v", got)
	}
}

func TestDeleteWorktree(t *testing.T) {
	dir := t.TempDir()
	if err := saveWorktree(dir, "cal", Worktree{ID: "abc", State: StateReady}); err != nil {
		t.Fatal(err)
	}
	if err := deleteWorktree(dir, "cal", "abc"); err != nil {
		t.Fatalf("deleteWorktree: %v", err)
	}
	got, _ := loadWorktrees(dir, "cal")
	if len(got) != 0 {
		t.Errorf("still present: %v", got)
	}
}

func TestTransitionsRejectIllegalJumps(t *testing.T) {
	w := Worktree{State: StateRegistered}
	if w.canTransition(StateReady) {
		t.Error("registered -> ready should be rejected")
	}
	if !w.canTransition(StateSettingUp) {
		t.Error("registered -> setting-up should be allowed")
	}
	failed := Worktree{State: StateFailed}
	if !failed.canTransition(StateSettingUp) {
		t.Error("failed -> setting-up should be allowed (retry)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tailmux/ -run 'TestSaveAndLoad|TestLoadWorktrees|TestDeleteWorktree|TestTransitions' -v`
Expected: FAIL — `undefined: Worktree`

- [ ] **Step 3: Write the implementation**

Create `internal/tailmux/worktree_state.go`:

```go
package tailmux

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type WorktreeState string

const (
	StateRegistered WorktreeState = "registered"
	StateSettingUp  WorktreeState = "setting-up"
	StateStarting   WorktreeState = "starting"
	StateReady      WorktreeState = "ready"
	StateStopped    WorktreeState = "stopped"
	StateFailed     WorktreeState = "failed"
	StateTornDown   WorktreeState = "torn-down"
)

type Worktree struct {
	ID        string        `json:"id"`
	Path      string        `json:"path"`
	Name      string        `json:"name"`
	Project   string        `json:"project"`
	Namespace string        `json:"namespace"`
	Hostname  string        `json:"hostname"`
	Database  string        `json:"database"`
	Port      int           `json:"port"`
	State     WorktreeState `json:"state"`
	Error     string        `json:"error,omitempty"`
}

var worktreeTransitions = map[WorktreeState][]WorktreeState{
	StateRegistered: {StateSettingUp, StateTornDown},
	StateSettingUp:  {StateStarting, StateFailed, StateTornDown},
	StateStarting:   {StateReady, StateFailed, StateStopped, StateTornDown},
	StateReady:      {StateStopped, StateFailed, StateTornDown},
	StateStopped:    {StateStarting, StateTornDown},
	StateFailed:     {StateSettingUp, StateStarting, StateTornDown},
	StateTornDown:   {},
}

func (w Worktree) canTransition(to WorktreeState) bool {
	for _, s := range worktreeTransitions[w.State] {
		if s == to {
			return true
		}
	}
	return false
}

func worktreeDir(dir, project string) string {
	return filepath.Join(dir, "worktrees", project)
}

func loadWorktrees(dir, project string) (map[string]Worktree, error) {
	out := map[string]Worktree{}
	entries, err := os.ReadDir(worktreeDir(dir, project))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(worktreeDir(dir, project), e.Name()))
		if err != nil {
			return nil, err
		}
		var w Worktree
		if err := json.Unmarshal(body, &w); err != nil {
			return nil, err
		}
		out[w.ID] = w
	}
	return out, nil
}

func saveWorktree(dir, project string, w Worktree) error {
	target := worktreeDir(dir, project)
	if err := os.MkdirAll(target, 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(w, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(target, ".worktree-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(append(body, '\n')); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), filepath.Join(target, w.ID+".json"))
}

func deleteWorktree(dir, project, id string) error {
	err := os.Remove(filepath.Join(worktreeDir(dir, project), id+".json"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tailmux/ -run 'TestSaveAndLoad|TestLoadWorktrees|TestDeleteWorktree|TestTransitions' -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/tailmux/worktree_state.go internal/tailmux/worktree_state_test.go
git commit -m "feat(worktree): persist worktree state with guarded transitions"
```

---

### Task 6: Hook environment construction

**Files:**
- Create: `internal/tailmux/worktree_env.go`
- Test: `internal/tailmux/worktree_env_test.go`

**Interfaces:**
- Consumes: `Worktree` (Task 5), `ResolvedProject` (Task 2).
- Produces: `func hookEnv(p ResolvedProject, w Worktree) []string`.

- [ ] **Step 1: Write the failing test**

Create `internal/tailmux/worktree_env_test.go`:

```go
package tailmux

import (
	"slices"
	"testing"
)

func TestHookEnvIncludesAllVariables(t *testing.T) {
	p := ResolvedProject{Root: "/home/sean/work/cal", Env: map[string]string{"DATABASE_URL": "postgres://x"}}
	w := Worktree{
		ID: "5aaa8a200427", Path: "/w/feat", Name: "feat-tags",
		Namespace: "work", Hostname: "feat-tags-5aaa8a.work.cal.localhost",
		Database: "calwt_5aaa8a200427", Port: 3111,
	}
	env := hookEnv(p, w)
	want := []string{
		"TAILMUX_WORKTREE_PATH=/w/feat",
		"TAILMUX_ROOT_PATH=/home/sean/work/cal",
		"TAILMUX_WORKSPACE_NAME=feat-tags",
		"TAILMUX_ID=5aaa8a200427",
		"TAILMUX_PORT=3111",
		"TAILMUX_HOSTNAME=feat-tags-5aaa8a.work.cal.localhost",
		"TAILMUX_NAMESPACE=work",
		"TAILMUX_DATABASE_NAME=calwt_5aaa8a200427",
		"DATABASE_URL=postgres://x",
	}
	for _, w := range want {
		if !slices.Contains(env, w) {
			t.Errorf("missing %q in %v", w, env)
		}
	}
}

func TestHookEnvProjectEnvCannotOverrideTailmuxVars(t *testing.T) {
	p := ResolvedProject{Root: "/r", Env: map[string]string{"TAILMUX_PORT": "9999"}}
	env := hookEnv(p, Worktree{Port: 3111})
	if slices.Contains(env, "TAILMUX_PORT=9999") {
		t.Error("project env must not override TAILMUX_ variables")
	}
	if !slices.Contains(env, "TAILMUX_PORT=3111") {
		t.Error("TAILMUX_PORT missing")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tailmux/ -run TestHookEnv -v`
Expected: FAIL — `undefined: hookEnv`

- [ ] **Step 3: Write the implementation**

Create `internal/tailmux/worktree_env.go`:

```go
package tailmux

import (
	"strconv"
	"strings"
)

// hookEnv builds the environment for every hook. Project-supplied env is applied
// first so that TAILMUX_ variables always win — a project cannot lie to itself
// about its own port or database name.
func hookEnv(p ResolvedProject, w Worktree) []string {
	env := []string{}
	for k, v := range p.Env {
		if strings.HasPrefix(k, "TAILMUX_") {
			continue
		}
		env = append(env, k+"="+v)
	}
	env = append(env,
		"TAILMUX_WORKTREE_PATH="+w.Path,
		"TAILMUX_ROOT_PATH="+p.Root,
		"TAILMUX_WORKSPACE_NAME="+w.Name,
		"TAILMUX_ID="+w.ID,
		"TAILMUX_PORT="+strconv.Itoa(w.Port),
		"TAILMUX_HOSTNAME="+w.Hostname,
		"TAILMUX_NAMESPACE="+w.Namespace,
		"TAILMUX_DATABASE_NAME="+w.Database,
	)
	return env
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tailmux/ -run TestHookEnv -v`
Expected: PASS (2 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/tailmux/worktree_env.go internal/tailmux/worktree_env_test.go
git commit -m "feat(worktree): build hook environment"
```

---

### Task 7: Running setup and teardown hooks, serialized per project

**Files:**
- Create: `internal/tailmux/worktree_hooks.go`
- Test: `internal/tailmux/worktree_hooks_test.go`

**Interfaces:**
- Consumes: `hookEnv` (Task 6), `ResolvedProject` (Task 2), `Worktree` (Task 5).
- Produces: `func runHook(ctx context.Context, script string, p ResolvedProject, w Worktree, logPath string) error`; `func projectLock(dir, project string) (release func(), err error)`.

- [ ] **Step 1: Write the failing test**

Create `internal/tailmux/worktree_hooks_test.go`:

```go
package tailmux

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunHookCapturesOutputAndEnv(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "setup.log")
	p := ResolvedProject{Root: dir}
	w := Worktree{Path: dir, Port: 3111, ID: "abc"}
	err := runHook(context.Background(), `echo "port=$TAILMUX_PORT"`, p, w, logPath)
	if err != nil {
		t.Fatalf("runHook: %v", err)
	}
	body, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "port=3111") {
		t.Errorf("log missing output: %q", body)
	}
}

func TestRunHookReturnsErrorOnNonZeroExit(t *testing.T) {
	dir := t.TempDir()
	err := runHook(context.Background(), "exit 3", ResolvedProject{Root: dir}, Worktree{Path: dir}, filepath.Join(dir, "l.log"))
	if err == nil {
		t.Fatal("expected error for exit 3")
	}
}

func TestRunHookRunsInWorktreeDirectory(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "l.log")
	err := runHook(context.Background(), "pwd", ResolvedProject{Root: "/"}, Worktree{Path: dir}, logPath)
	if err != nil {
		t.Fatalf("runHook: %v", err)
	}
	body, _ := os.ReadFile(logPath)
	if !strings.Contains(string(body), dir) {
		t.Errorf("did not run in worktree dir: %q", body)
	}
}

func TestProjectLockSerialises(t *testing.T) {
	dir := t.TempDir()
	var mu sync.Mutex
	concurrent, max := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := projectLock(dir, "cal")
			if err != nil {
				t.Error(err)
				return
			}
			defer release()
			mu.Lock()
			concurrent++
			if concurrent > max {
				max = concurrent
			}
			mu.Unlock()
			time.Sleep(20 * time.Millisecond)
			mu.Lock()
			concurrent--
			mu.Unlock()
		}()
	}
	wg.Wait()
	if max != 1 {
		t.Errorf("max concurrent = %d, want 1", max)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tailmux/ -run 'TestRunHook|TestProjectLock' -v`
Expected: FAIL — `undefined: runHook`

- [ ] **Step 3: Write the implementation**

Create `internal/tailmux/worktree_hooks.go`:

```go
package tailmux

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

// runHook executes a user-supplied script with sh -c, in the worktree directory,
// with an explicit environment. The script text is never interpolated with
// values; everything reaches it through the environment.
func runHook(ctx context.Context, script string, p ResolvedProject, w Worktree, logPath string) error {
	if script == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Dir = w.Path
	cmd.Env = hookEnv(p, w)
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("hook failed: %w (see %s)", err, logPath)
	}
	return nil
}

var projectLocks sync.Map // project name -> *sync.Mutex

// projectLock serialises setup across worktrees of one project. Concurrent
// setups otherwise race over shared resources such as a cached database
// template or a package manager cache.
func projectLock(dir, project string) (func(), error) {
	value, _ := projectLocks.LoadOrStore(dir+"\x00"+project, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock, nil
}
```

Note: this lock is in-process, which is correct while a single agent owns the project. If a second agent process is ever supported, replace it with an `O_EXCL` lock file under `worktreeDir(dir, project)` — the signature already returns an error to allow that.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tailmux/ -run 'TestRunHook|TestProjectLock' -v`
Expected: PASS (4 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/tailmux/worktree_hooks.go internal/tailmux/worktree_hooks_test.go
git commit -m "feat(worktree): run hooks with serialized setup"
```

---

### Task 8: Supervised start via a systemd user unit

**Files:**
- Create: `internal/tailmux/worktree_unit.go`
- Test: `internal/tailmux/worktree_unit_test.go`

**Interfaces:**
- Consumes: `hookEnv` (Task 6), `ResolvedProject`, `Worktree`.
- Produces: `func unitName(project, id string) string`; `func renderUnit(p ResolvedProject, w Worktree, script string) string`; `func writeUnit(unitDir string, p ResolvedProject, w Worktree, script string) (string, error)`.

Unit generation follows the existing pattern in `setup.go`, which already writes `tailmux-orca.service`.

- [ ] **Step 1: Write the failing test**

Create `internal/tailmux/worktree_unit_test.go`:

```go
package tailmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitName(t *testing.T) {
	if got := unitName("cal", "5aaa8a200427"); got != "tailmux-wt-cal-5aaa8a200427.service" {
		t.Errorf("got %q", got)
	}
}

func TestRenderUnitContainsEnvironmentAndExec(t *testing.T) {
	p := ResolvedProject{Root: "/home/sean/work/cal", Env: map[string]string{"DATABASE_URL": "postgres://x"}}
	w := Worktree{ID: "abc", Path: "/w/feat", Port: 3111, Project: "cal"}
	unit := renderUnit(p, w, `yarn dev --port "$TAILMUX_PORT"`)
	for _, want := range []string{
		"WorkingDirectory=/w/feat",
		`Environment="TAILMUX_PORT=3111"`,
		`Environment="DATABASE_URL=postgres://x"`,
		"Restart=on-failure",
		"UMask=0077",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestWriteUnitCreatesFile(t *testing.T) {
	dir := t.TempDir()
	w := Worktree{ID: "abc", Path: dir, Port: 3111, Project: "cal"}
	path, err := writeUnit(dir, ResolvedProject{Root: dir}, w, "sleep 1")
	if err != nil {
		t.Fatalf("writeUnit: %v", err)
	}
	if filepath.Base(path) != "tailmux-wt-cal-abc.service" {
		t.Errorf("path = %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("unit not written: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tailmux/ -run 'TestUnitName|TestRenderUnit|TestWriteUnit' -v`
Expected: FAIL — `undefined: unitName`

- [ ] **Step 3: Write the implementation**

Create `internal/tailmux/worktree_unit.go`:

```go
package tailmux

import (
	"os"
	"path/filepath"
	"strings"
)

func unitName(project, id string) string {
	return "tailmux-wt-" + project + "-" + id + ".service"
}

// renderUnit writes a systemd user unit that supervises the start hook. The
// script runs under sh -c so it behaves identically to setup and teardown.
func renderUnit(p ResolvedProject, w Worktree, script string) string {
	var b strings.Builder
	b.WriteString("[Unit]\nDescription=Tailmux worktree " + w.Project + "/" + w.ID + "\nAfter=network.target\n\n[Service]\nType=simple\n")
	b.WriteString("WorkingDirectory=" + w.Path + "\n")
	for _, kv := range hookEnv(p, w) {
		b.WriteString("Environment=\"" + kv + "\"\n")
	}
	b.WriteString("ExecStart=/bin/sh -c " + systemdQuote(script) + "\n")
	b.WriteString("Restart=on-failure\nRestartSec=2\nUMask=0077\n\n[Install]\nWantedBy=default.target\n")
	return b.String()
}

// systemdQuote wraps a value in single quotes for an ExecStart argument.
func systemdQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func writeUnit(unitDir string, p ResolvedProject, w Worktree, script string) (string, error) {
	if err := os.MkdirAll(unitDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(unitDir, unitName(w.Project, w.ID))
	if err := os.WriteFile(path, []byte(renderUnit(p, w, script)), 0o600); err != nil {
		return "", err
	}
	return path, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tailmux/ -run 'TestUnitName|TestRenderUnit|TestWriteUnit' -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/tailmux/worktree_unit.go internal/tailmux/worktree_unit_test.go
git commit -m "feat(worktree): generate systemd user units for start hook"
```

---

### Task 9: Health polling to readiness

**Files:**
- Create: `internal/tailmux/worktree_health.go`
- Test: `internal/tailmux/worktree_health_test.go`

**Interfaces:**
- Consumes: `Health` (Task 1).
- Produces: `func waitForHealth(ctx context.Context, port int, h Health, poll time.Duration) error`.

- [ ] **Step 1: Write the failing test**

Create `internal/tailmux/worktree_health_test.go`:

```go
package tailmux

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func listenerPort(t *testing.T, handler http.Handler) (int, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	return ln.Addr().(*net.TCPAddr).Port, func() { srv.Close() }
}

func TestWaitForHealthSucceedsOn200(t *testing.T) {
	port, stop := listenerPort(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer stop()
	err := waitForHealth(context.Background(), port, Health{Path: "/", Timeout: 2 * time.Second}, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForHealth: %v", err)
	}
}

func TestWaitForHealthTimesOutWhenNothingListens(t *testing.T) {
	err := waitForHealth(context.Background(), 1, Health{Path: "/", Timeout: 200 * time.Millisecond}, 10*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestWaitForHealthAcceptsAnyNon5xx(t *testing.T) {
	port, stop := listenerPort(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer stop()
	if err := waitForHealth(context.Background(), port, Health{Path: "/", Timeout: 2 * time.Second}, 10*time.Millisecond); err != nil {
		t.Fatalf("404 should count as reachable: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tailmux/ -run TestWaitForHealth -v`
Expected: FAIL — `undefined: waitForHealth`

- [ ] **Step 3: Write the implementation**

Create `internal/tailmux/worktree_health.go`:

```go
package tailmux

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// waitForHealth polls the app until it answers. Any non-5xx response counts as
// reachable: a 404 still proves the server is up, and demanding 200 would force
// every project to expose a health route it may not have.
func waitForHealth(ctx context.Context, port int, h Health, poll time.Duration) error {
	deadline := time.Now().Add(h.Timeout)
	url := "http://127.0.0.1:" + strconv.Itoa(port) + h.Path
	client := &http.Client{Timeout: 2 * time.Second}
	var last error
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return nil
			}
			last = fmt.Errorf("status %d", resp.StatusCode)
		} else {
			last = err
		}
		time.Sleep(poll)
	}
	return fmt.Errorf("not ready after %s: %v", h.Timeout, last)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tailmux/ -run TestWaitForHealth -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/tailmux/worktree_health.go internal/tailmux/worktree_health_test.go
git commit -m "feat(worktree): poll app health before reporting ready"
```

---

### Task 10: Reconciliation of vanished and archived worktrees

**Files:**
- Create: `internal/tailmux/worktree_reconcile.go`
- Test: `internal/tailmux/worktree_reconcile_test.go`

**Interfaces:**
- Consumes: `Worktree`, `loadWorktrees` (Task 5), `Reconcile` (Task 1).
- Produces: `func staleWorktrees(all map[string]Worktree, r Reconcile) []Worktree`.

- [ ] **Step 1: Write the failing test**

Create `internal/tailmux/worktree_reconcile_test.go`:

```go
package tailmux

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStaleWorktreesDetectsMissingPath(t *testing.T) {
	all := map[string]Worktree{
		"a": {ID: "a", Path: "/definitely/not/here", State: StateReady},
	}
	got := staleWorktrees(all, Reconcile{})
	if len(got) != 1 || got[0].ID != "a" {
		t.Errorf("got %v", got)
	}
}

func TestStaleWorktreesDetectsArchivedMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".orca-archived"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	all := map[string]Worktree{"a": {ID: "a", Path: dir, State: StateReady}}
	got := staleWorktrees(all, Reconcile{ArchivedMarker: ".orca-archived"})
	if len(got) != 1 {
		t.Errorf("marker not detected: %v", got)
	}
}

func TestStaleWorktreesIgnoresLiveAndTornDown(t *testing.T) {
	dir := t.TempDir()
	all := map[string]Worktree{
		"live": {ID: "live", Path: dir, State: StateReady},
		"gone": {ID: "gone", Path: "/nope", State: StateTornDown},
	}
	if got := staleWorktrees(all, Reconcile{}); len(got) != 0 {
		t.Errorf("got %v, want none", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tailmux/ -run TestStaleWorktrees -v`
Expected: FAIL — `undefined: staleWorktrees`

- [ ] **Step 3: Write the implementation**

Create `internal/tailmux/worktree_reconcile.go`:

```go
package tailmux

import (
	"os"
	"path/filepath"
	"sort"
)

// staleWorktrees returns worktrees whose directory has disappeared or which
// carry the configured archived marker. Already torn-down entries are skipped so
// teardown runs at most once.
func staleWorktrees(all map[string]Worktree, r Reconcile) []Worktree {
	var out []Worktree
	for _, w := range all {
		if w.State == StateTornDown {
			continue
		}
		if _, err := os.Stat(w.Path); os.IsNotExist(err) {
			out = append(out, w)
			continue
		}
		if r.ArchivedMarker != "" {
			if _, err := os.Stat(filepath.Join(w.Path, r.ArchivedMarker)); err == nil {
				out = append(out, w)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tailmux/ -run TestStaleWorktrees -v`
Expected: PASS (3 tests)

- [ ] **Step 5: Commit**

```bash
git add internal/tailmux/worktree_reconcile.go internal/tailmux/worktree_reconcile_test.go
git commit -m "feat(worktree): detect vanished and archived worktrees"
```

---

### Task 11: Register command and end-to-end fixture test

**Files:**
- Create: `internal/tailmux/worktree.go`
- Test: `internal/tailmux/worktree_test.go`
- Create: `internal/tailmux/testdata/fixture-project/tailmux.yaml`
- Modify: `internal/tailmux/cli.go` (add `case "worktree":` to the dispatch switch, following the existing `case "loopback":` pattern at line 133)
- Modify: `internal/tailmux/help.go` (add the `worktree` line to the command list)

**Interfaces:**
- Consumes: everything from Tasks 1-10.
- Produces: `func registerWorktree(ctx context.Context, dir, path, namespace string) (Worktree, error)`; `func runWorktree(args []string) error`.

This task deliberately excludes `systemctl` from the test path. `registerWorktree` takes the worktree as far as `starting` — allocation, setup, persistence — and stops there. The `starting → ready` transition requires the generated unit to actually be running, so `waitForHealth` (Task 9) is called by the agent loop, not by `registerWorktree`. Task 9's function is therefore written and tested here but wired up in the first host deployment; that is intentional, not an oversight.

- [ ] **Step 1: Create the fixture project**

Create `internal/tailmux/testdata/fixture-project/tailmux.yaml`:

```yaml
version: 1
project: fixture
ports:
  app: 39000-39099
hooks:
  setup: |
    echo "setup ran for $TAILMUX_ID" > "$TAILMUX_WORKTREE_PATH/setup-ran"
  start: |
    while true; do printf 'HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok' | nc -l 127.0.0.1 "$TAILMUX_PORT"; done
  teardown: |
    rm -f "$TAILMUX_WORKTREE_PATH/setup-ran"
health:
  path: /
  timeout: 10s
```

- [ ] **Step 2: Write the failing test**

Create `internal/tailmux/worktree_test.go`:

```go
package tailmux

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestRegisterWorktreeRunsSetupAndAllocates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TAILMUX_HOME", home)

	work := t.TempDir()
	src, err := os.ReadFile(filepath.Join("testdata", "fixture-project", "tailmux.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "tailmux.yaml"), src, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "projects"), 0o700); err != nil {
		t.Fatal(err)
	}
	hostCfg := `{"root":"` + work + `"}`
	if err := os.WriteFile(filepath.Join(home, "projects", "fixture.json"), []byte(hostCfg), 0o600); err != nil {
		t.Fatal(err)
	}

	w, err := registerWorktree(context.Background(), home, work, "work")
	if err != nil {
		t.Fatalf("registerWorktree: %v", err)
	}
	if w.Port < 39000 || w.Port > 39099 {
		t.Errorf("port %d outside declared range", w.Port)
	}
	if w.Database != worktreeDatabaseName(w.ID) {
		t.Errorf("database = %q", w.Database)
	}
	if _, err := os.Stat(filepath.Join(work, "setup-ran")); err != nil {
		t.Errorf("setup hook did not run: %v", err)
	}
	saved, err := loadWorktrees(home, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := saved[w.ID]; !ok {
		t.Error("worktree not persisted")
	}
}

func TestRegisterWorktreeSecondCallReusesPort(t *testing.T) {
	home := t.TempDir()
	t.Setenv("TAILMUX_HOME", home)
	work := t.TempDir()
	src, _ := os.ReadFile(filepath.Join("testdata", "fixture-project", "tailmux.yaml"))
	os.WriteFile(filepath.Join(work, "tailmux.yaml"), src, 0o600)
	os.MkdirAll(filepath.Join(home, "projects"), 0o700)
	os.WriteFile(filepath.Join(home, "projects", "fixture.json"), []byte(`{"root":"`+work+`"}`), 0o600)

	first, err := registerWorktree(context.Background(), home, work, "work")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	second, err := registerWorktree(context.Background(), home, work, "work")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first.Port != second.Port || first.ID != second.ID {
		t.Errorf("re-register changed identity: %+v vs %+v", first, second)
	}
}
```

- [ ] **Step 3: Run test to verify it fails**

Run: `go test ./internal/tailmux/ -run TestRegisterWorktree -v`
Expected: FAIL — `undefined: registerWorktree`

- [ ] **Step 4: Write the implementation**

Create `internal/tailmux/worktree.go`:

```go
package tailmux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// registerWorktree adopts a worktree that already exists on disk, allocating its
// port, hostname and database name, then running the setup hook. Re-registering
// the same path is idempotent: the identity is derived from the path.
func registerWorktree(ctx context.Context, dir, path, namespace string) (Worktree, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return Worktree{}, err
	}
	repo, err := loadProjectConfig(filepath.Join(abs, "tailmux.yaml"))
	if err != nil {
		return Worktree{}, err
	}
	host, err := loadHostProject(dir, repo.Project)
	if err != nil {
		return Worktree{}, err
	}
	p, err := resolveProject(repo, host)
	if err != nil {
		return Worktree{}, err
	}

	existing, err := loadWorktrees(dir, repo.Project)
	if err != nil {
		return Worktree{}, err
	}
	id := worktreeID(abs)
	if w, ok := existing[id]; ok && w.State != StateTornDown {
		return w, nil
	}

	taken := map[int]bool{}
	for _, w := range existing {
		if w.State != StateTornDown {
			taken[w.Port] = true
		}
	}
	port, err := allocatePort(p.Ports.App, taken)
	if err != nil {
		return Worktree{}, err
	}

	name := filepath.Base(abs)
	w := Worktree{
		ID: id, Path: abs, Name: name, Project: repo.Project, Namespace: namespace,
		Hostname: worktreeHostname(worktreeSlug(name), id, namespace, repo.Project),
		Database: worktreeDatabaseName(id), Port: port, State: StateRegistered,
	}
	if err := saveWorktree(dir, repo.Project, w); err != nil {
		return Worktree{}, err
	}

	release, err := projectLock(dir, repo.Project)
	if err != nil {
		return Worktree{}, err
	}
	defer release()

	w.State = StateSettingUp
	if err := saveWorktree(dir, repo.Project, w); err != nil {
		return Worktree{}, err
	}
	logPath := filepath.Join(worktreeDir(dir, repo.Project), w.ID+"-setup.log")
	if err := runHook(ctx, p.Hooks.Setup, p, w, logPath); err != nil {
		w.State, w.Error = StateFailed, err.Error()
		saveWorktree(dir, repo.Project, w)
		return w, err
	}
	w.State = StateStarting
	if err := saveWorktree(dir, repo.Project, w); err != nil {
		return Worktree{}, err
	}
	return w, nil
}

func runWorktree(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: tailmux worktree register <path> [--namespace NAME]")
	}
	dir, err := configDir()
	if err != nil {
		return err
	}
	switch args[0] {
	case "register":
		namespace := "work"
		for i := 2; i+1 < len(args); i += 2 {
			if args[i] == "--namespace" {
				namespace = args[i+1]
			}
		}
		w, err := registerWorktree(context.Background(), dir, args[1], namespace)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "%s  %s  port %d  db %s\n", w.ID, w.Hostname, w.Port, w.Database)
		return nil
	case "list":
		if len(args) < 2 {
			return fmt.Errorf("usage: tailmux worktree list <project>")
		}
		all, err := loadWorktrees(dir, args[1])
		if err != nil {
			return err
		}
		ids := make([]string, 0, len(all))
		for id := range all {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			w := all[id]
			fmt.Fprintf(os.Stdout, "%-13s %-11s %-6d %s\n", w.ID, w.State, w.Port, w.Hostname)
		}
		return nil
	}
	return fmt.Errorf("unknown worktree command %q", args[0])
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/tailmux/ -run TestRegisterWorktree -v`
Expected: PASS (2 tests)

- [ ] **Step 6: Wire the CLI**

In `internal/tailmux/cli.go`, add to the dispatch switch alongside `case "loopback":` (line 133):

```go
	case "worktree":
		return runWorktree(args[1:])
```

In `internal/tailmux/help.go`, add to the command list:

```
  tailmux worktree register <path> [--namespace NAME]   Adopt a worktree and run its setup
```

- [ ] **Step 7: Run the whole package**

Run: `go test ./internal/tailmux/`
Expected: PASS, no regressions in existing tests

- [ ] **Step 8: Build and smoke the command**

```bash
go build -o bin/tailmux ./cmd/tailmux
./bin/tailmux worktree 2>&1 | head -3
```
Expected: the usage line, not a panic

- [ ] **Step 9: Commit**

```bash
git add internal/tailmux/worktree.go internal/tailmux/worktree_test.go \
        internal/tailmux/testdata/fixture-project/tailmux.yaml \
        internal/tailmux/cli.go internal/tailmux/help.go
git commit -m "feat(worktree): add register command and fixture integration test"
```

---

## Out of scope for this plan

Deferred to Plan B (client side): the hostname router, per-host loopback IP allocation, the "starting" page, and the logs endpoint surfaced over HTTP.

Deferred entirely: `tailscale serve` sharing, on-demand auxiliary services, the one-off dev-sean adopt migration, and cutting the first release that the agent install story depends on.

Also not covered here: actually invoking `systemctl --user daemon-reload` / `start` for the generated unit, and the `tailmux agent` long-running process that drives reconciliation on a timer. Both need a real systemd session and belong with the first end-to-end host deployment.
