package dnspropagation_test

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/libdns/libdns"
	"github.com/miekg/dns"

	"github.com/rkstraessler/DBS-CaddyModule/internal/dnspropagation"
)

func TestWaitForEveryAuthority(t *testing.T) {
	const zone = "example.test."
	const name = "_acme-challenge." + zone
	var queried []string
	rounds := 0
	checker := dnspropagation.Checker{
		LookupNS: func(_ context.Context, gotZone string) ([]*net.NS, error) {
			if gotZone != zone {
				t.Fatalf("looked up NS for %s, want %s", gotZone, zone)
			}
			rounds++
			return []*net.NS{{Host: "ns1.example.test."}, {Host: "ns2.example.test."}}, nil
		},
		Exchange: func(_ context.Context, query *dns.Msg, address string) (*dns.Msg, error) {
			queried = append(queried, address)
			if query.RecursionDesired || query.Question[0].Name != name || query.Question[0].Qtype != dns.TypeTXT {
				t.Fatalf("unexpected query: %v", query)
			}
			answer := new(dns.Msg).SetReply(query)
			answer.Authoritative = true
			answer.Answer = append(answer.Answer, txt(name, "previous-value"))
			if address != "ns2.example.test.:53" || rounds > 1 {
				answer.Answer = append(answer.Answer, txt(name, "current", "-value"))
			}
			return answer, nil
		},
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	err := checker.Wait(ctx, "example.test", []libdns.Record{libdns.TXT{Name: "_acme-challenge", Text: "current-value"}})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ns1.example.test.:53", "ns2.example.test.:53", "ns1.example.test.:53", "ns2.example.test.:53"}
	if !slices.Equal(queried, want) {
		t.Fatalf("queried %v, want %v", queried, want)
	}
}

func TestWaitRejectsUnconfirmedTXT(t *testing.T) {
	const zone = "example.test."
	for _, test := range []struct {
		name  string
		reply func(*dns.Msg)
	}{
		{"missing record", func(answer *dns.Msg) { answer.Answer = nil }},
		{"wrong owner", func(answer *dns.Msg) { answer.Answer = []dns.RR{txt("other."+zone, "current-value")} }},
		{"non-authoritative", func(answer *dns.Msg) { answer.Authoritative = false }},
		{"truncated", func(answer *dns.Msg) { answer.Truncated = true }},
		{"failed response", func(answer *dns.Msg) { answer.Rcode = dns.RcodeServerFailure }},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			checker := dnspropagation.Checker{
				LookupNS: func(context.Context, string) ([]*net.NS, error) {
					return []*net.NS{{Host: "ns1.example.test."}}, nil
				},
				Exchange: func(_ context.Context, query *dns.Msg, _ string) (*dns.Msg, error) {
					cancel()
					answer := new(dns.Msg).SetReply(query)
					answer.Authoritative = true
					answer.Answer = []dns.RR{txt(zone, "current-value")}
					test.reply(answer)
					return answer, nil
				},
			}
			err := checker.Wait(ctx, zone, []libdns.Record{libdns.TXT{Name: "@", Text: "current-value"}})
			if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "ns1.example.test.") {
				t.Fatalf("expected a canceled propagation check naming the server, got %v", err)
			}
		})
	}
}

func txt(name string, parts ...string) *dns.TXT {
	return &dns.TXT{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeTXT, Class: dns.ClassINET}, Txt: parts}
}
