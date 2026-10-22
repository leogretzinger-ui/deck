package harnessbump

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"
)

// ReviewProxy permits the real Codex login and model connection without
// interpreting provider traffic or holding any credential. TLS stays end to end.
// This is destination filtering, not a restriction on upstream account actions.
type ReviewProxy struct{}

func (ReviewProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		http.Error(w, "CONNECT required", http.StatusMethodNotAllowed)
		return
	}
	if r.Host != "chatgpt.com:443" && r.Host != "auth.openai.com:443" {
		http.Error(w, "destination denied", http.StatusForbidden)
		return
	}
	host, _, _ := net.SplitHostPort(r.Host)
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		http.Error(w, "resolution failed", http.StatusBadGateway)
		return
	}
	for _, address := range addresses {
		if !reviewPublicAddress(address) {
			http.Error(w, "non-public destination", http.StatusForbidden)
			return
		}
	}
	var dialer net.Dialer
	remote, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(addresses[0].String(), "443"))
	if err != nil {
		http.Error(w, "connection failed", http.StatusBadGateway)
		return
	}
	defer remote.Close() //nolint:errcheck // Closing the tunnel cannot change its result.
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "tunneling unavailable", http.StatusInternalServerError)
		return
	}
	client, buffered, err := hijacker.Hijack()
	if err != nil {
		return
	}
	defer client.Close() //nolint:errcheck // Closing the tunnel cannot change its result.
	deadline := time.Now().Add(50 * time.Minute)
	_ = remote.SetDeadline(deadline)
	_ = client.SetDeadline(deadline)
	if _, err := buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err := buffered.Flush(); err != nil {
		return
	}
	done := make(chan struct{}, 1)
	go func() {
		_, _ = io.Copy(remote, buffered)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, remote)
		done <- struct{}{}
	}()
	<-done
}

func reviewPublicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	for _, denied := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96"} {
		if netip.MustParsePrefix(denied).Contains(address) {
			return false
		}
	}
	return true
}
