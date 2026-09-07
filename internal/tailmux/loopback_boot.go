package tailmux

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
)

var loopbackLaunchDir = "/Library/LaunchDaemons"

// Run only Apple's fixed system binary at boot, never a user-writable Tailmux executable.
func loopbackBootPlist(ip string) ([]byte, error) {
	if err := validateLoopbackSystem(ip, "boot.test"); err != nil {
		return nil, err
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>com.tailmux.loopback.%s</string>
<key>ProgramArguments</key><array>
<string>/sbin/ifconfig</string><string>lo0</string><string>alias</string>
<string>%s</string><string>netmask</string><string>255.255.255.255</string><string>up</string>
</array>
<key>RunAtLoad</key><true/>
</dict></plist>
`, ip, ip)), nil
}
func installLoopbackBoot(ip string) error {
	if loopbackOS != "darwin" {
		return nil
	}
	data, err := loopbackBootPlist(ip)
	if err != nil {
		return err
	}
	path := filepath.Join(loopbackLaunchDir, "com.tailmux.loopback."+ip+".plist")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing nonregular launchd file %s", path)
		}
		old, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if !bytes.Equal(old, data) {
			return fmt.Errorf("existing launchd job differs: %s", path)
		}
		if os.Geteuid() == 0 {
			if err := os.Chown(path, 0, 0); err != nil {
				return err
			}
		}
		return os.Chmod(path, 0644)
	} else if !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(loopbackLaunchDir, ".tailmux-loopback-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Chmod(0644); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
