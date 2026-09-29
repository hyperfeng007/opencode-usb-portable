//go:build !windows

package main

// Non-Windows builds have no Win32 progress window; the same orchestration code
// is used for the automated tests, with a silent stand-in.

type progressUI struct{}

func startProgress(hint string) *progressUI { return &progressUI{} }

func (ui *progressUI) SetStatus(status string, percent int) {}
func (ui *progressUI) SetHint(hint string)                  {}
func (ui *progressUI) Fail(reason string)                   {}
func (ui *progressUI) Close()                               {}
