//go:build linux

package main

import (
	"strings"
	"testing"
)

func TestUnitFilePath(t *testing.T) {
	mgr := NewServiceManager("test-service", false).(*systemdManager)

	// User service path
	path := mgr.unitFilePath()
	if !strings.Contains(path, ".config/systemd/user/test-service.service") {
		t.Errorf("unexpected user unit path: %s", path)
	}

	// System service path
	sysMgr := NewServiceManager("test-service", true).(*systemdManager)
	sysPath := sysMgr.unitFilePath()
	if sysPath != "/etc/systemd/system/test-service.service" {
		t.Errorf("unexpected system unit path: %s", sysPath)
	}
}

func TestParseSystemctlShow(t *testing.T) {
	input := "ActiveState=active\nSubState=running\nMainPID=1234\nActiveEnterTimestamp=Wed 2026-03-05 15:00:00 EST\n"
	props := parseSystemctlShow(input)

	if props["ActiveState"] != "active" {
		t.Errorf("ActiveState = %q, want %q", props["ActiveState"], "active")
	}
	if props["MainPID"] != "1234" {
		t.Errorf("MainPID = %q, want %q", props["MainPID"], "1234")
	}
	if props["ActiveEnterTimestamp"] != "Wed 2026-03-05 15:00:00 EST" {
		t.Errorf("ActiveEnterTimestamp = %q", props["ActiveEnterTimestamp"])
	}
}
