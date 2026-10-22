// SPDX-License-Identifier: AGPL-3.0-only

package relay

import (
	"net"
	"sync"
	"time"
)

// rateLimiter — token bucket на ключ (IP, устройство): burst событий сразу,
// дальше по одному в every.
type rateLimiter struct {
	mu    sync.Mutex
	every time.Duration
	burst float64
	m     map[string]*bucket
	now   func() time.Time
}

type bucket struct {
	tokens float64
	at     time.Time
}

// maxLimiterKeys — сколько ключей помнит лимитер; при переполнении (например,
// поддельные адреса источника DNS) забываются полные корзины, а если не помогло —
// все: лимиты на миг мягче, но память не растёт.
const maxLimiterKeys = 65536

func newRateLimiter(every time.Duration, burst int) *rateLimiter {
	return &rateLimiter{every: every, burst: float64(max(burst, 1)), m: map[string]*bucket{}, now: time.Now}
}

func (l *rateLimiter) refill(b *bucket, t time.Time) {
	if l.every > 0 {
		b.tokens = min(l.burst, b.tokens+float64(t.Sub(b.at))/float64(l.every))
	}
	b.at = t
}

// Allow — можно ли ещё одно событие по ключу (и учитывает его).
func (l *rateLimiter) Allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.now()
	b := l.m[key]
	if b == nil {
		if len(l.m) >= maxLimiterKeys {
			for k, o := range l.m {
				if l.refill(o, t); o.tokens >= l.burst {
					delete(l.m, k)
				}
			}
			if len(l.m) >= maxLimiterKeys {
				clear(l.m)
			}
		}
		b = &bucket{tokens: l.burst, at: t}
		l.m[key] = b
	}
	l.refill(b, t)
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// ipKey — ключ лимита по адресу: IPv4 целиком, IPv6 — сеть /64 (у одного
// клиента обычно вся /64).
func ipKey(a net.Addr) string {
	var ip net.IP
	switch v := a.(type) {
	case *net.TCPAddr:
		ip = v.IP
	case *net.UDPAddr:
		ip = v.IP
	case nil:
		return ""
	default:
		h, _, err := net.SplitHostPort(a.String())
		if err != nil {
			return a.String()
		}
		ip = net.ParseIP(h)
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4.String()
	}
	if ip == nil {
		return ""
	}
	return ip.Mask(net.CIDRMask(64, 128)).String()
}

// connLimiter — одновременные соединения: всего и с одного адреса.
type connLimiter struct {
	mu         sync.Mutex
	total      int
	perIP      map[string]int
	max, maxIP int
}

// acquire занимает место под соединение; release — освободить (ровно один раз).
func (l *connLimiter) acquire(key string) (release func(), ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if (l.max > 0 && l.total >= l.max) || (l.maxIP > 0 && l.perIP[key] >= l.maxIP) {
		return nil, false
	}
	if l.perIP == nil {
		l.perIP = map[string]int{}
	}
	l.total++
	l.perIP[key]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.total--
			if l.perIP[key]--; l.perIP[key] <= 0 {
				delete(l.perIP, key)
			}
		})
	}, true
}

// releaseConn освобождает место в connLimiter при закрытии соединения.
type releaseConn struct {
	net.Conn
	release func()
}

func (c *releaseConn) Close() error {
	err := c.Conn.Close()
	c.release()
	return err
}
