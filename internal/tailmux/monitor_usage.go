package tailmux

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"time"
)

type codexRateWindow struct {
	UsedPercent       *float64 `json:"usedPercent"`
	WindowDurationMin *int64   `json:"windowDurationMins"`
	ResetsAt          *int64   `json:"resetsAt"`
}

type codexRateSnapshot struct {
	LimitName string           `json:"limitName"`
	Primary   *codexRateWindow `json:"primary"`
	Secondary *codexRateWindow `json:"secondary"`
}

type codexRateResponse struct {
	RateLimits          codexRateSnapshot            `json:"rateLimits"`
	RateLimitsByLimitID map[string]codexRateSnapshot `json:"rateLimitsByLimitId"`
}

type codexAccountResponse struct {
	Account *struct {
		Type     string  `json:"type"`
		Email    *string `json:"email"`
		PlanType string  `json:"planType"`
	} `json:"account"`
}

type codexUsageResponse struct {
	Rates   codexRateResponse
	Account codexAccountResponse
}

var readCodexRateLimits = codexRateLimitsFromCommand

func codexRateLimitsFromCommand(ctx context.Context, cmd *exec.Cmd) (codexUsageResponse, error) {
	var result codexUsageResponse
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return result, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return result, err
	}
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return result, err
	}
	finished := false
	waitStarted := false
	var done chan error
	defer func() {
		stdin.Close()
		if !finished && cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		if !finished {
			if waitStarted {
				<-done
			} else {
				_ = cmd.Wait()
			}
		}
	}()
	enc := json.NewEncoder(stdin)
	dec := json.NewDecoder(bufio.NewReader(io.LimitReader(stdout, 2<<20)))
	if err = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "tailmux", "version": Version}}}); err != nil {
		return result, err
	}
	if err = waitForRPC(dec, 1, nil); err != nil {
		return result, fmt.Errorf("initialize Codex app-server: %w", err)
	}
	if err = enc.Encode(map[string]any{"jsonrpc": "2.0", "method": "initialized", "params": map[string]any{}}); err != nil {
		return result, err
	}
	if err = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 2, "method": "account/read", "params": map[string]any{"refreshToken": false}}); err != nil {
		return result, err
	}
	if err = waitForRPC(dec, 2, &result.Account); err != nil {
		return result, fmt.Errorf("read Codex account: %w", err)
	}
	if err = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": 3, "method": "account/rateLimits/read", "params": map[string]any{}}); err != nil {
		return result, err
	}
	if err = waitForRPC(dec, 3, &result.Rates); err != nil {
		return result, fmt.Errorf("read Codex rate limits: %w", err)
	}
	_ = stdin.Close()
	done = make(chan error, 1)
	waitStarted = true
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		finished = true
	case <-ctx.Done():
		return result, ctx.Err()
	case <-time.After(time.Second):
		// The response is complete; do not retain an idle app-server solely
		// because this CLI version waits for another transport event on EOF.
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-done
		finished = true
	}
	return result, nil
}

func waitForRPC(dec *json.Decoder, id int, result any) error {
	for {
		var message struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := dec.Decode(&message); err != nil {
			return err
		}
		if message.ID == nil || *message.ID != id {
			continue
		}
		if message.Error != nil {
			return fmt.Errorf("app-server request failed")
		}
		if result != nil {
			if err := json.Unmarshal(message.Result, result); err != nil {
				return err
			}
		}
		return nil
	}
}

func formatUsageWindow(minutes *int64) string {
	if minutes == nil || *minutes <= 0 {
		return ""
	}
	if *minutes%(24*60) == 0 {
		return fmt.Sprintf("%dd", *minutes/(24*60))
	}
	if *minutes%60 == 0 {
		return fmt.Sprintf("%dh", *minutes/60)
	}
	return fmt.Sprintf("%dm", *minutes)
}

func codexAccountLabel(source string, response codexAccountResponse) string {
	if response.Account == nil {
		return "current Codex login on " + source
	}
	switch response.Account.Type {
	case "chatgpt":
		if response.Account.Email != nil && *response.Account.Email != "" {
			return *response.Account.Email
		}
		if response.Account.PlanType != "" {
			return "ChatGPT " + response.Account.PlanType
		}
	case "apiKey":
		return "API key login on " + source
	case "amazonBedrock":
		return "Amazon Bedrock login on " + source
	}
	return "current Codex login on " + source
}

func usageFromCodexResponse(source string, response codexUsageResponse) ([]MonitorUsage, error) {
	snapshots := response.Rates.RateLimitsByLimitID
	if len(snapshots) == 0 {
		snapshots = map[string]codexRateSnapshot{"codex": response.Rates.RateLimits}
	}
	keys := make([]string, 0, len(snapshots))
	for key := range snapshots {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var usage []MonitorUsage
	for _, key := range keys {
		snapshot := snapshots[key]
		provider := snapshot.LimitName
		if provider == "" {
			provider = "Codex"
		}
		for _, window := range []*codexRateWindow{snapshot.Primary, snapshot.Secondary} {
			if window == nil || window.UsedPercent == nil {
				continue
			}
			used := *window.UsedPercent
			if used < 0 {
				used = 0
			} else if used > 100 {
				used = 100
			}
			remaining := 100 - used
			item := MonitorUsage{Source: source, Provider: provider, Account: codexAccountLabel(source, response.Account), UsedPercent: &used, RemainingPercent: &remaining, Window: formatUsageWindow(window.WindowDurationMin)}
			if window.ResetsAt != nil {
				item.ResetsAt = time.Unix(*window.ResetsAt, 0).UTC().Format(time.RFC3339)
			}
			usage = append(usage, item)
		}
	}
	if len(usage) == 0 {
		return nil, fmt.Errorf("Codex returned no usage windows")
	}
	return usage, nil
}

// collectBoxUsage queries the supported Codex app-server API on the selected
// box. It does not start a login flow, switch profiles, or read credential files.
func collectBoxUsage(ctx context.Context, dir string, cfg Config, target string) ([]MonitorUsage, error) {
	ctx, cancel := boundedMemoryContext(ctx)
	defer cancel()
	var cmd *exec.Cmd
	if target == "local" {
		path, err := exec.LookPath("codex")
		if err != nil {
			return nil, fmt.Errorf("Codex usage unavailable: codex CLI is not installed")
		}
		cmd = exec.CommandContext(ctx, path, "app-server", "--listen", "stdio://")
	} else {
		h, err := cfg.host(target)
		if err != nil {
			return nil, err
		}
		exe, err := monitorExecutable()
		if err != nil {
			return nil, err
		}
		args := append(sshOptions(exe, dir, target, h), "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", h.Address, "command -v codex >/dev/null 2>&1 || exit 127; exec codex app-server --listen stdio://")
		cmd = exec.CommandContext(ctx, "ssh", args...)
	}
	response, err := readCodexRateLimits(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("Codex usage unavailable on %s: %w", target, err)
	}
	return usageFromCodexResponse(target, response)
}
