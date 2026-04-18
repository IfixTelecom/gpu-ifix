// Package main_test exercises the CLI dispatcher + flag parsing without
// requiring a live Postgres or Redis. End-to-end migrate → tenant → key
// integration is deferred to Plan 02-07's testcontainers suite.
package main_test

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildCLI(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "gatewayctl")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build gatewayctl: %v\n%s", err, out)
	}
	return bin
}

func TestCLI_NoArgs_ExitsUsage(t *testing.T) {
	bin := buildCLI(t)
	cmd := exec.Command(bin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit")
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("stderr missing Usage: %s", stderr.String())
	}
}

func TestCLI_Unknown_ExitsWithMessage(t *testing.T) {
	bin := buildCLI(t)
	cmd := exec.Command(bin, "nonsense")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	_ = cmd.Run()
	if !strings.Contains(stderr.String(), "unknown command: nonsense") {
		t.Errorf("stderr: %s", stderr.String())
	}
}

func TestCLI_KeyCreate_RejectsBadDataClass(t *testing.T) {
	bin := buildCLI(t)
	cmd := exec.Command(bin, "key", "create", "--tenant", "any", "--data-class", "bogus")
	// This test intentionally does NOT set AI_GATEWAY_PG_DSN — flag validation
	// happens before config.Load so we see the data-class error regardless.
	var stderr, stdout bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit on bogus data-class")
	}
	combined := stderr.String() + stdout.String()
	if !strings.Contains(combined, "data-class must be") {
		t.Errorf("output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestCLI_MigrateNoDSN_Fails(t *testing.T) {
	bin := buildCLI(t)
	cmd := exec.Command(bin, "migrate", "up")
	cmd.Env = []string{} // no env at all → config.Load surfaces missing required vars
	var stderr, stdout bytes.Buffer
	cmd.Stderr = &stderr
	cmd.Stdout = &stdout
	err := cmd.Run()
	if err == nil {
		t.Fatal("expected non-zero exit with no DSN")
	}
	combined := stderr.String() + stdout.String()
	if !(strings.Contains(combined, "AI_GATEWAY_PG_DSN") || strings.Contains(combined, "required environment variable")) {
		t.Errorf("expected DSN-related error in output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestCLI_Help_ExitsZero(t *testing.T) {
	bin := buildCLI(t)
	cmd := exec.Command(bin, "--help")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("--help should exit 0, got: %v (stderr=%s)", err, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("stderr should print usage: %s", stderr.String())
	}
}
