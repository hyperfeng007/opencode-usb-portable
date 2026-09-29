//go:build !windows

package main

func stdinIsConsole() bool { return false }
func waitForUser()         {}
