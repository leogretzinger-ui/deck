//go:build !linux

package workspaceproxy

import (
	"context"
	"errors"
	"net"
)

func dialPrivate(context.Context, string, int) (net.Conn, error) {
	return nil, errors.New("private workspace transport requires Linux kernel peer credentials")
}
