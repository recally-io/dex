package engine

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

func mcpOAuthURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || strings.Contains(raw, "#") || strings.Contains(raw, "\\") {
		return nil, errors.New("invalid MCP OAuth URL")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !mcpOAuthLoopback(u.Hostname())) {
		return nil, errors.New("MCP OAuth requires HTTPS outside loopback")
	}
	return u, nil
}

func mcpOAuthLoopback(host string) bool {
	return strings.EqualFold(host, "localhost") || host == "127.0.0.1" || host == "::1"
}

func mcpSameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host)
}

var mcpOAuthDirect = &http.Client{Transport: mcpOAuthTransport(false), CheckRedirect: mcpOAuthNoRedirect}
var mcpOAuthPublic = &http.Client{Transport: mcpOAuthTransport(true), CheckRedirect: mcpOAuthNoRedirect}

func mcpOAuthNoRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

func mcpOAuthTransport(publicOnly bool) *http.Transport {
	transport := &http.Transport{
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		MaxIdleConns:          32,
		ForceAttemptHTTP2:     true,
	}
	dial := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport.DialContext = dial.DialContext
	if publicOnly {
		// Check the IP that the socket uses, not a separate DNS check before the dial.
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			for _, resolved := range ips {
				ip := resolved.Unmap()
				if !ip.IsGlobalUnicast() || ip.IsPrivate() || !mcpPublicIP(ip) {
					continue
				}
				conn, dialErr := dial.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				err = dialErr
			}
			if err != nil {
				return nil, err
			}
			return nil, errors.New("MCP OAuth discovery cannot access a private endpoint on another origin")
		}
	}
	return transport
}

var mcpNonPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("2002::/16"),
}

func mcpPublicIP(ip netip.Addr) bool {
	for _, prefix := range mcpNonPublic {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

type mcpOAuthFetch struct{ resource *url.URL }

func (f mcpOAuthFetch) Do(req *http.Request) (*http.Response, error) {
	endpoint, err := mcpOAuthURL(req.URL.String())
	if err != nil {
		return nil, err
	}
	client := mcpOAuthPublic
	if mcpSameOrigin(endpoint, f.resource) {
		client = mcpOAuthDirect
	} else if endpoint.Scheme != "https" || mcpOAuthLoopback(endpoint.Hostname()) {
		return nil, errors.New("MCP OAuth requires a public HTTPS endpoint on another origin")
	}
	resp, err := client.Do(req) //nolint:bodyclose,gosec // The SDK closes the body. Cross-origin dials check the actual destination IP.
	if err != nil {
		return nil, err
	}
	resp.Body = &mcpLimitedBody{Reader: io.LimitReader(resp.Body, 1<<20), Closer: resp.Body}
	return resp, nil
}

type mcpLimitedBody struct {
	io.Reader
	io.Closer
}

// mcpAuthChallenges separates schemes in a combined RFC 9110 field.
// A comma before a name followed by '=' separates parameters instead.
func mcpAuthChallenges(header string) []string {
	var fields []string
	start := 0
	quoted, escaped := false, false
	for i := range len(header) {
		switch ch := header[i]; {
		case escaped:
			escaped = false
		case quoted && ch == '\\':
			escaped = true
		case ch == '"':
			quoted = !quoted
		case ch == ',' && !quoted:
			next := strings.TrimLeft(header[i+1:], " \t")
			end := strings.IndexAny(next, " \t=,")
			if end >= 0 && strings.HasPrefix(strings.TrimLeft(next[end:], " \t"), "=") {
				continue
			}
			fields = append(fields, header[start:i])
			start = i + 1
		}
	}
	return append(fields, header[start:])
}
