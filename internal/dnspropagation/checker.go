// Package dnspropagation checks that a DNS-01 update is visible at every authority.
package dnspropagation

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/libdns/libdns"
	"github.com/miekg/dns"
)

// Checker queries the zone's nameservers directly. Its zero value uses system
// DNS for discovery and standard DNS transport for the authoritative queries.
type Checker struct {
	LookupNS func(context.Context, string) ([]*net.NS, error)
	Exchange func(context.Context, *dns.Msg, string) (*dns.Msg, error)
}

func (c Checker) Wait(ctx context.Context, zone string, records []libdns.Record) error {
	if c.LookupNS == nil {
		c.LookupNS = net.DefaultResolver.LookupNS
	}
	if c.Exchange == nil {
		c.Exchange = exchange
	}
	zone = dns.Fqdn(zone)
	for {
		pending := c.check(ctx, zone, records)
		if pending == nil {
			return ctx.Err()
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("waiting for authoritative DNS propagation in %s: %w (%v)", zone, ctx.Err(), pending)
		case <-timer.C:
		}
	}
}

func (c Checker) check(ctx context.Context, zone string, records []libdns.Record) error {
	lookupCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	nameservers, err := c.LookupNS(lookupCtx, zone)
	cancel()
	if err != nil {
		return fmt.Errorf("looking up zone nameservers: %w", err)
	}
	if len(nameservers) == 0 {
		return fmt.Errorf("zone has no nameservers")
	}
	for _, ns := range nameservers {
		for _, record := range records {
			rr := record.RR()
			name := dns.Fqdn(libdns.AbsoluteName(rr.Name, zone))
			if err := c.checkTXT(ctx, ns.Host, name, rr.Data); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c Checker) checkTXT(ctx context.Context, nameserver, name, value string) error {
	query := new(dns.Msg).SetQuestion(name, dns.TypeTXT)
	query.RecursionDesired = false
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	answer, err := c.Exchange(queryCtx, query, net.JoinHostPort(nameserver, "53"))
	if err != nil {
		return fmt.Errorf("querying %s for %s: %w", nameserver, name, err)
	}
	if answer.Authoritative && !answer.Truncated && answer.Rcode == dns.RcodeSuccess {
		for _, rr := range answer.Answer {
			txt, ok := rr.(*dns.TXT)
			if ok && strings.EqualFold(txt.Hdr.Name, name) && strings.Join(txt.Txt, "") == value {
				return nil
			}
		}
	}
	return fmt.Errorf("%s has not confirmed the expected TXT record for %s", nameserver, name)
}

func exchange(ctx context.Context, query *dns.Msg, address string) (*dns.Msg, error) {
	client := &dns.Client{Net: "udp"}
	answer, _, err := client.ExchangeContext(ctx, query, address)
	if err == nil && answer.Truncated {
		client.Net = "tcp"
		answer, _, err = client.ExchangeContext(ctx, query, address)
	}
	return answer, err
}
