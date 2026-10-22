// SPDX-License-Identifier: AGPL-3.0-only

package relayclient

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/certmagic"
)

// fakeIssuer — эмитент вместо ACME: выпускает сертификаты на 3 с и считает выпуски.
type fakeIssuer struct {
	mu     sync.Mutex
	issued int
	ca     *x509.Certificate
	key    *ecdsa.PrivateKey
}

func (f *fakeIssuer) IssuerKey() string { return "fake-acme" }

func (f *fakeIssuer) Issue(_ context.Context, csr *x509.CertificateRequest) (*certmagic.IssuedCertificate, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issued++
	now := time.Now()
	tpl := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano()), Subject: pkix.Name{CommonName: csr.DNSNames[0]},
		DNSNames: csr.DNSNames, NotBefore: now.Add(-time.Second), NotAfter: now.Add(3 * time.Second),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, f.ca, csr.PublicKey, f.key)
	if err != nil {
		return nil, err
	}
	return &certmagic.IssuedCertificate{Certificate: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}, nil
}

func (f *fakeIssuer) n() int { f.mu.Lock(); defer f.mu.Unlock(); return f.issued }

// TestCertRenewedInBackground: фоновое продление certmagic идёт через нашу
// конфигурацию (наш эмитент и хранилище), а не через certmagic.NewDefault —
// иначе сертификат устройства истёк бы без перезапуска daemon.
func TestCertRenewedInBackground(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fake CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, _ := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	ca, _ = x509.ParseCertificate(der)
	iss := &fakeIssuer{ca: ca, key: key}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	host := "dev1.relay.test"
	tc, err := ACMETLS(ctx, ACMEConfig{Host: host, StorageDir: t.TempDir(), issuer: iss, renewCheck: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	first := iss.n()
	if first == 0 {
		t.Fatal("certificate not issued")
	}
	leaf := func() *x509.Certificate {
		c, err := tc.GetCertificate(&tls.ClientHelloInfo{ServerName: host})
		if err != nil {
			t.Fatal(err)
		}
		l, _ := x509.ParseCertificate(c.Certificate[0])
		return l
	}
	was := leaf().NotAfter
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && (iss.n() == first || !leaf().NotAfter.After(was)) {
		time.Sleep(100 * time.Millisecond)
	}
	if iss.n() == first || !leaf().NotAfter.After(was) {
		t.Fatalf("certificate not renewed in background (issued %d)", iss.n())
	}
}
