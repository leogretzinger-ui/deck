//go:build !linux

package workspacehost

func privateDirectory(string, int) bool  { return false }
func privateFile(string, int) bool       { return false }
func volatileDirectory(string, int) bool { return false }
