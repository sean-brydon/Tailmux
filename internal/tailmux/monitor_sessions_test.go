package tailmux

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseHerdrMonitorKeepsAttentionUnknown(t *testing.T) {
	rows, err := parseHerdrMonitor([]byte(`{"sessions":[{"name":"agents","running":true},{"name":"old","running":false}]}`), "lab/dev")
	if err != nil || len(rows) != 2 {
		t.Fatalf("%+v %v", rows, err)
	}
	if rows[0].State != "running" || rows[0].AttentionKnown || rows[0].NeedsAttention {
		t.Fatalf("%+v", rows[0])
	}
	if _, err = parseHerdrMonitor([]byte(`{"result":{}}`), "lab/dev"); err == nil {
		t.Fatal("missing sessions accepted")
	}
}

func TestExplicitAgentAttentionDoesNotGuess(t *testing.T) {
	for _, state := range []string{"idle", "done", "running", "unknown"} {
		if needs, known := explicitAgentAttention(state); needs || known {
			t.Fatalf("%s inferred as known attention", state)
		}
	}
	if needs, known := explicitAgentAttention("waiting-for-input"); needs || known {
		t.Fatal("undocumented agent state was inferred")
	}
}

func TestCollectRemoteSessionsUsesSavedOrcaEnvironmentAndBatchSSH(t *testing.T) {
	original := monitorCommandOutput
	originalOrca := monitorOrcaOutput
	t.Cleanup(func() { monitorCommandOutput = original; monitorOrcaOutput = originalOrca })
	dir := t.TempDir()
	routes := map[string]orcaRoute{"worker-a": {Environment: "environment-id"}}
	b, _ := json.Marshal(routes)
	if err := os.WriteFile(filepath.Join(dir, "orca.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	monitorOrcaOutput = func(_ context.Context, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "worktree ps --environment environment-id --json") {
			t.Fatalf("unsafe Orca command: %s", joined)
		}
		return []byte(`{"ok":true,"result":{"worktrees":[{"worktreeId":"wt-1","displayName":"API","repo":"tailmux","branch":"refs/heads/main","lastActivityAt":123,"status":"active","workspaceStatus":"in-progress","unread":true,"preview":"must not be decoded","agents":[{"paneKey":"agent-1","state":"blocked","agentType":"codex","taskTitle":"Networking","updatedAt":124,"prompt":"secret prompt"},{"paneKey":"agent-2","state":"idle","agentType":"claude"}]}]}}`), nil
	}
	monitorCommandOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch name {
		case "ssh":
			if !strings.Contains(joined, "BatchMode=yes") {
				t.Fatalf("unsafe Herdr command: %s", joined)
			}
			if strings.HasSuffix(joined, "herdr session list --json") {
				return []byte(`{"sessions":[{"name":"agents","running":true}]}`), nil
			}
			if strings.HasSuffix(joined, "herdr --session 'agents' agent list") {
				return []byte(`{"result":{"agents":[{"agent":"codex","agent_status":"done","pane_id":"w1:p1","terminal_id":"term-1","workspace_id":"w1","foreground_cwd":"/repo","title":"Review"}]}}`), nil
			}
			t.Fatalf("unsafe Herdr command: %s", joined)
			return nil, nil
		default:
			t.Fatalf("unexpected executable %q", name)
			return nil, nil
		}
	}
	cfg := fixtureConfig()
	result := collectBoxSessions(context.Background(), dir, cfg, "worker-a")
	if len(result.Errors) != 0 || len(result.Rows) != 5 {
		t.Fatalf("%+v", result)
	}
	var blocked, idle, worktree *MonitorSession
	for i := range result.Rows {
		switch result.Rows[i].ID {
		case "agent-1":
			blocked = &result.Rows[i]
		case "agent-2":
			idle = &result.Rows[i]
		case "wt-1":
			worktree = &result.Rows[i]
		}
	}
	if blocked == nil || blocked.NeedsAttention || blocked.AttentionKnown {
		t.Fatalf("blocked=%+v", blocked)
	}
	if blocked.Repo != "tailmux" || blocked.Branch != "refs/heads/main" || blocked.TaskTitle != "Networking" || blocked.AgentType != "codex" || blocked.LastActivityAt != 124 {
		t.Fatalf("metadata=%+v", blocked)
	}
	if idle == nil || idle.NeedsAttention || idle.AttentionKnown {
		t.Fatalf("idle=%+v", idle)
	}
	if worktree == nil || !worktree.NeedsAttention || !worktree.AttentionKnown || worktree.AttentionReason != "Unread update" {
		t.Fatalf("worktree=%+v", worktree)
	}
}

func TestParseHerdrAgentsUsesDocumentedStateAndMetadata(t *testing.T) {
	rows, err := parseHerdrAgents([]byte(`{"result":{"agents":[{"agent":"codex","agent_status":"blocked","pane_id":"p1","terminal_id":"t1","workspace_id":"w1","foreground_cwd":"/repo","title":"Fix auth"},{"agent":"claude","agent_status":"unknown","pane_id":"p2"}]}}`), "lab/dev")
	if err != nil || len(rows) != 2 {
		t.Fatalf("%+v %v", rows, err)
	}
	if rows[0].ID != "t1" || rows[0].AgentType != "codex" || rows[0].WorkspaceID != "w1" || rows[0].CWD != "/repo" || !rows[0].NeedsAttention || !rows[0].AttentionKnown {
		t.Fatalf("%+v", rows[0])
	}
	if rows[1].AttentionKnown || rows[1].NeedsAttention {
		t.Fatalf("unknown state inferred: %+v", rows[1])
	}
	needs, known, reason := herdrAttention("done")
	if !needs || !known || reason == "" {
		t.Fatal("documented unseen completion not surfaced")
	}
}

func TestHerdrAgentInventoryQueriesEveryRunningSession(t *testing.T) {
	original := monitorCommandOutput
	t.Cleanup(func() { monitorCommandOutput = original })
	queried := []string{}
	monitorCommandOutput = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "herdr" {
			t.Fatalf("unexpected command %s", name)
		}
		joined := strings.Join(args, " ")
		if joined == "session list --json" {
			return []byte(`{"sessions":[{"name":"agents","running":true},{"name":"tailmux-test","running":true},{"name":"default","running":false}]}`), nil
		}
		if len(args) == 4 && args[0] == "--session" && args[2] == "agent" && args[3] == "list" {
			queried = append(queried, args[1])
			return []byte(`{"result":{"agents":[]}}`), nil
		}
		t.Fatalf("unexpected Herdr args %q", args)
		return nil, nil
	}
	rows, err := collectHerdrSessions(context.Background(), t.TempDir(), fixtureConfig(), "local")
	if err != nil || len(rows) != 3 {
		t.Fatalf("%+v %v", rows, err)
	}
	if strings.Join(queried, ",") != "agents,tailmux-test" {
		t.Fatalf("queried %v", queried)
	}
}

func TestCollectSessionsKeepsPerSourceErrors(t *testing.T) {
	original := monitorCommandOutput
	originalOrca := monitorOrcaOutput
	t.Cleanup(func() { monitorCommandOutput = original; monitorOrcaOutput = originalOrca })
	monitorCommandOutput = func(_ context.Context, _ string, _ ...string) ([]byte, error) { return nil, os.ErrNotExist }
	monitorOrcaOutput = func(_ context.Context, _ ...string) ([]byte, error) { return nil, os.ErrNotExist }
	result := collectBoxSessions(context.Background(), t.TempDir(), fixtureConfig(), "local")
	if len(result.Errors) != 2 || result.Errors[0].Source != "herdr" || result.Errors[1].Source != "orca" {
		t.Fatalf("%+v", result.Errors)
	}
}

func TestMonitorOrcaCommandStripsInheritedRouting(t *testing.T) {
	t.Setenv("ORCA_ENVIRONMENT", "wrong-environment")
	t.Setenv("ORCA_PAIRING_CODE", "orca://secret")
	cmd := monitorOrcaCommand(context.Background(), "worktree", "ps", "--json")
	for _, value := range cmd.Env {
		if strings.HasPrefix(value, "ORCA_ENVIRONMENT=") || strings.HasPrefix(value, "ORCA_PAIRING_CODE=") {
			t.Fatalf("monitor inherited Orca routing: %q", value)
		}
	}
}
