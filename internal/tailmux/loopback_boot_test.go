package tailmux

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoopbackBootJob(t *testing.T) {
	data, err := loopbackBootPlist("127.77.133.203")
	if err != nil {
		t.Fatal(err)
	}
	var parsed any
	if err = xml.Unmarshal(data, &parsed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "/sbin/ifconfig") || !strings.Contains(string(data), "<key>RunAtLoad</key><true/>") || strings.Contains(string(data), "KeepAlive") {
		t.Fatal(string(data))
	}
	for _, ip := range []string{"1.2.3.4", "127.0.0.1", "127.1.2.3<script>"} {
		if _, err := loopbackBootPlist(ip); err == nil {
			t.Fatal(ip)
		}
	}
	oldOS, oldDir := loopbackOS, loopbackLaunchDir
	t.Cleanup(func() { loopbackOS, loopbackLaunchDir = oldOS, oldDir })
	loopbackOS = "darwin"
	loopbackLaunchDir = t.TempDir()
	for i := 0; i < 2; i++ {
		if err := installLoopbackBoot("127.77.133.203"); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(loopbackLaunchDir, "com.tailmux.loopback.127.77.133.203.plist")
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0644 {
		t.Fatal(info.Mode())
	}
	_ = os.WriteFile(path, []byte("unrelated"), 0644)
	if err := installLoopbackBoot("127.77.133.203"); err == nil {
		t.Fatal("overwrote unrelated job")
	}
}
