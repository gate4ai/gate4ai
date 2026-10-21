// SPDX-License-Identifier: AGPL-3.0-only

// Команда gate4ai-relay — relay gate4.ai: публичный адрес устройств
// (<device-id>.<domain>), TLS-passthrough по SNI, туннели WebSocket + yamux,
// авторитетный DNS зоны для ACME DNS-01, свидетель чекпоинтов. Тот же бинарник
// для hosted и self-hosted. --route пробрасывает сторонние имена (сайт) на
// локальный бэкенд по SNI, тоже без расшифровки.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gate4ai/gate4ai/relay"
	"github.com/prometheus/client_golang/prometheus"
)

// version — версия сборки (-ldflags "-X main.version=…").
var version = "dev"

func main() {
	domain := flag.String("domain", "relay.gate4.ai", "relay domain; devices are <id>.<domain>")
	listen := flag.String("listen", ":443", "TCP address for TLS (tunnels, API, device traffic)")
	dnsAddr := flag.String("dns", "", "address of the authoritative DNS server for the zone, e.g. :53 (empty — off)")
	ipv4 := flag.String("ipv4", "", "public IPv4 of the relay (A records)")
	ipv6 := flag.String("ipv6", "", "public IPv6 of the relay (AAAA records)")
	certFile := flag.String("cert", "", "TLS certificate of the relay domain (PEM)")
	keyFile := flag.String("key", "", "TLS key (PEM)")
	selfSigned := flag.Bool("self-signed", false, "development: self-signed certificate for the relay domain")
	stateFile := flag.String("state", "relay-state.json", "state file (devices, TXT records, witnessed checkpoints)")
	openReg := flag.Bool("open-registration", true, "allow new devices to register")
	var routes routeFlags
	flag.Var(&routes, "route", "pass TLS for another name to a backend by SNI, without decrypting: host=addr, e.g. gate4.ai=127.0.0.1:8443; host=addr,proxy sends a PROXY protocol v2 header with the client address first (repeatable)")
	var lim limits
	flag.StringVar(&lim.caa, "caa", strings.Join(relay.DefaultCAA, ","), "CAs allowed to issue certificates in the zone (CAA issue), comma-separated; empty — no CAA records")
	flag.IntVar(&lim.maxDevices, "max-devices", relay.DefaultMaxDevices, "maximum registered devices (0 — unlimited)")
	flag.IntVar(&lim.regPerIP, "reg-per-ip", 5, "device registrations per day from one IP address (IPv6 — /64; 0 — unlimited)")
	flag.IntVar(&lim.regPerDay, "reg-per-day", 500, "device registrations per day in total (0 — unlimited)")
	flag.IntVar(&lim.ordersPerDay, "cert-orders-per-device", relay.DefaultCertOrdersPerDay, "certificate orders (ACME TXT) per device per day (0 — unlimited)")
	flag.IntVar(&lim.maxConns, "max-conns", 20000, "simultaneous TCP connections (0 — unlimited)")
	flag.IntVar(&lim.maxConnsIP, "max-conns-per-ip", 256, "simultaneous TCP connections from one IP address (0 — unlimited)")
	metricsAddr := flag.String("metrics", "", "address for Prometheus /metrics, e.g. 127.0.0.1:9464 (empty — off; keep it on localhost)")
	otlp := flag.String("otlp", "", "OTLP/HTTP endpoint for OpenTelemetry traces, e.g. 127.0.0.1:4318 (empty — off)")
	traceRatio := flag.Float64("trace-ratio", 0.1, "share of traces sampled, 0..1")
	showVersion := flag.Bool("version", false, "")
	flag.Parse()
	if *showVersion {
		fmt.Println("gate4ai-relay", version)
		return
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if *otlp != "" {
		stop, err := relay.InitTracing(context.Background(), *otlp, *traceRatio, version)
		if err != nil {
			log.Error("tracing", "err", err)
			os.Exit(1)
		}
		defer stop(context.Background())
		log.Info("tracing", "otlp", *otlp, "ratio", *traceRatio)
	}
	if err := run(log, *domain, *listen, *dnsAddr, *ipv4, *ipv6, *certFile, *keyFile, *selfSigned, *stateFile, *openReg, routes, *metricsAddr, lim); err != nil {
		log.Error("relay", "err", err)
		os.Exit(1)
	}
}

// limits — пределы relay из флагов.
type limits struct {
	caa                                           string
	maxDevices, regPerIP, regPerDay, ordersPerDay int
	maxConns, maxConnsIP                          int
}

func run(log *slog.Logger, domain, listen, dnsAddr, ipv4, ipv6, certFile, keyFile string, selfSigned bool, stateFile string, openReg bool, routes []string, metricsAddr string, lim limits) error {
	var cert tls.Certificate
	var err error
	switch {
	case certFile != "":
		cert, err = tls.LoadX509KeyPair(certFile, keyFile)
	case selfSigned:
		cert, _, err = relay.SelfSigned(domain)
	default:
		return fmt.Errorf("pass --cert/--key for %s (or --self-signed for development)", domain)
	}
	if err != nil {
		return err
	}
	st, err := relay.OpenStore(stateFile)
	if err != nil {
		return err
	}
	s := relay.NewServer(domain, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"http/1.1"}}, st)
	st.MaxDevices, st.CertOrdersPerDay = lim.maxDevices, lim.ordersPerDay
	s.Log, s.AllowReg = log, openReg
	s.RegPerIPDay, s.RegPerDay, s.MaxConns, s.MaxConnsPerIP = lim.regPerIP, lim.regPerDay, lim.maxConns, lim.maxConnsIP
	s.Metrics.Register(prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "gate4ai_relay_build_info",
		Help: "Relay build; value is always 1.", ConstLabels: prometheus.Labels{"version": version}}, func() float64 { return 1 }))
	if dnsAddr != "" {
		caa := []string{}
		for _, ca := range strings.Split(lim.caa, ",") {
			if ca = strings.TrimSpace(ca); ca != "" {
				caa = append(caa, ca)
			}
		}
		h := &relay.DNS{Zone: domain, IPv4: net.ParseIP(ipv4), IPv6: net.ParseIP(ipv6), Store: st, Metrics: s.Metrics, CAA: caa}
		if _, _, err := relay.ListenDNS(dnsAddr, h); err != nil {
			return fmt.Errorf("dns: %w", err)
		}
		log.Info("dns listening", "addr", dnsAddr, "zone", domain)
	}
	if metricsAddr != "" {
		ml, err := net.Listen("tcp", metricsAddr)
		if err != nil {
			return fmt.Errorf("metrics: %w", err)
		}
		mux := http.NewServeMux()
		mux.Handle("GET /metrics", s.Metrics.Handler())
		go (&http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}).Serve(ml)
		log.Info("metrics listening", "addr", metricsAddr)
	}
	for _, r := range routes {
		host, addr, _ := strings.Cut(r, "=")
		if err := s.AddRoute(host, addr); err != nil {
			return err
		}
		log.Info("route", "host", host, "backend", addr)
	}
	l, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	log.Info("relay listening", "addr", listen, "domain", domain)
	return s.Serve(l)
}

// routeFlags — повторяемый флаг --route host=addr.
type routeFlags []string

func (r *routeFlags) String() string     { return strings.Join(*r, ",") }
func (r *routeFlags) Set(v string) error { *r = append(*r, v); return nil }
