package tests

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	_ "github.com/caddyserver/caddy/v2/modules/standard"
	"github.com/libdns/libdns"

	dbs "github.com/rkstraessler/DBS-CaddyModule"
)

const soapNS = "http://soap.domain-bestellsystem.de/soap/1.1/"
const envNS = "http://schemas.xmlsoap.org/soap/envelope/"
const zone = "sub.example.test."

type record struct {
	Name string `xml:"name"`
	Type string `xml:"type"`
	Data string `xml:"data"`
	Aux  string `xml:"aux"`
	TTL  string `xml:"ttl"`
}

type mockDBS struct {
	mu               sync.Mutex
	records          map[string][]record
	calls            map[string]int
	readFailures     int
	responseOverride string
	httpStatus       int
}

func mockProvider(t *testing.T) (*dbs.Provider, *mockDBS) {
	t.Helper()
	mock := &mockDBS{records: make(map[string][]record), calls: make(map[string]int)}
	server := httptest.NewTLSServer(http.HandlerFunc(mock.serve))
	t.Cleanup(server.Close)
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	t.Setenv("DBS_SOAP_ENDPOINT", server.URL+"/soap.php")
	t.Setenv("DBS_SOAP_USERNAME", "partner/test")
	t.Setenv("DBS_SOAP_PASSWORD", "fixture-secret")
	p := new(dbs.Provider)
	if err := p.Provision(caddy.Context{}); err != nil {
		t.Fatal(err)
	}
	return p, mock
}

func (m *mockDBS) serve(w http.ResponseWriter, req *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, password, ok := req.BasicAuth()
	if !ok || user != "partner/test" || password != "fixture-secret" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var message struct {
		XMLName xml.Name `xml:"http://schemas.xmlsoap.org/soap/envelope/ Envelope"`
		Body    struct {
			Operation struct {
				XMLName xml.Name
				Request struct {
					XMLName xml.Name
					Zone    string `xml:"soaOrigin"`
					Records struct {
						record
						Items []record `xml:"item"`
					} `xml:"rr"`
				} `xml:",any"`
			} `xml:",any"`
		} `xml:"http://schemas.xmlsoap.org/soap/envelope/ Body"`
	}
	op := &message.Body.Operation
	if err := xml.NewDecoder(req.Body).Decode(&message); err != nil ||
		req.Method != http.MethodPost || req.URL.Path != "/soap.php" || op.XMLName.Space != soapNS ||
		req.Header.Get("SOAPAction") != `"`+soapNS+"#"+op.XMLName.Local+`"` ||
		op.Request.XMLName.Local != op.XMLName.Local+"Request" ||
		req.Header.Get("Content-Type") != "text/xml; charset=utf-8" || op.Request.Zone == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	name := op.XMLName.Local
	zoneName := op.Request.Zone
	m.calls[name]++
	if m.httpStatus != 0 {
		w.WriteHeader(m.httpStatus)
		return
	}
	if m.responseOverride != "" {
		fmt.Fprint(w, m.responseOverride)
		return
	}
	switch name {
	case "nameserverZoneInfo":
		if m.readFailures > 0 {
			m.readFailures--
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		list, _ := xml.Marshal(struct {
			XMLName xml.Name `xml:"rrList"`
			Items   []record `xml:"item"`
		}{Items: m.records[zoneName]})
		reply(w, name, "<origin>"+zoneName+"</origin>"+string(list), 1000)
	case "nameserverRRCreate":
		if len(op.Request.Records.Items) != 1 || op.Request.Records.Items[0].Type != "TXT" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		m.records[zoneName] = append(m.records[zoneName], op.Request.Records.Items[0])
		reply(w, name, "", 1000)
	case "nameserverRRDelete":
		for i, existing := range m.records[zoneName] {
			if existing == op.Request.Records.record {
				m.records[zoneName] = append(m.records[zoneName][:i], m.records[zoneName][i+1:]...)
				reply(w, name, "", 1000)
				return
			}
		}
		reply(w, name, "", 2303)
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func reply(w http.ResponseWriter, op, content string, code int) {
	fmt.Fprintf(w, `<s:Envelope xmlns:s="%s" xmlns:d="%s"><s:Body><d:%sResponse><return>%s<returnCode>%d</returnCode></return></d:%sResponse></s:Body></s:Envelope>`,
		envNS, soapNS, op, content, code, op)
}

func TestCaddyModule(t *testing.T) {
	mod, err := caddy.GetModule("dns.providers.domain_bestellsystem")
	if err != nil {
		t.Fatal(err)
	}
	provider := mod.New().(*dbs.Provider)
	if err := provider.UnmarshalCaddyfile(caddyfile.NewTestDispenser("domain_bestellsystem")); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"domain_bestellsystem passwd", "domain_bestellsystem {\npassword secret\n}"} {
		if err := provider.UnmarshalCaddyfile(caddyfile.NewTestDispenser(invalid)); err == nil {
			t.Fatal("accepted Caddyfile configuration")
		}
	}
	t.Setenv("DBS_SOAP_ENDPOINT", "https://example.test/soap.php")
	t.Setenv("DBS_SOAP_USERNAME", "partner/test")
	t.Setenv("DBS_SOAP_PASSWORD", "")
	if err := provider.Provision(caddy.Context{}); err == nil {
		t.Fatal("accepted missing password")
	}
	t.Setenv("DBS_SOAP_PASSWORD", "fixture-secret")
	for _, invalid := range []string{"http://example.test", "https://user:secret@example.test", "https://example.test/soap.php?wsdl"} {
		t.Setenv("DBS_SOAP_ENDPOINT", invalid)
		if err := provider.Provision(caddy.Context{}); err == nil {
			t.Fatal("accepted unsafe endpoint")
		}
	}
}

func TestCaddyfileAdapter(t *testing.T) {
	config := []byte("api.example.test {\n\ttls {\n\t\tdns domain_bestellsystem\n\t}\n\treverse_proxy localhost:3000\n}\n")
	adapted, _, err := caddyconfig.GetAdapter("caddyfile").Adapt(config, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(adapted), `"provider":{"name":"domain_bestellsystem"}`) {
		t.Fatalf("Caddyfile adapter did not select the DBS module: %s", adapted)
	}
}

func TestParallelChallenges(t *testing.T) {
	p, mock := mockProvider(t)
	ctx := t.Context()
	zoneName := strings.TrimSuffix(zone, ".")
	existing := record{"_acme-challenge", "TXT", "previous", "0", "600"}
	address := record{"www", "A", "192.0.2.7", "0", "900"}
	mock.records[zoneName] = []record{existing, address}
	prior := libdns.TXT{Name: "_acme-challenge", Text: "previous"}
	reused, err := p.AppendRecords(ctx, zone, []libdns.Record{prior})
	if err != nil || len(reused) != 1 {
		t.Fatalf("reusing existing TXT: %v, %v", reused, err)
	}
	if deleted, err := p.DeleteRecords(ctx, zone, reused); err != nil || len(deleted) != 0 {
		t.Fatalf("cleanup of pre-existing TXT: %v, %v", deleted, err)
	}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			challenge := libdns.TXT{Name: "_acme-challenge", Text: fmt.Sprintf("test<&-%d", i)}
			created, err := p.AppendRecords(ctx, zone, []libdns.Record{challenge})
			if err != nil || len(created) != 1 || created[0].RR().TTL != 300*time.Second {
				t.Errorf("create: %v, %v", created, err)
				return
			}
			repeated, err := p.AppendRecords(ctx, zone, []libdns.Record{challenge})
			if err != nil || len(repeated) != 1 {
				t.Errorf("repeat create: %v, %v", repeated, err)
				return
			}
			if deleted, err := p.DeleteRecords(ctx, zone, repeated); err != nil || len(deleted) != 0 {
				t.Errorf("cleanup of repeated create: %v, %v", deleted, err)
			}
			if deleted, err := p.DeleteRecords(ctx, zone, created); err != nil || len(deleted) != 1 {
				t.Errorf("cleanup: %v, %v", deleted, err)
			}
			if deleted, err := p.DeleteRecords(ctx, zone, created); err != nil || len(deleted) != 0 {
				t.Errorf("repeat cleanup: %v, %v", deleted, err)
			}
		})
	}
	wg.Wait()
	mock.mu.Lock()
	defer mock.mu.Unlock()
	remaining := mock.records[zoneName]
	if len(remaining) != 2 || remaining[0] != existing || remaining[1] != address {
		t.Fatalf("unrelated records changed: %v", remaining)
	}
	if mock.calls["nameserverRRCreate"] != 8 || mock.calls["nameserverRRDelete"] != 8 {
		t.Fatalf("unexpected writes: %v", mock.calls)
	}
}

func TestSOAPFailuresAndRetry(t *testing.T) {
	p, mock := mockProvider(t)
	txt := []libdns.Record{libdns.TXT{Name: "_acme-challenge", Text: "value"}}
	mock.readFailures = 2
	out, err := p.AppendRecords(t.Context(), zone, txt)
	if err != nil || len(out) != 1 || mock.calls["nameserverZoneInfo"] != 3 {
		t.Fatalf("bounded read retries: %v, %v, %v", out, err, mock.calls)
	}
	mock.httpStatus = http.StatusUnauthorized
	if _, err := p.AppendRecords(t.Context(), zone, txt); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("authentication failure: %v", err)
	}
	mock.httpStatus = 0
	for _, body := range []string{
		"<broken",
		`<s:Envelope xmlns:s="` + envNS + `"><s:Body><s:Fault><faultstring>fixture-secret</faultstring></s:Fault></s:Body></s:Envelope>`,
		`<s:Envelope xmlns:s="` + envNS + `" xmlns:d="` + soapNS + `"><s:Body><d:nameserverZoneInfoResponse><return><returnCode>2303</returnCode><returnMessage>fixture-secret</returnMessage></return></d:nameserverZoneInfoResponse></s:Body></s:Envelope>`,
	} {
		mock.responseOverride = body
		if _, err := p.AppendRecords(t.Context(), zone, txt); err == nil || strings.Contains(err.Error(), "fixture-secret") {
			t.Fatalf("expected a redacted SOAP failure: %v", err)
		}
	}
	mock.responseOverride = ""
	mock.readFailures = 3
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := p.AppendRecords(ctx, zone, txt); err != context.DeadlineExceeded {
		t.Fatalf("retry ignored caller deadline: %v", err)
	}
}
