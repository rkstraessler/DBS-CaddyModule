package renewalmail

import (
	_ "embed"
	"fmt"
	"html/template"
	"strings"

	"github.com/rkstraessler/ReikemMailClient_GO"
)

//go:embed failure.html
var failureHTML string

var failureTemplate = template.Must(template.New("failure").Parse(failureHTML))

func renewalMessage(target string, entry logEntry, detail string) (reikemmail.SendMailOptions, error) {
	data := struct {
		Host       string
		OtherNames string
		Time       string
		Error      string
	}{
		Host:       entry.Identifiers[0],
		OtherNames: strings.Join(entry.Identifiers[1:], ", "),
		Time:       entry.Time.UTC().Format("02 Jan 2006, 15:04:05 MST"),
		Error:      detail,
	}
	var html strings.Builder
	if err := failureTemplate.Execute(&html, data); err != nil {
		return reikemmail.SendMailOptions{}, err
	}
	var plain strings.Builder
	fmt.Fprintf(&plain, "Certificate renewal failed for %s.\n\nDetected at\n%s\n\n", data.Host, data.Time)
	if data.OtherNames != "" {
		fmt.Fprintf(&plain, "Other certificate names\n%s\n\n", data.OtherNames)
	}
	fmt.Fprintf(&plain, "Failure details\n%s\n", data.Error)
	text := plain.String()
	return reikemmail.SendMailOptions{
		To:      []reikemmail.MailAddress{{Email: target}},
		Subject: "[Caddy] Certificate renewal failed for " + data.Host,
		HTML:    html.String(),
		Text:    &text,
	}, nil
}
