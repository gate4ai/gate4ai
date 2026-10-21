// SPDX-License-Identifier: AGPL-3.0-only

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

// Сроки: CA живёт только в памяти daemon (до перезапуска), поэтому долгий;
// leaf перевыпускается, когда до конца срока остаётся меньше leafRenew.
const (
	caLifetime   = 5 * 365 * 24 * time.Hour
	leafLifetime = 7 * 24 * time.Hour
	leafRenew    = 24 * time.Hour
)

// NewCA создаёт CA.
func NewCA(name string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(caLifetime), IsCA: true,
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

// Leaf — сертификат для хоста (из кэша, пока до конца срока больше leafRenew).
func (c *CA) Leaf(host string) (*tls.Certificate, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if l, ok := c.leaf[host]; ok && time.Until(l.Leaf.NotAfter) > leafRenew {
		return l, nil
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: host},
		DNSNames: []string{host}, NotBefore: time.Now().Add(-time.Hour), NotAfter: minTime(time.Now().Add(leafLifetime), c.cert.NotAfter),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	l := &tls.Certificate{Certificate: [][]byte{der, c.der}, PrivateKey: key, Leaf: cert}
	c.leaf[host] = l
	return l, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
