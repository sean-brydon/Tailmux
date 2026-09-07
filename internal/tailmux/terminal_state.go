package tailmux

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

// Zellij has stable tab IDs but no user-defined metadata on a tab. Keep the
// routing identity separately so the tab name remains a freely editable label.
func (t terminal) zellijTargetsPath() string {
	return filepath.Join(t.dir, "terminal", "zellij-targets.json")
}

func (t terminal) withZellijTargets(update func(map[string]string) error) error {
	lock, err := os.OpenFile(t.zellijTargetsPath()+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	targets := map[string]string{}
	b, err := os.ReadFile(t.zellijTargetsPath())
	if err == nil {
		if err = json.Unmarshal(b, &targets); err != nil {
			return fmt.Errorf("read Zellij terminal metadata: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err = update(targets); err != nil {
		return err
	}
	b, err = json.Marshal(targets)
	if err != nil {
		return err
	}
	tmp := t.zellijTargetsPath() + ".tmp"
	if err = os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, t.zellijTargetsPath())
}

func (t terminal) loadZellijTargets() (map[string]string, error) {
	var copy map[string]string
	err := t.withZellijTargets(func(targets map[string]string) error {
		copy = make(map[string]string, len(targets))
		for id, target := range targets {
			copy[id] = target
		}
		return nil
	})
	return copy, err
}

func (t terminal) saveZellijTarget(tabID int, target string) error {
	return t.withZellijTargets(func(targets map[string]string) error {
		targets[strconv.Itoa(tabID)] = target
		return nil
	})
}

func (t terminal) zellijHasTarget(target string) (bool, error) {
	panes, err := t.zellijPanes(false)
	if err != nil {
		return false, err
	}
	targets, err := t.loadZellijTargets()
	if err != nil {
		return false, err
	}
	for _, pane := range panes {
		if targets[strconv.Itoa(pane.TabID)] == target {
			return true, nil
		}
	}
	return false, nil
}

func (t terminal) clearZellijTargets() error {
	return t.withZellijTargets(func(targets map[string]string) error {
		for id := range targets {
			delete(targets, id)
		}
		return nil
	})
}

func (t terminal) rememberTarget(pane string, tabID int, target string) {
	if t.backend == "tmux" {
		if _, err := runOutput(t.command("set-window-option", "-t", pane, "@tailmux_target", target)); err != nil {
			fmt.Fprintln(os.Stderr, "Could not store terminal routing:", err)
		}
		return
	}
	if err := t.saveZellijTarget(tabID, target); err != nil {
		fmt.Fprintln(os.Stderr, "Could not store terminal routing:", err)
	}
}
