package renewalmail_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/caddyserver/caddy/v2"
	"github.com/caddyserver/caddy/v2/caddyconfig"
	"github.com/caddyserver/caddy/v2/modules/caddytls"
	_ "github.com/caddyserver/caddy/v2/modules/standard"
	"go.uber.org/zap"

	_ "github.com/rkstraessler/DBS-CaddyModule"
	"github.com/rkstraessler/DBS-CaddyModule/internal/renewalmail"
)

const (
	targetEmail = "ops@example.test"
	authKey     = "fixture-auth"
	adapterKey  = "fixture-adapter"
)

func setMailEnvironment(t *testing.T, baseURL string) {
	t.Helper()
	t.Setenv("MAIL_API_URL", baseURL)
	t.Setenv("MAIL_AUTH_KEY", authKey)
	t.Setenv("MAIL_ADAPTER_KEY", adapterKey)
	t.Setenv("MAIL_TO_TARGET", targetEmail)
}

func TestCaddyRenewalMail(t *testing.T) {
	type mailRequest struct {
		To []struct {
			Email string `json:"email"`
		} `json:"to"`
		Subject string `json:"subject"`
		HTML    string `json:"html"`
		Text    string `json:"text"`
	}
	messages := make(chan mailRequest, 16)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var message mailRequest
		err := json.NewDecoder(r.Body).Decode(&message)
		if err != nil || r.Method != http.MethodPost || r.URL.Path != "/api/v2/send" ||
			r.Header.Get("Authorization") != "Bearer "+authKey || r.Header.Get("X-Adapter-Key") != adapterKey {
			t.Error("invalid Mail API request")
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		messages <- message
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"status":202,"data":{"success":true,"queueIds":[12]}}`)
	}))
	t.Cleanup(server.Close)
	setMailEnvironment(t, server.URL)
	t.Setenv("DBS_SOAP_PASSWORD", "fixture-soap-password")

	path := filepath.Join("..", "..", "..", "Caddyfile")
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	adapted, _, err := caddyconfig.GetAdapter("caddyfile").Adapt(file, map[string]any{"filename": path})
	if err != nil {
		t.Fatal(err)
	}
	var config caddy.Config
	if err := json.Unmarshal(adapted, &config); err != nil {
		t.Fatal(err)
	}
	if config.Logging == nil {
		t.Fatal("the shipped Caddyfile must configure the mail logger")
	}
	if defaultLog := config.Logging.Logs["default"]; defaultLog != nil && slices.Contains(defaultLog.Exclude, "tls") {
		t.Fatal("the mail logger must leave TLS diagnostics in the default log")
	}
	// Keep the shipped logging configuration and exercise it without starting ACME or the admin listener.
	config.AppsRaw = nil
	persist := false
	config.Admin = &caddy.AdminConfig{Disabled: true, Config: &caddy.ConfigSettings{Persist: &persist}}
	isolated, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := caddy.Load(isolated, true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := caddy.Stop(); err != nil {
			t.Error(err)
		}
	})

	tlsLog := caddy.ActiveContext().Logger(new(caddytls.TLS))
	renew := tlsLog.Named("renew")
	renew.Error("could not get certificate from issuer", zap.String("identifier", "retry.example.test"), zap.Error(errors.New("issuer unavailable")))
	tlsLog.Error("job failed", zap.Error(errors.New("[retry.example.test] DNS timed out")))
	renew.Error("will retry", zap.Int("attempt", 1), zap.Error(errors.New("DNS timed out")))
	renew.Info("certificate renewed successfully", zap.String("identifier", "retry.example.test"))
	tlsLog.Error("job failed", zap.Error(errors.New("new.example.test: obtaining certificate: DNS timed out")))
	tlsLog.Error("job failed", zap.Error(errors.New("stopped.example.test: renewing certificate: context canceled")))
	if len(messages) != 0 {
		t.Fatal("a retry, successful renewal, initial issuance, or cancellation sent email")
	}

	tlsLog.Named("cache").Named("maintenance").Error(
		"final attempt; giving up until next scheduled renewal check finds the certificate still needs renewal",
		zap.Strings("identifiers", []string{"*.example.test", "other.example.test"}),
		zap.Error(errors.New("DNS refused <script> & fixture-soap-password")),
	)
	tlsLog.Error("job failed", zap.Error(errors.New("existing.example.test: renewing certificate: ACME account rejected")))
	if len(messages) != 2 {
		t.Fatalf("expected one email per terminal failure, got %d", len(messages))
	}
	message := <-messages
	if len(message.To) != 1 || message.To[0].Email != targetEmail || !strings.Contains(message.Subject, "*.example.test") {
		t.Fatalf("unexpected delivery or subject: %+v", message)
	}
	for _, fragment := range []string{"*.example.test", "other.example.test", " UTC", "DNS refused &lt;script&gt; &amp; [redacted]"} {
		if !strings.Contains(message.HTML, fragment) {
			t.Errorf("HTML is missing %q", fragment)
		}
	}
	if !strings.Contains(message.Text, "DNS refused <script> & [redacted]") ||
		strings.Contains(message.HTML+message.Text, "fixture-soap-password") {
		t.Error("email omitted the error or exposed a credential")
	}
	message = <-messages
	if !strings.Contains(message.Subject, "existing.example.test") || !strings.Contains(message.Text, "ACME account rejected") {
		t.Error("email for a terminal asynchronous renewal is missing its host or error")
	}
}

func TestMailWriterConfiguration(t *testing.T) {
	for _, name := range []string{"MAIL_API_URL", "MAIL_AUTH_KEY", "MAIL_ADAPTER_KEY", "MAIL_TO_TARGET"} {
		t.Setenv(name, "")
	}
	module := renewalmail.Writer{}
	writer, err := module.OpenWriter()
	if err != nil {
		t.Fatal("all mail settings must be optional", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MAIL_TO_TARGET", targetEmail)
	if _, err := module.OpenWriter(); err == nil {
		t.Fatal("accepted partial mail settings")
	}
	setMailEnvironment(t, "http://mail.example.test")
	if _, err := module.OpenWriter(); err == nil {
		t.Fatal("accepted a remote plaintext endpoint")
	}
	setMailEnvironment(t, "https://mail.example.test")
	t.Setenv("MAIL_TO_TARGET", "invalid-address")
	if _, err := module.OpenWriter(); err == nil {
		t.Fatal("accepted an invalid recipient")
	}
}

func TestMailWriterReportsRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"status":422,"data":{"success":false,"error":"fixture-private-response"}}`)
	}))
	t.Cleanup(server.Close)
	setMailEnvironment(t, server.URL)
	writer, err := (renewalmail.Writer{}).OpenWriter()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	log := []byte(`{"level":"error","logger":"tls","msg":"job failed","ts":"2026-11-19T12:00:00Z","error":"api.example.test: renewing certificate: DNS failed"}`)
	if n, err := writer.Write(log); n != len(log) || err == nil || !strings.Contains(err.Error(), "422") ||
		strings.Contains(err.Error(), "fixture-private-response") {
		t.Fatalf("expected a redacted Mail API rejection, got %d bytes with %v", n, err)
	}
}
