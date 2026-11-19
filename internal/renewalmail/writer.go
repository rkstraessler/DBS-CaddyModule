// Package renewalmail registers a Caddy log writer for terminal certificate renewal failures.
package renewalmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig/caddyfile"
	"github.com/rkstraessler/ReikemMailClient_GO"
)

const writerName = "certificate_renewal_mail"

func init() { caddy.RegisterModule(Writer{}) }

// Writer opens a Caddy JSON log output backed by the REIKEM Mail API.
type Writer struct{}

func (Writer) CaddyModule() caddy.ModuleInfo {
	return caddy.ModuleInfo{ID: "caddy.logging.writers." + writerName, New: func() caddy.Module { return new(Writer) }}
}

func (Writer) String() string    { return "certificate renewal email" }
func (Writer) WriterKey() string { return writerName }

func (Writer) UnmarshalCaddyfile(d *caddyfile.Dispenser) error {
	d.Next()
	if d.NextArg() || d.NextBlock(0) {
		return d.Err("certificate_renewal_mail reads its configuration from the environment")
	}
	return nil
}

func (Writer) OpenWriter() (io.WriteCloser, error) {
	baseURL := strings.TrimSpace(os.Getenv("MAIL_API_URL"))
	authKey := os.Getenv("MAIL_AUTH_KEY")
	adapterKey := os.Getenv("MAIL_ADAPTER_KEY")
	target := strings.TrimSpace(os.Getenv("MAIL_TO_TARGET"))
	if baseURL == "" && authKey == "" && adapterKey == "" && target == "" {
		return &notificationWriter{}, nil
	}
	if baseURL == "" || authKey == "" || adapterKey == "" || target == "" {
		return nil, errors.New("set MAIL_API_URL, MAIL_AUTH_KEY, MAIL_ADAPTER_KEY, and MAIL_TO_TARGET to enable renewal email")
	}
	endpoint, err := url.Parse(baseURL)
	if err != nil || endpoint.Hostname() == "" {
		return nil, errors.New("invalid MAIL_API_URL")
	}
	host := endpoint.Hostname()
	ip, _ := netip.ParseAddr(host)
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && (ip.IsLoopback() || strings.EqualFold(host, "localhost"))) {
		return nil, errors.New("MAIL_API_URL must use HTTPS for remote hosts")
	}
	address, err := mail.ParseAddress(target)
	if err != nil || address.Address != target || len(target) > 254 || strings.ContainsFunc(target, func(r rune) bool {
		return r < '!' || r > '~'
	}) {
		return nil, errors.New("MAIL_TO_TARGET must be one plain ASCII email address")
	}
	client, err := reikemmail.NewClient(reikemmail.Config{
		BaseURL:        baseURL,
		AuthKey:        authKey,
		AdapterKey:     adapterKey,
		ConnectTimeout: 3 * time.Second,
		RequestTimeout: 8 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("invalid renewal email configuration: %w", err)
	}
	secrets := []string{authKey, "[redacted]", adapterKey, "[redacted]"}
	if password := os.Getenv("DBS_SOAP_PASSWORD"); password != "" {
		secrets = append(secrets, password, "[redacted]")
	}
	return &notificationWriter{client: client, target: target, redact: strings.NewReplacer(secrets...)}, nil
}

type notificationWriter struct {
	client *reikemmail.Client
	target string
	redact *strings.Replacer
}

func (*notificationWriter) Close() error { return nil }

type logEntry struct {
	Level       string    `json:"level"`
	Logger      string    `json:"logger"`
	Message     string    `json:"msg"`
	Time        time.Time `json:"ts"`
	Identifiers []string  `json:"identifiers"`
	Error       string    `json:"error"`
}

func (entry *logEntry) normalizeTerminalRenewalFailure() bool {
	if entry.Level != "error" || strings.Contains(entry.Error, context.Canceled.Error()) {
		return false
	}
	// CertMagic's periodic maintenance jobs report individual failures as "job failed".
	// Only the maintenance logger knows when that renewal's backoff window has ended.
	switch {
	case entry.Logger == "tls.cache.maintenance" && entry.Message == "final attempt; giving up until next scheduled renewal check finds the certificate still needs renewal":
		// The maintenance entry already carries all the certificate names.
	case entry.Logger == "tls" && entry.Message == "job failed":
		// A renewal queued by ManageAsync wraps the error after RenewCertAsync returns.
		// Its wrapper identifies the host and distinguishes it from issuance and periodic jobs.
		host, detail, found := strings.Cut(entry.Error, ": renewing certificate: ")
		if !found || host == "" || strings.ContainsAny(host, " []\t\r\n") {
			return false
		}
		entry.Identifiers = []string{host}
		entry.Error = detail
	default:
		return false
	}
	return len(entry.Identifiers) > 0 && entry.Identifiers[0] != "" && entry.Error != "" && !entry.Time.IsZero()
}

func (w *notificationWriter) Write(p []byte) (int, error) {
	var entry logEntry
	if w.client == nil || json.Unmarshal(p, &entry) != nil || !entry.normalizeTerminalRenewalFailure() {
		return len(p), nil
	}
	host := entry.Identifiers[0]
	message, err := renewalMessage(w.target, entry, w.redact.Replace(entry.Error))
	if err != nil {
		return len(p), fmt.Errorf("rendering certificate renewal email for %s: %w", host, err)
	}
	// Only terminal failures send mail. The SDK bounds each request, including response reads.
	response, err := w.client.SendMail(context.Background(), message)
	if err != nil {
		return len(p), fmt.Errorf("sending certificate renewal email for %s: %w", host, err)
	}
	if response.HTTPCode < 200 || response.HTTPCode >= 300 || response.Status < 200 || response.Status >= 300 ||
		(response.Data != nil && response.Data.Success != nil && !*response.Data.Success) {
		return len(p), fmt.Errorf("mail API rejected certificate renewal email for %s (HTTP %d, API status %d)", host, response.HTTPCode, response.Status)
	}
	return len(p), nil
}

var (
	_ caddy.WriterOpener    = Writer{}
	_ caddyfile.Unmarshaler = Writer{}
)
