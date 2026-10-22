//go:build linux

package harnesslogin

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

type SessionIdentity struct {
	ID              string
	Generation      int
	Provider        string
	CodexExecutable string
}

// ResolveSession returns a reconciled session only for its exact pane process.
// The callback is provided by boot; caller-supplied session IDs do not route.
type ResolveSession func(context.Context, int, uint64) (SessionIdentity, bool)
type LinuxRuntime struct{ Resolve ResolveSession }
type process struct {
	pid, parent int
	start       uint64
	network     string
}

func readProcess(pid int) (process, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return process{}, ErrUnavailable
	}
	end := strings.LastIndexByte(string(b), ')')
	if end < 0 {
		return process{}, ErrUnavailable
	}
	fields := strings.Fields(string(b[end+1:]))
	if len(fields) < 20 || fields[0] == "Z" {
		return process{}, ErrUnavailable
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return process{}, ErrUnavailable
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return process{}, ErrUnavailable
	}
	network, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/net", pid))
	if err != nil {
		return process{}, ErrUnavailable
	}
	return process{pid, parent, start, network}, nil
}
func (r *LinuxRuntime) ancestors(ctx context.Context, pid int) ([]process, SessionIdentity, error) {
	var chain []process
	for range 32 {
		if ctx.Err() != nil || pid <= 1 {
			break
		}
		p, err := readProcess(pid)
		if err != nil {
			return nil, SessionIdentity{}, ErrUnavailable
		}
		chain = append(chain, p)
		if session, ok := r.Resolve(ctx, p.pid, p.start); ok {
			return chain, session, nil
		}
		pid = p.parent
	}
	return nil, SessionIdentity{}, ErrUnavailable
}

type tcpSocket struct {
	local, remote, state string
	inode                uint64
}

func tcpSockets() ([]tcpSocket, error) {
	file, err := os.Open("/proc/net/tcp")
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = file.Close() }()
	b, err := io.ReadAll(io.LimitReader(file, (4<<20)+1))
	if err != nil || len(b) > 4<<20 {
		return nil, ErrUnavailable
	}
	var out []tcpSocket
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 10 {
			continue
		}
		inode, err := strconv.ParseUint(f[9], 10, 64)
		if err != nil {
			return nil, ErrUnavailable
		}
		out = append(out, tcpSocket{f[1], f[2], f[3], inode})
	}
	return out, nil
}
func ownsSocket(pid int, inode uint64) bool {
	if inode == 0 {
		return false
	}
	entries, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", pid))
	if err != nil || len(entries) > 65536 {
		return false
	}
	want := fmt.Sprintf("socket:[%d]", inode)
	for _, entry := range entries {
		v, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", pid, entry.Name()))
		if err == nil && v == want {
			return true
		}
	}
	return false
}
func listener(port int) (uint64, error) {
	sockets, err := tcpSockets()
	if err != nil {
		return 0, err
	}
	address := fmt.Sprintf("0100007F:%04X", port)
	var inode uint64
	for _, socket := range sockets {
		if socket.state == "0A" && socket.local == address {
			if inode != 0 {
				return 0, ErrUnavailable
			}
			inode = socket.inode
		}
	}
	if inode == 0 {
		return 0, ErrUnavailable
	}
	return inode, nil
}
func (r *LinuxRuntime) Capture(ctx context.Context, peerPID, port int) (Binding, error) {
	chain, session, err := r.ancestors(ctx, peerPID)
	if err != nil {
		return Binding{}, err
	}
	current, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		return Binding{}, ErrUnavailable
	}
	inode, err := listener(port)
	if err != nil {
		return Binding{}, err
	}
	if len(chain) < 2 {
		return Binding{}, ErrUnavailable
	}
	owner, ok := nativeOwner(chain[1:], session)
	if !ok || owner.network != current || !ownsSocket(owner.pid, inode) {
		return Binding{}, ErrUnavailable
	}
	binding := Binding{Session: session.ID, Generation: session.Generation, Provider: session.Provider, PID: owner.pid, StartTicks: owner.start, Listener: inode, Network: current}
	if !r.Alive(ctx, binding) {
		return Binding{}, ErrUnavailable
	}
	return binding, nil
}

// Pi owns its listener in the reconciled pane process. The reviewed Codex npm
// launcher spawns one direct native child; only the qualified binary may own
// that listener. A tool child inheriting BROWSER must not receive callbacks.
func nativeOwner(chain []process, session SessionIdentity) (process, bool) {
	if len(chain) == 0 {
		return process{}, false
	}
	root := chain[len(chain)-1]
	if session.Provider != "codex" {
		return root, true
	}
	if session.CodexExecutable == "" {
		return process{}, false
	}
	if processExecutable(root.pid, session.CodexExecutable) {
		return root, true
	}
	if len(chain) >= 2 {
		child := chain[len(chain)-2]
		if child.parent == root.pid && processExecutable(child.pid, session.CodexExecutable) {
			return child, true
		}
	}
	return process{}, false
}

func processExecutable(pid int, expected string) bool {
	want, err := os.Stat(expected)
	if err != nil {
		return false
	}
	actual, err := os.Stat(fmt.Sprintf("/proc/%d/exe", pid))
	return err == nil && os.SameFile(want, actual)
}

func (r *LinuxRuntime) Alive(ctx context.Context, b Binding) bool {
	chain, session, err := r.ancestors(ctx, b.PID)
	current, networkErr := os.Readlink("/proc/self/ns/net")
	owner, ok := nativeOwner(chain, session)
	return networkErr == nil && current == b.Network && err == nil && ok && owner.pid == b.PID && owner.start == b.StartTicks && owner.network == b.Network && session.ID == b.Session && session.Generation == b.Generation && session.Provider == b.Provider && ownsSocket(b.PID, b.Listener)
}

func tcpAddress(a net.Addr) string {
	t, ok := a.(*net.TCPAddr)
	if !ok {
		return ""
	}
	ip := t.IP.To4()
	if ip == nil {
		return ""
	}
	return fmt.Sprintf("%02X%02X%02X%02X:%04X", ip[3], ip[2], ip[1], ip[0], t.Port)
}
func (r *LinuxRuntime) Dial(ctx context.Context, b Binding, port int) (net.Conn, error) {
	if !r.Alive(ctx, b) {
		return nil, ErrUnavailable
	}
	inode, err := listener(port)
	if err != nil || inode != b.Listener {
		return nil, ErrUnavailable
	}
	conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return nil, ErrUnavailable
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = conn.Close()
		}
	}()
	// TCP connect can finish before the CLI calls accept. Wait only for kernel
	// descriptor evidence; send no probe, HTTP header, or callback bytes.
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		sockets, err := tcpSockets()
		if err != nil {
			return nil, ErrUnavailable
		}
		for _, socket := range sockets {
			if socket.state == "01" && socket.local == tcpAddress(conn.RemoteAddr()) && socket.remote == tcpAddress(conn.LocalAddr()) && ownsSocket(b.PID, socket.inode) {
				if !r.Alive(ctx, b) {
					return nil, ErrUnavailable
				}
				accepted = true
				return conn, nil
			}
		}
		select {
		case <-ctx.Done():
			return nil, ErrUnavailable
		case <-timer.C:
			return nil, ErrUnavailable
		case <-tick.C:
		}
	}
}
