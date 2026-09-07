package tailmux

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

type MonitorSession struct {
	Source          string `json:"source"`
	Kind            string `json:"kind"`
	Runtime         string `json:"runtime"`
	ID              string `json:"id"`
	Title           string `json:"title"`
	State           string `json:"state"`
	NeedsAttention  bool   `json:"needs_attention"`
	AttentionKnown  bool   `json:"attention_known"`
	AttentionReason string `json:"attention_reason,omitempty"`
	Project         string `json:"project,omitempty"`
	Repo            string `json:"repo,omitempty"`
	Branch          string `json:"branch,omitempty"`
	TaskTitle       string `json:"task_title,omitempty"`
	AgentType       string `json:"agent_type,omitempty"`
	WorkspaceID     string `json:"workspace_id,omitempty"`
	CWD             string `json:"cwd,omitempty"`
	LastActivityAt  int64  `json:"last_activity_at,omitempty"` // Unix milliseconds from Orca.
}

type MonitorUsage struct {
	Source           string   `json:"source"`
	Provider         string   `json:"provider"`
	Account          string   `json:"account,omitempty"`
	Window           string   `json:"window,omitempty"`
	UsedPercent      *float64 `json:"used_percent,omitempty"`
	RemainingPercent *float64 `json:"remaining_percent,omitempty"`
	ResetsAt         string   `json:"resets_at,omitempty"`
}

type MonitorSourceError struct {
	Source  string `json:"source"`
	Message string `json:"message"`
}

type MonitorSessions struct {
	Target string               `json:"target"`
	Rows   []MonitorSession     `json:"rows"`
	Usage  []MonitorUsage       `json:"usage"`
	Errors []MonitorSourceError `json:"errors"`
}

var monitorCommandOutput = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).Output()
}
var monitorOrcaOutput = func(ctx context.Context, args ...string) ([]byte, error) {
	return monitorOrcaCommand(ctx, args...).Output()
}

func monitorOrcaCommand(ctx context.Context, args ...string) *exec.Cmd {
	return orcaCommand(ctx, args...)
}

type orcaMonitorReply struct {
	OK     bool `json:"ok"`
	Result struct {
		Worktrees []struct {
			ID              string `json:"worktreeId"`
			DisplayName     string `json:"displayName"`
			Status          string `json:"status"`
			WorkspaceStatus string `json:"workspaceStatus"`
			Unread          *bool  `json:"unread"`
			Repo            string `json:"repo"`
			Branch          string `json:"branch"`
			LastActivityAt  int64  `json:"lastActivityAt"`
			Agents          []struct {
				ID          string `json:"paneKey"`
				State       string `json:"state"`
				Type        string `json:"agentType"`
				TaskTitle   string `json:"taskTitle"`
				DisplayName string `json:"displayName"`
				UpdatedAt   int64  `json:"updatedAt"`
			} `json:"agents"`
		} `json:"worktrees"`
	} `json:"result"`
}

func explicitAgentAttention(state string) (bool, bool) {
	// Orca's current worktree summary exposes agent state, but does not define
	// an attention boolean or a documented state enum. Preserve it as unknown.
	return false, false
}

func collectOrcaSessions(ctx context.Context, dir, target string) ([]MonitorSession, error) {
	args := []string{"worktree", "ps"}
	runtimeName := "local"
	if target != "local" {
		routes, err := readOrcaRoutes(dir)
		if err != nil {
			return nil, fmt.Errorf("read saved route: %w", err)
		}
		route, ok := routes[target]
		if !ok || route.Environment == "" {
			return nil, fmt.Errorf("no saved Orca environment")
		}
		args = append(args, "--environment", route.Environment)
		runtimeName = target
	}
	args = append(args, "--json")
	out, err := monitorOrcaOutput(ctx, args...)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("Orca runtime unavailable")
	}
	var reply orcaMonitorReply
	if err = json.Unmarshal(out, &reply); err != nil || !reply.OK {
		return nil, fmt.Errorf("invalid or unsuccessful Orca response")
	}
	rows := []MonitorSession{}
	for _, worktree := range reply.Result.Worktrees {
		state := worktree.Status
		if worktree.WorkspaceStatus != "" {
			state = worktree.WorkspaceStatus
		}
		row := MonitorSession{Source: "orca", Kind: "session", Runtime: runtimeName, ID: worktree.ID, Title: worktree.DisplayName, State: state, Project: worktree.Repo, Repo: worktree.Repo, Branch: worktree.Branch, LastActivityAt: worktree.LastActivityAt}
		if worktree.Unread != nil {
			row.AttentionKnown, row.NeedsAttention = true, *worktree.Unread
			if *worktree.Unread {
				row.AttentionReason = "Unread update"
			}
		}
		rows = append(rows, row)
		for _, agent := range worktree.Agents {
			title := agent.DisplayName
			if title == "" {
				title = agent.TaskTitle
			}
			if title == "" {
				title = agent.Type
			}
			needs, known := explicitAgentAttention(agent.State)
			rows = append(rows, MonitorSession{Source: "orca", Kind: "agent", Runtime: runtimeName, ID: agent.ID, Title: title, State: agent.State, Project: worktree.Repo, Repo: worktree.Repo, Branch: worktree.Branch, TaskTitle: agent.TaskTitle, AgentType: agent.Type, LastActivityAt: agent.UpdatedAt, NeedsAttention: needs, AttentionKnown: known})
		}
	}
	return rows, nil
}

type herdrSessionRecord struct {
	Name    string `json:"name"`
	Running bool   `json:"running"`
}

func parseHerdrSessionRecords(out []byte) ([]herdrSessionRecord, error) {
	var reply struct {
		Sessions []herdrSessionRecord `json:"sessions"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		return nil, fmt.Errorf("invalid Herdr response")
	}
	if reply.Sessions == nil {
		return nil, fmt.Errorf("Herdr response has no sessions array")
	}
	return reply.Sessions, nil
}

func parseHerdrMonitor(out []byte, _ string) ([]MonitorSession, error) {
	sessions, err := parseHerdrSessionRecords(out)
	if err != nil {
		return nil, err
	}
	rows := make([]MonitorSession, 0, len(sessions))
	for _, session := range sessions {
		state := "stopped"
		if session.Running {
			state = "running"
		}
		rows = append(rows, MonitorSession{Source: "herdr", Kind: "session", Runtime: session.Name, ID: session.Name, Title: session.Name, State: state})
	}
	return rows, nil
}

func herdrAttention(state string) (bool, bool, string) {
	switch state {
	case "blocked":
		return true, true, "Approval or answer requested"
	case "done":
		return true, true, "Background work completed"
	case "idle", "working":
		return false, true, ""
	default:
		return false, false, ""
	}
}

func parseHerdrAgents(out []byte, runtimeName string) ([]MonitorSession, error) {
	var reply struct {
		Result struct {
			Agents []struct {
				Agent         string `json:"agent"`
				DisplayAgent  string `json:"display_agent"`
				Status        string `json:"agent_status"`
				PaneID        string `json:"pane_id"`
				TerminalID    string `json:"terminal_id"`
				WorkspaceID   string `json:"workspace_id"`
				CWD           string `json:"cwd"`
				ForegroundCWD string `json:"foreground_cwd"`
				Title         string `json:"title"`
				Label         string `json:"label"`
			} `json:"agents"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out, &reply); err != nil {
		return nil, fmt.Errorf("invalid Herdr agent response")
	}
	if reply.Result.Agents == nil {
		return nil, fmt.Errorf("Herdr response has no agents array")
	}
	rows := make([]MonitorSession, 0, len(reply.Result.Agents))
	for _, agent := range reply.Result.Agents {
		id := agent.PaneID
		if agent.TerminalID != "" {
			id = agent.TerminalID
		}
		title := agent.Title
		if title == "" {
			title = agent.DisplayAgent
		}
		if title == "" {
			title = agent.Label
		}
		if title == "" {
			title = agent.Agent
		}
		cwd := agent.ForegroundCWD
		if cwd == "" {
			cwd = agent.CWD
		}
		needs, known, reason := herdrAttention(agent.Status)
		rows = append(rows, MonitorSession{Source: "herdr", Kind: "agent", Runtime: runtimeName, ID: id, Title: title, State: agent.Status, Project: agent.WorkspaceID, TaskTitle: agent.Title, AgentType: agent.Agent, WorkspaceID: agent.WorkspaceID, CWD: cwd, NeedsAttention: needs, AttentionKnown: known, AttentionReason: reason})
	}
	return rows, nil
}

func collectHerdrSessions(ctx context.Context, dir string, cfg Config, target string) ([]MonitorSession, error) {
	var sessionOutput []byte
	var runAgentList func(string) ([]byte, error)
	if target == "local" {
		out, err := monitorCommandOutput(ctx, "herdr", "session", "list", "--json")
		if err != nil {
			return nil, fmt.Errorf("Herdr unavailable")
		}
		sessionOutput = out
		runAgentList = func(session string) ([]byte, error) {
			return monitorCommandOutput(ctx, "herdr", "--session", session, "agent", "list")
		}
	} else {
		h, err := cfg.host(target)
		if err != nil {
			return nil, err
		}
		exe, err := os.Executable()
		if err != nil {
			return nil, err
		}
		sshRun := func(command string) ([]byte, error) {
			args := append(sshOptions(exe, dir, target, h), "-o", "BatchMode=yes", "-o", "ConnectTimeout=8", h.Address, command)
			return monitorCommandOutput(ctx, "ssh", args...)
		}
		out, err := sshRun("herdr session list --json")
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, fmt.Errorf("Herdr unavailable over SSH")
		}
		sessionOutput = out
		runAgentList = func(session string) ([]byte, error) {
			return sshRun("herdr --session " + shellQuote(session) + " agent list")
		}
	}
	rows, err := parseHerdrMonitor(sessionOutput, target)
	if err != nil {
		return nil, err
	}
	sessions, _ := parseHerdrSessionRecords(sessionOutput)
	failed := []string{}
	for _, session := range sessions {
		if !session.Running {
			continue
		}
		agents, agentErr := runAgentList(session.Name)
		if agentErr != nil {
			failed = append(failed, session.Name)
			continue
		}
		agentRows, agentErr := parseHerdrAgents(agents, session.Name)
		if agentErr != nil {
			failed = append(failed, session.Name)
			continue
		}
		rows = append(rows, agentRows...)
	}
	if len(failed) > 0 {
		return rows, fmt.Errorf("Herdr agent inventory unavailable for sessions: %s", strings.Join(failed, ", "))
	}
	return rows, nil
}

func collectBoxSessions(ctx context.Context, dir string, cfg Config, target string) MonitorSessions {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	result := MonitorSessions{Target: target, Rows: []MonitorSession{}, Usage: []MonitorUsage{}, Errors: []MonitorSourceError{}}
	type sourceResult struct {
		source string
		rows   []MonitorSession
		err    error
	}
	results := make(chan sourceResult, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		rows, err := collectOrcaSessions(ctx, dir, target)
		results <- sourceResult{"orca", rows, err}
	}()
	go func() {
		defer wg.Done()
		rows, err := collectHerdrSessions(ctx, dir, cfg, target)
		results <- sourceResult{"herdr", rows, err}
	}()
	go func() { wg.Wait(); close(results) }()
	for source := range results {
		result.Rows = append(result.Rows, source.rows...)
		if source.err != nil {
			result.Errors = append(result.Errors, MonitorSourceError{Source: source.source, Message: source.err.Error()})
			continue
		}
	}
	sort.Slice(result.Rows, func(i, j int) bool {
		if result.Rows[i].Source != result.Rows[j].Source {
			return result.Rows[i].Source < result.Rows[j].Source
		}
		if result.Rows[i].Kind != result.Rows[j].Kind {
			return result.Rows[i].Kind < result.Rows[j].Kind
		}
		return result.Rows[i].ID < result.Rows[j].ID
	})
	sort.Slice(result.Errors, func(i, j int) bool { return result.Errors[i].Source < result.Errors[j].Source })
	return result
}
