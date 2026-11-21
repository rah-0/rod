package launcher

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/rah-0/rod/lib/launcher/internal/guardian"
)

func TestGuardianRejectsForeignParentExecutable(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	copy := filepath.Join(t.TempDir(), "foreign-executable")
	if err := os.WriteFile(copy, binary, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	profile := filepath.Join(parent, "profile")
	if err := os.Mkdir(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	input, owner, err := guardianChannel()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	_ = owner.Close()
	reports, status, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reports.Close()
	defer status.Close()
	profileParent, err := os.Open(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer profileParent.Close()
	cmd := exec.CommandContext(t.Context(), copy, guardian.Argument, "owned", "profile")
	cmd.Env = append(withoutGuardianEnv(os.Environ()), guardian.Env+"=1")
	cmd.Stdin = input
	cmd.ExtraFiles = []*os.File{status, profileParent}
	if err := cmd.Run(); err == nil {
		t.Fatal("forged private invocation unexpectedly succeeded")
	}
	_ = status.Close()
	if data, err := io.ReadAll(reports); err != nil || len(data) != 0 {
		t.Fatalf("forged guardian entered private mode: %q, %v", data, err)
	}
	if _, err := os.Stat(profile); err != nil {
		t.Fatalf("forged guardian changed profile: %v", err)
	}
}

func TestLocalProfileCleanupUsesOpenedParent(t *testing.T) {
	base := t.TempDir()
	parent := filepath.Join(base, "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(parent, "profile")
	if err := os.Mkdir(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	l := New()
	l.cleanupUserDataDir = profile
	cmd := exec.Command("sleep", "60")
	_, stop, err := l.startProcess(cmd)
	if err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		stop()
		if !waited {
			_ = cmd.Wait()
		}
		_ = l.cleanupParent.Close()
	})
	moved := filepath.Join(base, "moved")
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	victim := t.TempDir()
	if err := os.Mkdir(filepath.Join(victim, "profile"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, parent); err != nil {
		t.Fatal(err)
	}
	stop()
	err = cmd.Wait()
	waited = true
	if err != nil {
		t.Fatal(err)
	}
	if err := l.removeUserDataDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(moved, "profile")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned profile remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(victim, "profile")); err != nil {
		t.Fatalf("cleanup followed replaced parent: %v", err)
	}
}

func TestGuardianChallengeDelegate(t *testing.T) {
	if os.Getenv("ROD_GUARDIAN_CHALLENGE_DELEGATE") != "1" {
		return
	}
	command := os.NewFile(3, "command")
	status := os.NewFile(4, "status")
	defer command.Close()
	defer status.Close()
	if err := guardian.AnswerChallenge(status, command); err != nil {
		t.Fatal(err)
	}
}

func TestGuardianRejectsDelegatedChallenge(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	profile := filepath.Join(parent, "profile")
	if err := os.Mkdir(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	input, owner, err := guardianChannel()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer owner.Close()
	reports, status, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reports.Close()
	defer status.Close()
	profileParent, err := os.Open(parent)
	if err != nil {
		t.Fatal(err)
	}
	defer profileParent.Close()
	cmd := exec.CommandContext(ctx, self, guardian.Argument, "owned", "profile")
	cmd.Env = guardianRaceEnv(append(withoutGuardianEnv(os.Environ()), guardian.Env+"=1"))
	cmd.Stdin = input
	cmd.ExtraFiles = []*os.File{status, profileParent}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if !waited {
			_ = cmd.Wait()
		}
	})
	_ = status.Close()
	ready, err := guardian.ReadStatus(reports)
	if err != nil || !ready.Ready {
		t.Fatalf("guardian ready: %+v, %v", ready, err)
	}
	delegate := exec.CommandContext(ctx, self, "-test.run=^TestGuardianChallengeDelegate$")
	delegate.Env = guardianRaceEnv(append(withoutGuardianEnv(os.Environ()), "ROD_GUARDIAN_CHALLENGE_DELEGATE=1"))
	delegate.ExtraFiles = []*os.File{owner, reports}
	if output, err := delegate.CombinedOutput(); err != nil {
		t.Fatalf("delegated challenge: %v\n%s", err, output)
	}
	failure, err := guardian.ReadStatus(reports)
	if err != nil || failure.Error != "guardian challenge answered by another process" {
		t.Fatalf("delegated response was accepted: %+v, %v", failure, err)
	}
	err = cmd.Wait()
	waited = true
	if err == nil {
		t.Fatal("unauthenticated guardian succeeded")
	}
	if _, err := os.Stat(profile); err != nil {
		t.Fatalf("unauthenticated guardian changed profile: %v", err)
	}
}
