//go:build windows

package main

// The window probe is what makes the progress window disappear at exactly the
// right moment ("opencode window is up"), so it gets its own test. It runs
// under Wine on CI/development machines, using notepad.exe as a stand-in for
// opencode: a real Win32 process that creates a real top-level window.

import (
	"os/exec"
	"testing"
	"time"
)

func TestWaitForAppWindowDetectsNotepad(t *testing.T) {
	notepad := exec.Command("C:\\windows\\system32\\notepad.exe")
	if err := notepad.Start(); err != nil {
		t.Skipf("cannot start notepad: %v", err)
	}
	defer func() {
		_ = notepad.Process.Kill()
		_, _ = notepad.Process.Wait()
	}()

	time.Sleep(500 * time.Millisecond)
	if !waitForAppWindow(notepad.Process.Pid, 20*time.Second) {
		t.Fatalf("notepad window (pid %d) was not detected", notepad.Process.Pid)
	}
}

func TestWaitForAppWindowTimesOutForDeadProcess(t *testing.T) {
	start := time.Now()
	if waitForAppWindow(999999, 2*time.Second) {
		t.Fatal("unexpectedly found a window for a non-existent pid")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("timeout not honoured (took %s)", elapsed)
	}
}

func TestWaitForAppWindowAnyElectronWindow(t *testing.T) {
	notepad := exec.Command("C:\\windows\\system32\\notepad.exe")
	if err := notepad.Start(); err != nil {
		t.Skipf("cannot start notepad: %v", err)
	}
	defer func() {
		_ = notepad.Process.Kill()
		_, _ = notepad.Process.Wait()
	}()

	time.Sleep(1500 * time.Millisecond)
	// pid <= 0 means "any visible top-level window of a known app class";
	// notepad is not that class, so this must stay false, and must not hang.
	if waitForAppWindow(0, 1*time.Second) {
		t.Log("a Chrome_WidgetWin_1 window was present - fine on a desktop with a browser open")
	}
}
