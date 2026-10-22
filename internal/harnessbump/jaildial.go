package harnessbump

import (
	"context"
	"io"
	"net"
)

// JailDial is the jail's only way out: a listener on the jail's loopback
// that forwards each connection to the broker's unix socket, which is
// bind-mounted in. The jail has no network namespace beyond loopback, so
// nothing else leaves it: no route, no resolver, no query.
func JailDial(ctx context.Context, ln net.Listener, socket string) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			// Closed by the cancel above: the context's error says so.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		go func() {
			defer func() { _ = conn.Close() }()
			out, err := net.Dial("unix", socket)
			if err != nil {
				return
			}
			defer func() { _ = out.Close() }()
			done := make(chan struct{}, 2)
			go func() { _, _ = io.Copy(out, conn); done <- struct{}{} }()
			go func() { _, _ = io.Copy(conn, out); done <- struct{}{} }()
			<-done
		}()
	}
}
