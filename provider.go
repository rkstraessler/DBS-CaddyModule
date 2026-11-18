// Package domainbestellsystem registers a Caddy DNS provider for Domain-Bestellsystem.
package domainbestellsystem

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/libdns/libdns"

	"github.com/rkstraessler/DBS-CaddyModule/internal/dbsoap"
)

const moduleID = "dns.providers.domain_bestellsystem"

func init() { caddy.RegisterModule(&Provider{}) }

// Provider manages TXT records for Caddy's DNS-01 solver.
type Provider struct {
	client *dbsoap.Client
}

func (*Provider) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: moduleID, New: func() caddy.Module { return new(Provider) }}
}

func (p *Provider) Provision(_ caddy.Context) error {
	client, err := dbsoap.New(os.Getenv("DBS_SOAP_ENDPOINT"), os.Getenv("DBS_SOAP_USERNAME"), os.Getenv("DBS_SOAP_PASSWORD"))
	if err != nil {
		return err
	}
	p.client = client
	return nil
}

func (*Provider) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next()
	if d.NextArg() || d.NextBlock(0) {
		return d.Err("domain_bestellsystem reads its configuration from the environment")
	}
	return nil
}

// Stripes serialize matching zone operations across Caddy module instances in this process.
var zoneLocks = func() [64]chan struct{} {
	var stripes [64]chan struct{}
	for i := range stripes {
		stripes[i] = make(chan struct{}, 1)
	}
	return stripes
}()

type borrowedTXT struct{}

func (p *Provider) AppendRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error) {
	return p.change(ctx, zone, records, true)
}

func (p *Provider) DeleteRecords(ctx context.Context, zone string, records []libdns.Record) ([]libdns.Record, error) {
	return p.change(ctx, zone, records, false)
}

func (p *Provider) change(ctx context.Context, zone string, records []libdns.Record, appendRecords bool) ([]libdns.Record, error) {
	if len(records) == 0 {
		return nil, nil
	}
	if p.client == nil {
		return nil, errors.New("Domain-Bestellsystem provider has not been provisioned")
	}
	zone = strings.ToLower(strings.TrimSuffix(zone, "."))
	if !validName(zone) || strings.Contains(zone, "_") {
		return nil, errors.New("invalid DBS zone name")
	}
	desired := make([]libdns.TXT, 0, len(records))
	for _, record := range records {
		if txt, ok := record.(libdns.TXT); ok && !appendRecords {
			if _, borrowed := txt.ProviderData.(borrowedTXT); borrowed {
				continue
			}
		}
		txt, err := checkedTXT(record)
		if err != nil {
			return nil, err
		}
		if appendRecords && txt.TTL == 0 {
			txt.TTL = 300 * time.Second
		}
		desired = append(desired, txt)
	}
	if len(desired) == 0 {
		return nil, nil
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(zone))
	lock := zoneLocks[hash.Sum32()%uint32(len(zoneLocks))]
	select {
	case lock <- struct{}{}:
		defer func() { <-lock }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	var changed []libdns.Record
	for _, txt := range desired {
		record, err := p.changeTXT(ctx, zone, txt, appendRecords)
		if err != nil {
			return changed, err
		}
		if record != nil {
			changed = append(changed, *record)
		}
	}
	return changed, nil
}

func (p *Provider) changeTXT(ctx context.Context, zone string, txt libdns.TXT, appendRecord bool) (*libdns.TXT, error) {
	current, err := p.client.Zone(ctx, zone)
	if err != nil {
		return nil, err
	}
	for _, rr := range current {
		if !strings.EqualFold(rr.Type, "TXT") || !strings.EqualFold(relative(rr.Name, zone), txt.Name) ||
			(rr.Data != txt.Text && rr.Data != `"`+txt.Text+`"`) {
			continue
		}
		seconds, err := strconv.ParseUint(rr.TTL, 10, 32)
		if err != nil {
			return nil, errors.New("invalid TTL on matching DBS TXT record")
		}
		ttl := time.Duration(seconds) * time.Second
		if appendRecord {
			// CertMagic expects one result even when that exact value was already present.
			// Carry the distinction into cleanup so that it leaves the pre-existing value alone.
			txt.TTL = ttl
			txt.ProviderData = borrowedTXT{}
			return &txt, nil
		}
		if txt.TTL != 0 && txt.TTL != ttl {
			continue
		}
		deleted, err := p.client.Delete(ctx, zone, rr)
		if !deleted || err != nil {
			return nil, err
		}
		txt.TTL = ttl
		return &txt, nil
	}
	if !appendRecord {
		return nil, nil
	}
	err = p.client.Create(ctx, zone, dbsoap.Record{
		Name: txt.Name, Type: "TXT", Data: txt.Text, Aux: "0", TTL: strconv.FormatInt(int64(txt.TTL/time.Second), 10),
	})
	if err != nil {
		return nil, err
	}
	return &txt, nil
}

func relative(name, zone string) string {
	if name == "" || name == "@" {
		return "@"
	}
	if strings.HasSuffix(name, ".") {
		name = strings.ToLower(strings.TrimSuffix(name, "."))
		if name == zone {
			return "@"
		}
		if relativeName, ok := strings.CutSuffix(name, "."+zone); ok {
			return relativeName
		}
		return name + "."
	}
	return name
}

func checkedTXT(record libdns.Record) (libdns.TXT, error) {
	if record == nil {
		return libdns.TXT{}, errors.New("a DBS TXT record is required")
	}
	rr := record.RR()
	if !strings.EqualFold(rr.Type, "TXT") || (rr.Name != "@" && !validName(rr.Name)) ||
		!utf8.ValidString(rr.Data) || len(rr.Data) == 0 || len(rr.Data) > 255 ||
		strings.ContainsAny(rr.Data, "\"\\\r\n\x00") || rr.TTL < 0 || rr.TTL%time.Second != 0 ||
		rr.TTL/time.Second > (1<<31)-1 {
		return libdns.TXT{}, fmt.Errorf("DBS requires a named TXT record with a single unquoted value (1–255 bytes) and a valid whole-second TTL")
	}
	return libdns.TXT{Name: strings.ToLower(rr.Name), TTL: rr.TTL, Text: rr.Data}, nil
}

func validName(name string) bool {
	if len(name) == 0 || len(name) > 253 {
		return false
	}
	for label := range strings.SplitSeq(name, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, char := range label {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_') {
				return false
			}
		}
	}
	return true
}

var (
	_ caddy.Module          = (*Provider)(nil)
	_ caddy.Provisioner     = (*Provider)(nil)
	_ caddyfile.Unmarshaler = (*Provider)(nil)
	_ libdns.RecordAppender = (*Provider)(nil)
	_ libdns.RecordDeleter  = (*Provider)(nil)
)
