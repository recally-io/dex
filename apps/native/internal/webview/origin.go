package webview

import (
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

// CanonicalOrigin normalizes a server address. Only macOS accepts loopback HTTP.
func CanonicalOrigin(input string, mac bool) (string, bool) {
	input = strings.TrimSpace(input)
	u, err := url.Parse(input)
	if err != nil || u.Host == "" || u.Opaque != "" || u.User != nil ||
		(u.EscapedPath() != "" && u.EscapedPath() != "/") || strings.ContainsAny(input, "?#") ||
		strings.HasSuffix(u.Host, ":") {
		return "", false
	}

	host := strings.ToLower(u.Hostname())
	macLoopback := mac && u.Scheme == "http" && host == "127.0.0.1"
	if u.Scheme != "https" && !macLoopback {
		return "", false
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if ip.Zone() != "" || ip.Is4In6() || ip.Is6() != strings.HasPrefix(u.Host, "[") {
			return "", false
		}
		host = ip.String()
	} else if strings.HasPrefix(u.Host, "[") || !isDNSHost(host) {
		return "", false
	}

	if raw := u.Port(); raw != "" {
		port, err := strconv.ParseUint(raw, 10, 16)
		if err != nil || port == 0 {
			return "", false
		}
		defaultPort := u.Scheme == "https" && port == 443 || u.Scheme == "http" && port == 80
		if !defaultPort {
			return u.Scheme + "://" + net.JoinHostPort(host, strconv.FormatUint(port, 10)), true
		}
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return u.Scheme + "://" + host, true
}

func isDNSHost(host string) bool {
	if len(host) > 253 {
		return false
	}
	for label := range strings.SplitSeq(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			default:
				return false
			}
		}
	}
	// WebKit can interpret a numeric final label as an IPv4 address.
	_, last, hasDot := strings.CutLast(host, ".")
	if !hasDot {
		last = host
	}
	digits := strings.Trim(last, "0123456789") == ""
	hex := strings.HasPrefix(last, "0x") && strings.Trim(last[2:], "0123456789abcdef") == ""
	return !digits && !hex
}
