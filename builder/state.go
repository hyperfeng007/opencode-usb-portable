package main

// Build state + console helpers.
//
// data\build-state.json lets the launcher see what the builder is doing:
//   starting -> written by the launcher before it spawns the builder
//   running  -> written by the builder itself, right after it starts
//   failed   -> written by the builder when it gives up (with a reason)
//   ok       -> written when the build finished successfully
//
// Because the launcher can read this file, a failed or never-started build can
// never be a silent "flash" any more: the launcher reports it in a dialog with
// the tail of data\build.log.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type buildState struct {
	Status    string `json:"status"`
	PID       int    `json:"pid"`
	Builder   string `json:"builder"`
	Stage     string `json:"stage,omitempty"`
	Progress  int    `json:"progress,omitempty"`
	StartedAt string `json:"startedAt,omitempty"`
	Updated   string `json:"updated"`
	Reason    string `json:"reason,omitempty"`
}

var currentRoot string

func statePath(root string) string {
	return filepath.Join(root, "data", "build-state.json")
}

// readBuildState returns the recorded state, if any.
func readBuildState(root string) (buildState, bool) {
	var state buildState
	data, err := os.ReadFile(statePath(root))
	if err != nil {
		return state, false
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, false
	}
	return state, true
}

func updatedAt(state buildState) time.Time {
	parsed, err := time.Parse(time.RFC3339, state.Updated)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

// ---------------------------------------------------------------------------
// live progress reporting for the launcher's progress bar
// ---------------------------------------------------------------------------

var (
	stageText       string
	stageFrom       int
	stageTo         int
	lastWrite       time.Time
	lastPercent     = -1
	stateWriteEvery = 700 * time.Millisecond
)

// reportStage announces a new stage ("正在下载 opencode") and anchors the
// progress range that this stage fills. Every stage starts at its own "from".
func reportStage(stage string, from, to int) {
	stageText, stageFrom, stageTo = stage, from, to
	lastPercent = -1
	writeProgress(from, true)
}

// reportFraction updates the bar inside the current stage, throttled so the
// state file is not rewritten on every chunk of a download.
func reportFraction(frac float64) {
	if stageText == "" {
		return
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	percent := stageFrom + int(frac*float64(stageTo-stageFrom))
	if percent == lastPercent {
		return
	}
	writeProgress(percent, false)
}

// reportDone marks the stage as fully finished.
func reportDone() {
	lastPercent = -1
	writeProgress(stageTo, true)
}

func writeProgress(percent int, force bool) {
	if currentRoot == "" || stageText == "" {
		return
	}
	if !force && time.Since(lastWrite) < stateWriteEvery {
		return
	}
	lastPercent = percent
	lastWrite = time.Now()
	writeState(currentRoot, "running", "", stageText, percent)
}

// writeBuildState stores the state, keeping StartedAt from a previous record.
func writeBuildState(root string, status string, reason string) {
	writeState(root, status, reason, "", 0)
}

func writeState(root string, status string, reason string, stage string, progress int) {
	if root == "" {
		return
	}
	now := time.Now().Format(time.RFC3339)
	state := buildState{
		Status:   status,
		PID:      os.Getpid(),
		Builder:  appVersion,
		Stage:    stage,
		Progress: progress,
		Updated:  now,
	}
	if previous, ok := readBuildState(root); ok && previous.StartedAt != "" {
		state.StartedAt = previous.StartedAt
	}
	if status == "running" || status == "starting" {
		state.StartedAt = now
	}
	if reason != "" {
		state.Reason = reason
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(statePath(root)), 0o755); err != nil {
		return
	}
	tmp := statePath(root) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err == nil {
		_ = os.Rename(tmp, statePath(root))
	}
}

// tailOfLog returns the last n lines of data\build.log (for error dialogs).
func tailOfLog(root string, n int) string {
	data, err := os.ReadFile(filepath.Join(root, "data", "build.log"))
	if err != nil {
		return ""
	}
	lines := 0
	for i := len(data) - 1; i >= 0; i-- {
		if data[i] == '\n' {
			lines++
			if lines > n {
				return string(data[i+1:])
			}
		}
	}
	return string(data)
}

var _ = fmt.Sprintf
