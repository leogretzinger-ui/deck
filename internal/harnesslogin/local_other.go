//go:build !linux

package harnesslogin

type LocalServer struct{}

func (*LocalServer) Close() error { return nil }
func BrowserOpener([]string) int  { return 1 }
