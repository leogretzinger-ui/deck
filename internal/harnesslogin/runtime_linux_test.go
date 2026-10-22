//go:build linux

package harnesslogin

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"
)

// Subprocesses and ephemeral listeners exercise actual /proc ownership. No
// native login port, provider home, tmux namespace or real user session is used.
func TestRuntimeChild(t *testing.T) {
	switch os.Getenv("DECK_LOGIN_TEST_CHILD") {
	case "idle":
		fmt.Fprintln(os.Stdout, "ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		os.Exit(0)
	case "hostile":
		listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			os.Exit(2)
		}
		opener := exec.Command(os.Args[0], "-test.run=^TestRuntimeChild$")
		opener.Env = append(os.Environ(), "DECK_LOGIN_TEST_CHILD=idle")
		input, err := opener.StdinPipe()
		if err != nil || opener.Start() != nil {
			os.Exit(3)
		}
		fmt.Fprintln(os.Stdout, "ready")
		fmt.Fprintln(os.Stdout, listener.Addr().(*net.TCPAddr).Port, opener.Process.Pid)
		_ = listener.SetDeadline(time.Now().Add(time.Second))
		conn, err := listener.Accept()
		var received int64
		if err == nil {
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			received, _ = io.Copy(io.Discard, conn)
			_ = conn.Close()
		}
		fmt.Fprintln(os.Stdout, received)
		_ = input.Close()
		_ = opener.Wait()
		_ = listener.Close()
		os.Exit(0)
	case "accept":
		file := os.NewFile(3, "listener")
		listener, err := net.FileListener(file)
		if err != nil {
			os.Exit(2)
		}
		_ = file.Close()
		fmt.Fprintln(os.Stdout, "ready")
		conn, err := listener.Accept()
		if err != nil {
			os.Exit(3)
		}
		_ = conn.SetReadDeadline(time.Now().Add(4 * time.Second))
		n, _ := io.Copy(io.Discard, conn)
		fmt.Fprintln(os.Stdout, n)
		_ = conn.Close()
		_ = listener.Close()
		os.Exit(0)
	}
}

func runtimeChild(t *testing.T, mode string, files ...*os.File) (*exec.Cmd, *bufio.Reader) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestRuntimeChild$")
	cmd.Env = append(os.Environ(), "DECK_LOGIN_TEST_CHILD="+mode)
	cmd.ExtraFiles = files
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { input.Close(); cmd.Process.Kill(); cmd.Wait() })
	reader := bufio.NewReader(output)
	if ready, err := reader.ReadString('\n'); err != nil || ready != "ready\n" {
		t.Fatal("child did not start")
	}
	return cmd, reader
}

func realRuntime(t *testing.T) *LinuxRuntime {
	t.Helper()
	p, err := readProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	return &LinuxRuntime{Resolve: func(_ context.Context, pid int, start uint64) (SessionIdentity, bool) {
		return SessionIdentity{ID: "isolated-test-session", Generation: 1, Provider: "pi"}, pid == p.pid && start == p.start
	}}
}

func testListener(t *testing.T) *net.TCPListener {
	t.Helper()
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener
}

func TestKernelBindingAndAcceptedPeer(t *testing.T) {
	rt := realRuntime(t)
	listener := testListener(t)
	port := listener.Addr().(*net.TCPAddr).Port
	child, _ := runtimeChild(t, "idle")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b, err := rt.Capture(ctx, child.Process.Pid, port)
	if err != nil || b.PID != os.Getpid() || b.Session != "isolated-test-session" {
		t.Fatalf("capture: %v", err)
	}
	if _, err = rt.Capture(ctx, os.Getpid(), port); err == nil {
		t.Fatal("opener nominated its own listener")
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	conn, err := rt.Dial(ctx, b, port)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	(<-accepted).Close()
	for _, changed := range []Binding{
		func() Binding { v := b; v.StartTicks++; return v }(),
		func() Binding { v := b; v.Generation++; return v }(),
		func() Binding { v := b; v.Network = "different-network"; return v }(),
		func() Binding { v := b; v.Listener++; return v }(),
		func() Binding { v := b; v.Session = "other-session"; return v }(),
	} {
		if conn, err := rt.Dial(ctx, changed, port); err == nil {
			conn.Close()
			t.Fatal("changed binding accepted")
		}
	}
	listener.Close()
	if rt.Alive(ctx, b) {
		t.Fatal("closed listener remained live")
	}
}

func TestAcceptedSocketInAnotherProcessReceivesNoBytes(t *testing.T) {
	rt := realRuntime(t)
	listener := testListener(t)
	file, err := listener.File()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	child, output := runtimeChild(t, "accept", file)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	port := listener.Addr().(*net.TCPAddr).Port
	b, err := rt.Capture(ctx, child.Process.Pid, port)
	if err != nil {
		t.Fatal(err)
	}
	if conn, err := rt.Dial(ctx, b, port); err == nil {
		conn.Close()
		t.Fatal("callback could reach a different process")
	}
	if line, err := output.ReadString('\n'); err != nil || line != "0\n" {
		t.Fatal("unverified accepted socket received bytes")
	}
}

func TestListenerOwningDescendantCannotRegisterOrReceiveCallback(t *testing.T) {
	for _, provider := range []string{"pi", "codex"} {
		t.Run(provider, func(t *testing.T) {
			rt := realRuntime(t)
			resolve := rt.Resolve
			rt.Resolve = func(ctx context.Context, pid int, start uint64) (SessionIdentity, bool) {
				identity, ok := resolve(ctx, pid, start)
				identity.Provider = provider
				identity.CodexExecutable = "/bin/true"
				return identity, ok
			}
			child, output := runtimeChild(t, "hostile")
			var port, openerPID int
			if _, err := fmt.Fscanln(output, &port, &openerPID); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			if _, err := rt.Capture(ctx, openerPID, port); err == nil {
				t.Fatal("listener-owning tool child registered as native CLI")
			}
			p, err := readProcess(child.Process.Pid)
			if err != nil {
				t.Fatal(err)
			}
			inode, err := listener(port)
			if err != nil {
				t.Fatal(err)
			}
			forged := Binding{Session: "isolated-test-session", Generation: 1, Provider: provider, PID: p.pid, StartTicks: p.start, Listener: inode, Network: p.network}
			if conn, err := rt.Dial(ctx, forged, port); err == nil {
				conn.Close()
				t.Fatal("tool child received callback transport")
			}
			if line, err := output.ReadString('\n'); err != nil || line != "0\n" {
				t.Fatal("listener-owning descendant received bytes")
			}

		})
	}
}
