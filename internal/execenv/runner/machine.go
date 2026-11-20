package runner

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"time"
)

// MachineClient returns a client that trusts only the pinned Dex machine key. The machine
// certificate identifies this host on every connection, so the pin and the key replace both a
// CA bundle and a bearer credential.
func MachineClient(pin string, identity tls.Certificate) (*http.Client, error) {
	client := &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	if pin == "" {
		return nil, errors.New("the Dex machine public key pin is required")
	}
	want, err := base64.StdEncoding.DecodeString(pin)
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("invalid Dex machine public key fingerprint")
	}
	client.Transport = &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion:         tls.VersionTLS13,
		Certificates:       []tls.Certificate{identity},
		InsecureSkipVerify: true, //nolint:gosec // VerifyConnection authenticates the pinned key and checks validity.
		VerifyConnection: func(info tls.ConnectionState) error {
			if len(info.PeerCertificates) == 0 {
				return errors.New("missing Dex machine certificate")
			}
			cert := info.PeerCertificates[0]
			hash := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
			if subtle.ConstantTimeCompare(hash[:], want) != 1 || time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
				return errors.New("the Dex machine certificate does not match the install command")
			}
			return nil
		},
	}}
	return client, nil
}

// NewMachineIdentity returns a private machine key pair in PEM form. Dex stores its public key
// on enrollment and checks it on every connection.
func NewMachineIdentity() (string, error) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	now := time.Now()
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", err
	}
	template := &x509.Certificate{SerialNumber: serial, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(10, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		return "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})) + string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), nil
}

// ParseMachineIdentity loads the PEM key pair that NewMachineIdentity returned.
func ParseMachineIdentity(pemData string) (tls.Certificate, error) {
	if pemData == "" {
		return tls.Certificate{}, nil
	}
	return tls.X509KeyPair([]byte(pemData), []byte(pemData))
}
