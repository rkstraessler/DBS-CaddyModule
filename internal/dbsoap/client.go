// Package dbsoap implements the Domain-Bestellsystem SOAP 1.1 DNS operations.
package dbsoap

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	namespace         = "http://soap.domain-bestellsystem.de/soap/1.1/"
	envelopeNamespace = "http://schemas.xmlsoap.org/soap/envelope/"
	zoneInfo          = "nameserverZoneInfo"
	createRR          = "nameserverRRCreate"
	deleteRR          = "nameserverRRDelete"
	maxResponse       = 8 << 20
)

type Client struct {
	endpoint string
	username string
	password string
	http     *http.Client
}

// New uses the system TLS trust store and refuses redirects.
func New(endpoint, username, password string) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("DBS_SOAP_ENDPOINT must be an HTTPS service URL without credentials, query, or fragment")
	}
	if strings.TrimSpace(username) == "" || strings.Contains(username, ":") || strings.TrimSpace(password) == "" {
		return nil, errors.New("valid DBS_SOAP_USERNAME and DBS_SOAP_PASSWORD are required")
	}
	return &Client{endpoint: u.String(), username: username, password: password, http: &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

type Record struct {
	Name string `xml:"name"`
	Type string `xml:"type"`
	Data string `xml:"data"`
	Aux  string `xml:"aux"`
	TTL  string `xml:"ttl"`
}

type zoneRecords struct {
	Items []Record `xml:"item"`
	Nil   string   `xml:"http://www.w3.org/2001/XMLSchema-instance nil,attr"`
}

type result struct {
	ReturnCode *int         `xml:"returnCode"`
	Origin     string       `xml:"origin"`
	Records    *zoneRecords `xml:"rrList"`
}

type response struct {
	XMLName xml.Name `xml:"http://schemas.xmlsoap.org/soap/envelope/ Envelope"`
	Body    struct {
		Operation struct {
			XMLName   xml.Name
			Return    *result `xml:"return"`
			FaultCode string  `xml:"faultcode"`
		} `xml:",any"`
	} `xml:"http://schemas.xmlsoap.org/soap/envelope/ Body"`
}

type apiError struct {
	operation string
	code      int
}

func (e *apiError) Error() string {
	return fmt.Sprintf("DBS SOAP %s returned code %d", e.operation, e.code)
}

type stringField struct {
	XSIType string `xml:"xsi:type,attr"`
	Value   string `xml:",chardata"`
}

func field(value string) stringField {
	return stringField{XSIType: "xsd:string", Value: value}
}

type wireRecord struct {
	XSIType string      `xml:"xsi:type,attr"`
	Name    stringField `xml:"name"`
	Type    stringField `xml:"type"`
	Data    stringField `xml:"data"`
	Aux     stringField `xml:"aux"`
	TTL     stringField `xml:"ttl"`
}

func toWire(r Record) wireRecord {
	return wireRecord{"dbs:NameserverRRElement", field(r.Name), field(r.Type), field(r.Data), field(r.Aux), field(r.TTL)}
}

type recordArray struct {
	XSIType string       `xml:"xsi:type,attr"`
	Items   []wireRecord `xml:"item"`
}

type request struct {
	XMLName xml.Name
	XSIType string      `xml:"xsi:type,attr"`
	Origin  stringField `xml:"soaOrigin"`
	Record  any         `xml:"rr,omitempty"`
}

func (c *Client) Zone(ctx context.Context, zone string) ([]Record, error) {
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	var err error
	for attempt := range 3 {
		var response *result
		var retry bool
		response, retry, err = c.call(ctx, zoneInfo, request{Origin: field(zone)})
		if err == nil {
			if !strings.EqualFold(strings.TrimSuffix(response.Origin, "."), zone) || response.Records == nil ||
				response.Records.Nil == "true" || response.Records.Nil == "1" {
				return nil, errors.New("DBS SOAP returned incomplete or mismatched zone data")
			}
			return response.Records.Items, nil
		}
		if !retry || attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(1<<attempt) * 200 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, err
}

func (c *Client) Create(ctx context.Context, zone string, record Record) error {
	_, _, err := c.call(ctx, createRR, request{Origin: field(zone), Record: recordArray{
		XSIType: "dbs:ArrayOfNameserverRRElement", Items: []wireRecord{toWire(record)},
	}})
	return err
}

func (c *Client) Delete(ctx context.Context, zone string, record Record) (bool, error) {
	wire := toWire(record)
	_, _, err := c.call(ctx, deleteRR, request{Origin: field(zone), Record: &wire})
	var api *apiError
	if errors.As(err, &api) && api.code == 2303 {
		return false, nil
	}
	return err == nil, err
}

func (c *Client) call(ctx context.Context, operation string, payload request) (*result, bool, error) {
	payload.XMLName = xml.Name{Local: operation + "Request"}
	payload.XSIType = "dbs:" + strings.ToUpper(operation[:1]) + operation[1:] + "RequestObject"
	data, err := xml.Marshal(payload)
	if err != nil {
		return nil, false, errors.New("cannot encode DBS SOAP request")
	}
	prefix := xml.Header + `<env:Envelope xmlns:env="` + envelopeNamespace + `" xmlns:dbs="` + namespace +
		`" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:xsd="http://www.w3.org/2001/XMLSchema"><env:Body><dbs:` +
		operation + ` env:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">`
	suffix := "</dbs:" + operation + "></env:Body></env:Envelope>"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(prefix+string(data)+suffix))
	if err != nil {
		return nil, false, errors.New("cannot construct DBS SOAP request")
	}
	// Keep writes single-attempt, including at the transport layer.
	req.GetBody = nil
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("SOAPAction", `"`+namespace+"#"+operation+`"`)
	req.SetBasicAuth(c.username, c.password)
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, false, ctx.Err()
		}
		return nil, true, fmt.Errorf("DBS SOAP %s transport failed", operation)
	}
	defer res.Body.Close()
	status := res.StatusCode
	if status != http.StatusOK && status != http.StatusInternalServerError {
		retry := status == http.StatusTooManyRequests || status == http.StatusBadGateway ||
			status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
		return nil, retry, fmt.Errorf("DBS SOAP %s returned HTTP %d", operation, status)
	}
	decoded, err := decodeResponse(operation, res.Body)
	op := decoded.Body.Operation
	if err == nil && op.XMLName == (xml.Name{Space: envelopeNamespace, Local: "Fault"}) {
		return nil, false, faultError(operation, status, op.FaultCode)
	}
	if status != http.StatusOK {
		return nil, false, fmt.Errorf("DBS SOAP %s returned HTTP %d", operation, status)
	}
	if err != nil {
		return nil, false, err
	}
	// The manual also shows this legacy response namespace.
	if op.XMLName.Local != operation+"Response" ||
		(op.XMLName.Space != namespace && op.XMLName.Space != "http://www.domain-bestellsystem.com/soap/1.1/") ||
		op.Return == nil || op.Return.ReturnCode == nil {
		return nil, false, fmt.Errorf("unexpected DBS SOAP %s response", operation)
	}
	if *op.Return.ReturnCode != 1000 {
		return nil, false, &apiError{operation, *op.Return.ReturnCode}
	}
	return op.Return, false, nil
}

func decodeResponse(operation string, reader io.Reader) (response, error) {
	body, err := io.ReadAll(io.LimitReader(reader, maxResponse+1))
	if err != nil || len(body) > maxResponse {
		return response{}, fmt.Errorf("cannot read bounded DBS SOAP %s response", operation)
	}
	var decoded response
	if err := xml.Unmarshal(body, &decoded); err != nil {
		return response{}, fmt.Errorf("invalid XML in DBS SOAP %s response", operation)
	}
	return decoded, nil
}

func faultError(operation string, status int, value string) error {
	message := fmt.Sprintf("DBS SOAP %s returned HTTP %d", operation, status)
	if status == http.StatusOK {
		message = fmt.Sprintf("DBS SOAP %s returned a fault", operation)
	}
	// Only report a numeric DBS code from the fault envelope. The server's
	// message and detail fields may contain request data.
	code, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || code < 1000 || code > 9999 {
		return errors.New(message)
	}
	if code == 2200 {
		return fmt.Errorf("%s (SOAP fault code %d: authentication rejected)", message, code)
	}
	return fmt.Errorf("%s (SOAP fault code %d)", message, code)
}
