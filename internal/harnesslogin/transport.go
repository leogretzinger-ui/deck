package harnesslogin

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"
)

const maxResponseBytes = 64 << 10

// exchange uses a fresh, already peer-verified connection. In particular it
// never uses DefaultTransport, proxy environment variables, or a cookie jar.
func exchange(ctx context.Context, conn net.Conn, authority, target string) (int, string, error) {
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if ctx.Err() != nil {
		return 0, "", ErrUnavailable
	}
	deadline := time.Now().Add(20 * time.Second)
	if end, ok := ctx.Deadline(); ok && end.Before(deadline) {
		deadline = end
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return 0, "", ErrUnavailable
	}
	u, err := url.Parse("http://" + authority + target)
	if err != nil {
		return 0, "", ErrInvalid
	}
	req := &http.Request{Method: http.MethodGet, URL: u, Host: authority, Header: make(http.Header), Close: true}
	if err = req.Write(conn); err != nil {
		return 0, "", ErrUnavailable
	}
	bounded := &io.LimitedReader{R: conn, N: maxResponseBytes + 1}
	response, err := http.ReadResponse(bufio.NewReader(bounded), req)
	if err != nil {
		return 0, "", ErrUnavailable
	}
	defer func() { _ = response.Body.Close() }()
	// No response body or transport error is ever returned to a caller. Bounds
	// apply even to chunked or EOF-delimited responses from a replaced listener.
	n, err := io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || n > maxResponseBytes || bounded.N <= 0 {
		return 0, "", ErrUnavailable
	}
	locations := response.Header.Values("Location")
	if len(locations) > 1 {
		return 0, "", ErrUnavailable
	}
	return response.StatusCode, response.Header.Get("Location"), nil
}

func deliver(ctx context.Context, runtime Runtime, binding Binding, flow Flow, cb callback, target string) (string, error) {
	if flow != CodexBrowser && flow != PiOpenAI && flow != PiAnthropic {
		return "", ErrUnavailable
	}
	conn, err := runtime.Dial(ctx, binding, cb.port())
	if err != nil {
		return "", ErrUnavailable
	}
	status, location, err := exchange(ctx, conn, cb.authority, target)
	if err != nil {
		return "", ErrUnavailable
	}
	if flow == PiOpenAI || flow == PiAnthropic {
		if status == http.StatusOK && location == "" {
			return "delivered", nil
		}
		return "", ErrUnavailable
	}
	if status != http.StatusFound {
		return "", ErrUnavailable
	}
	// Codex's native local success redirect can contain an ID token. Its
	// reviewed /success handler needs none of that query to finish the loop.
	// Hosted/unknown redirects fail closed; no redirect authority is fetched.
	u, err := strictURL(location)
	if err != nil || u.Scheme != "http" || u.Host != cb.authority || u.Path != "/success" {
		return "", ErrUnavailable
	}
	conn, err = runtime.Dial(ctx, binding, cb.port())
	if err != nil {
		return "", ErrUnavailable
	}
	status, location, err = exchange(ctx, conn, cb.authority, "/success")
	if err != nil || status != http.StatusOK || location != "" {
		return "", ErrUnavailable
	}
	return "completed", nil
}
