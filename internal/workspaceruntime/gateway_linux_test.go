//go:build linux

package workspaceruntime

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type ingressFixture struct {
	app, sidecar, transport string
	inspectFailure          bool
}

func (f ingressFixture) run(_ context.Context, _ string, args, _ []string, input []byte, _ bool) ([]byte, error) {
	if len(input) != 0 {
		return nil, errors.New("unexpected private input")
	}
	if args[0] == "ps" {
		return []byte(strings.Repeat("a", 64)), nil
	}
	if len(args) != 4 || args[0] != "inspect" {
		return nil, errors.New("unexpected native command")
	}
	if f.inspectFailure {
		return nil, errors.New("synthetic observation failure")
	}
	if args[2] == gatewayTransportFormat {
		return []byte(f.transport), nil
	}
	if args[3] == "installation-app-1" {
		return []byte(f.app), nil
	}
	return []byte(f.sidecar), nil
}

func TestGatewayNativeIngressRefusesPublishedOrUnknownRoutes(t *testing.T) {
	closed := `{"ports":{"8837/tcp":null},"network":"bridge"}`
	for _, tc := range []struct {
		name    string
		change  func(*ingressFixture)
		allowed bool
	}{
		{"private", func(*ingressFixture) {}, true},
		{"backend published", func(f *ingressFixture) { f.app = `{"ports":{"8837/tcp":[{"HostPort":"18837"}]},"network":"bridge"}` }, false},
		{"sidecar published", func(f *ingressFixture) { f.sidecar = `{"ports":{"8766/tcp":[{}]},"network":"bridge"}` }, false},
		{"host network", func(f *ingressFixture) { f.app = `{"ports":{},"network":"host"}` }, false},
		{"external namespace", func(f *ingressFixture) { f.sidecar = `{"ports":{},"network":"ns:/run/netns/outside"}` }, false},
		{"ports missing", func(f *ingressFixture) { f.app = `{"network":"bridge"}` }, false},
		{"observation missing", func(f *ingressFixture) { f.inspectFailure = true }, false},
		{"tcp setting not overridden", func(f *ingressFixture) { f.transport = "spca" }, false},
		{"duplicate setting", func(f *ingressFixture) { f.transport = "bbspca" }, false},
		{"wrong auth mode", func(f *ingressFixture) { f.transport = "bspc" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ingressFixture{app: closed, sidecar: closed, transport: "bspca"}
			tc.change(&f)
			c := Controller{cfg: Config{AppContainer: "installation-app-1", ComposeProject: "installation"}, run: f}
			if err := c.verifyGatewayIngress(context.Background()); (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, err)
			}
		})
	}
}
