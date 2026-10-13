// SPDX-License-Identifier: FSL-1.1-ALv2

package egress

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"sync"
	"time"
)

// CA — локальный CA для доменов с подстановкой секретов: только для них прокси
// расшифровывает HTTPS (агенту в песочнице выдаётся этот CA), остальное — туннель.
type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	der  []byte
	mu   sync.Mutex
	leaf map[string]*tls.Certificate
}

// NewCA создаёт CA.
func NewCA(name string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(30 * 24 * time.Hour), IsCA: true,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, MaxPathLenZero: true}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, _ := x509.ParseCertificate(der)
	return &CA{cert: cert, key: key, der: der, leaf: map[string]*tls.Certificate{}}, nil
}

// PEM — сертификат CA для доверия внутри песочницы.
func (c *CA) PEM() []byte { return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.der}) }

// Pool — пул с этим CA.
func (c *CA) Pool() *x509.CertPool {
	p := x509.NewCertPool()
	p.AddCert(c.cert)
	return p
}

// Leaf — сертификат для хоста.
func (c *CA) Leaf(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l, ok := c.leaf[host]; ok {
		return l, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: host},
		DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(7 * 24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	l := &tls.Certificate{Certificate: [][]byte{der, c.der}, PrivateKey: key}
	c.leaf[host] = l
	return l, nil
}
