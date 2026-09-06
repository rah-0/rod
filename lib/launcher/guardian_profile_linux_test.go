package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestGuardianManagedProfileCleanup(t *testing.T) {
	if os.Getenv(guardianTestRole) != "harness" {
		dir := t.TempDir()
		cmd := guardianTestCommand(t, "harness", dir)
		cmd.Args[1] = "-test.run=^TestGuardianManagedProfileCleanup$"
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("managed profile harness: %v\n%s", err, output)
		}
		return
	}
	if err := guardianSubreaper(); err != nil {
		t.Fatal(err)
	}
	dir := os.Getenv("ROD_GUARDIAN_TEST_DIR")
	actual := filepath.Join(dir, "actual")
	victim := filepath.Join(dir, "victim")
	alias := filepath.Join(dir, "profiles")
	for _, path := range []string{actual, victim} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	cmd := guardianTestCommand(t, "owner", dir)
	cmd.Args[1] = "-test.run=^TestGuardianManagedProfileOwner$"
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		if !waited {
			_ = cmd.Wait()
		}
		guardianReapChildren(nil)
	})
	pids := guardianTestPIDs(t, dir)
	var name string
	guardianTestEventually(t, func() bool {
		data, err := os.ReadFile(filepath.Join(dir, "managed-profile"))
		name = string(data)
		return err == nil && name != ""
	})
	if err := os.Remove(alias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, alias); err != nil {
		t.Fatal(err)
	}
	victimProfile := filepath.Join(victim, name)
	if err := os.Mkdir(victimProfile, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(victimProfile, "keep")
	if err := os.WriteFile(sentinel, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("owner unexpectedly exited successfully")
	}
	waited = true
	guardianTestEventually(t, func() bool {
		_, err := os.Stat(filepath.Join(actual, name))
		return errors.Is(err, os.ErrNotExist)
	})
	for _, pid := range pids[:2] {
		if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
			t.Errorf("managed browser process %d remains: %v", pid, err)
		}
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("guardian cleanup escaped through a repointed symlink: %v", err)
	}
}

func TestGuardianManagedProfileOwner(t *testing.T) {
	if os.Getenv(guardianTestRole) != "owner" {
		return
	}
	dir := os.Getenv("ROD_GUARDIAN_TEST_DIR")
	manager := NewManager(managerTestToken)
	manager.userDataRoot = filepath.Join(dir, "profiles")
	profile, err := manager.newManagedProfile()
	if err != nil {
		t.Fatal(err)
	}
	l := New()
	l.managedProfile = profile
	cmd := guardianTestCommand(t, "browser", dir)
	if _, _, err := l.startProcess(cmd); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "managed-profile"), []byte(profile.name), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}
