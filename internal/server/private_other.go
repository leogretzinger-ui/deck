//go:build !linux

package server

import (
	"errors"
	"net"
)

func listenPrivate(string, int) (net.Listener, error) {
	return nil, errors.New("server: private gateway sockets require Linux peer credentials")
}
