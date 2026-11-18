package tests

import (
	"net"
	"strings"
	"testing"

	"github.com/caddyserver/certmagic"
	"github.com/mholt/acmez/v3/acme"
	"github.com/miekg/dns"
)

func TestCertMagicDiscoversPublicZones(t *testing.T) {
	provider, mock := mockProvider(t)
	listener, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	const secondZone = "other.example.test."
	resolver := &dns.Server{
		PacketConn: listener,
		Handler: dns.HandlerFunc(func(w dns.ResponseWriter, req *dns.Msg) {
			resp := new(dns.Msg).SetReply(req)
			question := req.Question[0]
			if question.Qtype == dns.TypeSOA && (question.Name == zone || question.Name == secondZone) {
				resp.Answer = append(resp.Answer, &dns.SOA{
					Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeSOA, Class: dns.ClassINET, Ttl: 60},
					Ns:  "ns.example.test.", Mbox: "dns.example.test.", Minttl: 60,
				})
			}
			if err := w.WriteMsg(resp); err != nil {
				t.Errorf("mock DNS response: %v", err)
			}
		}),
	}
	started := make(chan struct{})
	stopped := make(chan error, 1)
	resolver.NotifyStartedFunc = func() { close(started) }
	go func() { stopped <- resolver.ActivateAndServe() }()
	select {
	case <-started:
	case err := <-stopped:
		t.Fatalf("mock DNS resolver startup: %v", err)
	}
	t.Cleanup(func() {
		if err := resolver.Shutdown(); err != nil {
			t.Errorf("shutting down mock DNS resolver: %v", err)
		}
		if err := <-stopped; err != nil {
			t.Errorf("mock DNS resolver: %v", err)
		}
	})

	solver := &certmagic.DNS01Solver{DNSManager: certmagic.DNSManager{
		DNSProvider: provider,
		Resolvers:   []string{listener.LocalAddr().String()},
	}}
	for _, zoneName := range []string{zone, secondZone} {
		base := strings.TrimSuffix(zoneName, ".")
		// ACME supplies the bare identifier for wildcard authorizations. The
		// ordinary and wildcard challenges can share an owner with distinct values.
		first := acme.Challenge{Identifier: acme.Identifier{Type: "dns", Value: "api." + base}, KeyAuthorization: "ordinary.fixture"}
		second := first
		second.KeyAuthorization = "wildcard.fixture"
		for _, challenge := range []acme.Challenge{first, second} {
			if err := solver.Present(t.Context(), challenge); err != nil {
				t.Fatal(err)
			}
		}

		mock.mu.Lock()
		records := append([]record(nil), mock.records[base]...)
		mock.mu.Unlock()
		if len(records) != 2 || records[0].Name != "_acme-challenge.api" ||
			records[0].Data != first.DNS01KeyAuthorization() || records[1].Data != second.DNS01KeyAuthorization() {
			t.Fatalf("zone %s received the wrong records: %v", base, records)
		}
		for _, challenge := range []acme.Challenge{first, second} {
			if err := solver.CleanUp(t.Context(), challenge); err != nil {
				t.Fatal(err)
			}
		}

		mock.mu.Lock()
		count := len(mock.records[base])
		mock.mu.Unlock()
		if count != 0 {
			t.Fatalf("zone %s still contains %d challenge records", base, count)
		}
	}
}
