// SPDX-License-Identifier: AGPL-3.0-only

package relay

import (
	"net"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
)

// DNS — авторитетный DNS зоны relay: A/AAAA для relay и всех устройств
// (<id>.relay.gate4.ai → адрес relay), TXT _acme-challenge.<id> для DNS-01.
// SOA и NS — только у вершины зоны: по ним ACME-клиент (certmagic) ищет зону
// и её авторитетные серверы, проверяя TXT; SOA у каждого имени делал зоной
// само имя _acme-challenge.<id>, у которого нет NS, и выпуск сертификата падал.
//
// Против отражённых атак (поддельный адрес источника по UDP): ответ не больше
// буфера клиента (512 байт или размер EDNS0) — лишнее отрезается с битом TC,
// клиент повторит по TCP; запросы с одного адреса по UDP ограничены
// (UDPPerSec, UDPBurst), сверх — без ответа. TXT ограничены store: не больше
// MaxTXT значений по 255 байт на имя.
type DNS struct {
	Zone  string // relay.gate4.ai
	IPv4  net.IP
	IPv6  net.IP
	Store *Store
	TTL   uint32
	// Metrics — счётчик запросов (nil — без учёта).
	Metrics *Metrics
	// CAA — удостоверяющие центры, которым разрешён выпуск в зоне (issue);
	// nil — DefaultCAA, пустой срез ([]string{}) — записей CAA нет (любой CA).
	CAA []string
	// UDPPerSec и UDPBurst — лимит запросов по UDP с одного адреса (IPv6 — /64);
	// 0 — 50 в секунду, запас 100.
	UDPPerSec, UDPBurst int

	once sync.Once
	rl   *rateLimiter
}

// DefaultCAA — CAA зоны по умолчанию.
var DefaultCAA = []string{"letsencrypt.org"}

// maxUDPSize — больше этого по UDP не отвечаем даже при большом буфере EDNS0
// (рекомендация DNS Flag Day 2020): дальше — TCP.
const maxUDPSize = 1232

// ServeDNS отвечает на запросы.
func (d *DNS) ServeDNS(w dns.ResponseWriter, r *dns.Msg) {
	_, udp := w.RemoteAddr().(*net.UDPAddr)
	if udp && !d.allow(w.RemoteAddr()) {
		if d.Metrics != nil {
			d.Metrics.dns.WithLabelValues("RATELIMITED").Inc()
		}
		return // без ответа: усиливать нечего
	}
	m := new(dns.Msg)
	m.SetReply(r)
	m.Authoritative = true
	zone := dns.Fqdn(strings.ToLower(d.Zone))
	ttl := d.TTL
	if ttl == 0 {
		ttl = 60
	}
	for _, q := range r.Question {
		name := strings.ToLower(q.Name)
		if name != zone && !strings.HasSuffix(name, "."+zone) {
			m.Rcode = dns.RcodeRefused
			continue
		}
		hdr := dns.RR_Header{Name: q.Name, Class: dns.ClassINET, Ttl: ttl}
		apex := name == zone
		switch q.Qtype {
		case dns.TypeA:
			if d.IPv4 != nil && !strings.HasPrefix(name, "_acme-challenge.") {
				hdr.Rrtype = dns.TypeA
				m.Answer = append(m.Answer, &dns.A{Hdr: hdr, A: d.IPv4})
			}
		case dns.TypeAAAA:
			if d.IPv6 != nil && !strings.HasPrefix(name, "_acme-challenge.") {
				hdr.Rrtype = dns.TypeAAAA
				m.Answer = append(m.Answer, &dns.AAAA{Hdr: hdr, AAAA: d.IPv6})
			}
		case dns.TypeTXT:
			hdr.Rrtype = dns.TypeTXT
			hdr.Ttl = 10
			for _, v := range d.Store.TXT(name) {
				m.Answer = append(m.Answer, &dns.TXT{Hdr: hdr, Txt: []string{v}})
			}
		case dns.TypeSOA:
			if apex {
				m.Answer = append(m.Answer, d.soa(zone, ttl))
			}
		case dns.TypeNS:
			if apex {
				hdr.Rrtype = dns.TypeNS
				m.Answer = append(m.Answer, &dns.NS{Hdr: hdr, Ns: "ns1." + zone})
			}
		case dns.TypeCAA:
			hdr.Rrtype = dns.TypeCAA
			cas := d.CAA
			if cas == nil {
				cas = DefaultCAA
			}
			for _, ca := range cas {
				m.Answer = append(m.Answer, &dns.CAA{Hdr: hdr, Flag: 0, Tag: "issue", Value: ca})
			}
		}
	}
	// Нет ответа у имени зоны — NODATA: SOA зоны в authority (RFC 2308).
	if m.Rcode == dns.RcodeSuccess && len(m.Answer) == 0 {
		m.Ns = append(m.Ns, d.soa(zone, ttl))
	}
	if d.Metrics != nil {
		d.Metrics.dns.WithLabelValues(dns.RcodeToString[m.Rcode]).Inc()
	}
	size := dns.MaxMsgSize // TCP
	if udp {
		size = dns.MinMsgSize
		if opt := r.IsEdns0(); opt != nil {
			size = min(max(int(opt.UDPSize()), dns.MinMsgSize), maxUDPSize)
		}
	}
	if opt := r.IsEdns0(); opt != nil {
		m.SetEdns0(uint16(min(size, maxUDPSize)), false)
	}
	m.Truncate(size) // не влезло — TC, клиент повторит по TCP
	_ = w.WriteMsg(m)
}

// allow — лимит запросов по UDP с адреса a.
func (d *DNS) allow(a net.Addr) bool {
	d.once.Do(func() {
		per, burst := d.UDPPerSec, d.UDPBurst
		if per <= 0 {
			per = 50
		}
		if burst <= 0 {
			burst = 2 * per
		}
		d.rl = newRateLimiter(time.Second/time.Duration(per), burst)
	})
	return d.rl.Allow(ipKey(a))
}

func (d *DNS) soa(zone string, ttl uint32) *dns.SOA {
	return &dns.SOA{Hdr: dns.RR_Header{Name: zone, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: ttl},
		Ns: "ns1." + zone, Mbox: "hostmaster." + zone, Serial: 1, Refresh: 3600, Retry: 600, Expire: 86400, Minttl: 60}
}

// ListenDNS запускает UDP и TCP сервер DNS на addr.
func ListenDNS(addr string, h *DNS) (*dns.Server, *dns.Server, error) {
	udp := &dns.Server{Addr: addr, Net: "udp", Handler: h}
	tcp := &dns.Server{Addr: addr, Net: "tcp", Handler: h}
	errc := make(chan error, 2)
	for _, srv := range []*dns.Server{udp, tcp} {
		srv.NotifyStartedFunc = func() { errc <- nil }
		go func() {
			if err := srv.ListenAndServe(); err != nil {
				errc <- err
			}
		}()
	}
	for i := 0; i < 2; i++ {
		if err := <-errc; err != nil {
			return nil, nil, err
		}
	}
	return udp, tcp, nil
}
