package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/recally-io/dex/internal/execenv"
)

//go:embed install-machine.sh
var machineInstaller []byte

// MachineTLS serves the remote machine protocol on its own TLS listener. Dex holds the only
// certificate, and the install command pins its public key, so a machine needs no CA and no
// public domain. The admin UI keeps its own listener.
type MachineTLS struct {
	server   *http.Server
	listener net.Listener
	addr     string
	// Pin is the base64 SHA-256 digest of the certificate public key.
	Pin string
	// URLs are the addresses a machine can use to reach this listener.
	URLs []string
}

// ListenMachineTLS starts the machine listener and serves it until the server closes.
func ListenMachineTLS(ctx context.Context, address, dataDir string, envs *execenv.Manager) (*MachineTLS, error) {
	certificate, err := machineIdentity(dataDir)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(certificate.Leaf.RawSubjectPublicKeyInfo)
	listener, err := new(net.ListenConfig).Listen(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	m := &MachineTLS{Pin: base64.StdEncoding.EncodeToString(digest[:])}
	m.addr = listener.Addr().String()
	h := &handler{Envs: envs, machine: true}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/runner/{id}/pair", h.pairRunner)
	mux.HandleFunc("GET /api/runner/{id}/connect", h.connectRunner)
	mux.HandleFunc("GET /install-machine.sh", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(machineInstaller)
	})
	// Development builds have no published archive, so Dex serves the local payload instead.
	mux.HandleFunc("GET /machine-release/{file}", func(w http.ResponseWriter, r *http.Request) {
		name := r.PathValue("file")
		if name != "checksums.txt" && !strings.HasPrefix(name, "dex-runner_v0.0.0-dev_linux_") {
			http.NotFound(w, r)
			return
		}
		// name matches the fixed development archive names above.
		http.ServeFile(w, r, filepath.Join(dataDir, "machine-release", name)) //nolint:gosec // name is one of the fixed development archives. //nolint:gosec // The names above are fixed literals.
	})
	m.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	m.listener = listener
	m.URLs = machineURLs(m.addr)
	config := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS13, ClientAuth: tls.RequestClientCert}
	m.server.TLSConfig = config
	return m, nil
}

// Serve accepts machine connections until Close.
func (m *MachineTLS) Serve() error {
	return m.server.Serve(tls.NewListener(m.listener, m.server.TLSConfig))
}

// Close stops the listener and its active connections.
func (m *MachineTLS) Close() error { return m.server.Close() }

// machineURLs lists the addresses a remote machine can dial. An unspecified listen host
// advertises every non-loopback IPv4 address; a specific host advertises only itself.
func machineURLs(addr string) []string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	var hosts []netip.Addr
	switch ip, err := netip.ParseAddr(host); {
	case err == nil && !ip.IsUnspecified():
		hosts = []netip.Addr{ip}
	default:
		addrs, _ := net.InterfaceAddrs()
		for _, item := range addrs {
			ipnet, ok := item.(*net.IPNet)
			ip, err := netip.ParseAddr(ipnet.IP.String())
			if ok && err == nil && ip.Is4() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() {
				hosts = append(hosts, ip)
			}
		}
	}
	// A private address is usually the one that a machine on the same network can reach.
	slices.SortFunc(hosts, func(a, b netip.Addr) int {
		if a.IsPrivate() != b.IsPrivate() {
			if a.IsPrivate() {
				return -1
			}
			return 1
		}
		return a.Compare(b)
	})
	urls := make([]string, 0, len(hosts))
	for _, h := range hosts {
		urls = append(urls, "https://"+net.JoinHostPort(h.String(), port))
	}
	return urls
}

// machineIdentity reads the Dex machine certificate, or creates it on the first run. The
// certificate is self-signed and long-lived; the install command carries its pin, so a
// rotation requires a fresh install command.
func machineIdentity(dataDir string) (tls.Certificate, error) {
	filename := filepath.Join(dataDir, "machine-tls.pem")
	data, err := os.ReadFile(filename) //nolint:gosec // The data directory comes from the operator. //nolint:gosec // The data directory is the operator's choice.
	if err == nil {
		return tls.X509KeyPair(data, data)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return tls.Certificate{}, err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, err
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(10, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		return tls.Certificate{}, err
	}
	key, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return tls.Certificate{}, err
	}
	data = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})...)
	file, err := os.CreateTemp(dataDir, ".machine-tls-*")
	if err != nil {
		return tls.Certificate{}, err
	}
	defer func() { _ = file.Close(); _ = os.Remove(file.Name()) }()
	if _, err := file.Write(data); err != nil {
		return tls.Certificate{}, err
	}
	if err := file.Close(); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.Rename(file.Name(), filename); err != nil {
		return tls.Certificate{}, err
	}
	return tls.X509KeyPair(data, data)
}
