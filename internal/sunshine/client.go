package sunshine

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

var (
	// errUnauthorized: Sunshine rejected vosd's API credentials (or has
	// none yet and redirects to its welcome page).
	errUnauthorized = errors.New("sunshine rejected the API credentials")
	// errNotSupported: the endpoint does not exist in this Sunshine.
	errNotSupported = errors.New("not supported by this Sunshine version")
)

// Client talks to Sunshine's local configuration API on
// https://127.0.0.1:47990 as vosd's API user.
//
// Sunshine serves that port with a self-signed certificate, so ordinary
// verification is off and the leaf certificate is pinned instead: it must
// be the one in ~vapor/.config/sunshine/credentials/cacert.pem, whenever
// that file exists. Requests never carry Origin or Referer, which is how
// Sunshine tells a script from a browser and skips its CSRF token.
type Client struct {
	base     string
	user     string
	password string
	pinPath  string
	hc       *http.Client
}

func NewClient(base, user, password, pinPath string) *Client {
	c := &Client{base: strings.TrimRight(base, "/"), user: user, password: password, pinPath: pinPath}
	c.hc = &http.Client{
		Transport: &http.Transport{
			Proxy: nil, // never send loopback traffic to a proxy
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // self-signed; VerifyConnection pins it instead
				VerifyConnection:   c.verifyPin,
			},
			MaxIdleConns:        2,
			IdleConnTimeout:     30 * time.Second,
			TLSHandshakeTimeout: 5 * time.Second,
		},
		// A redirect means "not logged in" (to /welcome when Sunshine has
		// no credentials at all); never follow it.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return c
}

// verifyPin accepts the server only if its leaf certificate is Sunshine's.
func (c *Client) verifyPin(cs tls.ConnectionState) error {
	if len(cs.PeerCertificates) == 0 {
		return errors.New("sunshine: no server certificate")
	}
	data, err := os.ReadFile(c.pinPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // Sunshine has not created its certificate yet
	}
	if err != nil {
		return fmt.Errorf("sunshine: reading pinned certificate: %w", err)
	}
	for len(data) > 0 {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		if _, err := x509.ParseCertificate(block.Bytes); err != nil {
			return fmt.Errorf("sunshine: pinned certificate: %w", err)
		}
		if bytes.Equal(block.Bytes, cs.PeerCertificates[0].Raw) {
			return nil
		}
		return errors.New("sunshine: server certificate does not match " + c.pinPath)
	}
	return errors.New("sunshine: no certificate in " + c.pinPath)
}

const defaultAPITimeout = 10 * time.Second

// do sends one request. body, when not nil, is sent as JSON; out, when
// not nil, receives the JSON response.
func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	resp, cancel, err := c.send(ctx, method, path, body)
	if err != nil {
		return err
	}
	defer cancel()
	defer resp.Body.Close()
	if out == nil {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out); err != nil {
		return fmt.Errorf("sunshine %s %s: %w", method, path, err)
	}
	return nil
}

// send performs the request and maps Sunshine's error statuses. On
// success the caller closes the body and then calls cancel, which ends
// the default timeout applied when ctx has no deadline of its own.
func (c *Client) send(ctx context.Context, method, path string, body any) (*http.Response, context.CancelFunc, error) {
	cancel := context.CancelFunc(func() {})
	if _, ok := ctx.Deadline(); !ok {
		ctx, cancel = context.WithTimeout(ctx, defaultAPITimeout)
	}
	resp, err := c.roundTrip(ctx, method, path, body)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	return resp, cancel, nil
}

func (c *Client) roundTrip(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(c.user, c.password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("sunshine %s %s: %w", method, path, err)
	}
	if resp.StatusCode/100 == 2 {
		return resp, nil
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode/100 == 3:
		return nil, errUnauthorized
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("sunshine %s %s: %w", method, path, errNotSupported)
	}
	var e struct {
		Error string `json:"error"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&e)
	if e.Error == "" {
		e.Error = resp.Status
	}
	return nil, fmt.Errorf("sunshine %s %s: %s", method, path, e.Error)
}

// Pairing is a Moonlight client waiting for its PIN (GET /api/pin).
type Pairing struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
}

// PendingPairings lists clients waiting for a PIN. Sunshine versions from
// before pairing ids return errNotSupported.
func (c *Client) PendingPairings(ctx context.Context) ([]Pairing, error) {
	var out struct {
		Pairings []Pairing `json:"pairings"`
	}
	if err := c.do(ctx, "GET", "/api/pin", nil, &out); err != nil {
		return nil, err
	}
	if out.Pairings == nil {
		out.Pairings = []Pairing{}
	}
	return out.Pairings, nil
}

var pairingIDRe = regexp.MustCompile(`^[0-9A-Fa-f]{32}$`)

// Pair submits the PIN Moonlight shows. Sunshine holds the request open
// until Moonlight finishes the handshake and answers whether it did.
// With pairingID == "" it uses the older API that paired whichever client
// was waiting.
func (c *Client) Pair(ctx context.Context, pairingID, pin, name string) (bool, error) {
	body := map[string]string{"pin": pin, "name": name}
	if pairingID != "" {
		body["pairing_id"] = pairingID
	}
	var out struct {
		Status bool `json:"status"`
	}
	if err := c.do(ctx, "POST", "/api/pin", body, &out); err != nil {
		return false, err
	}
	return out.Status, nil
}

// PairedClient is one paired Moonlight client (GET /api/clients/list).
type PairedClient struct {
	UUID    string `json:"uuid"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func (c *Client) Clients(ctx context.Context) ([]PairedClient, error) {
	var out struct {
		NamedCerts []struct {
			UUID    string `json:"uuid"`
			Name    string `json:"name"`
			Enabled *bool  `json:"enabled"` // absent before Sunshine could disable clients
		} `json:"named_certs"`
	}
	if err := c.do(ctx, "GET", "/api/clients/list", nil, &out); err != nil {
		return nil, err
	}
	clients := []PairedClient{}
	for _, n := range out.NamedCerts {
		clients = append(clients, PairedClient{UUID: n.UUID, Name: n.Name, Enabled: n.Enabled == nil || *n.Enabled})
	}
	return clients, nil
}

// Unpair removes a paired client; false when Sunshine did not know it.
func (c *Client) Unpair(ctx context.Context, uuid string) (bool, error) {
	var out struct {
		Status bool `json:"status"`
	}
	if err := c.do(ctx, "POST", "/api/clients/unpair", map[string]string{"uuid": uuid}, &out); err != nil {
		return false, err
	}
	return out.Status, nil
}

// Version returns the running Sunshine's version (from GET /api/config).
func (c *Client) Version(ctx context.Context) (string, error) {
	var out struct {
		Version string `json:"version"`
	}
	if err := c.do(ctx, "GET", "/api/config", nil, &out); err != nil {
		return "", err
	}
	return out.Version, nil
}

// Logs returns Sunshine's log file as it serves it (GET /api/logs).
func (c *Client) Logs(ctx context.Context) (string, error) {
	resp, cancel, err := c.send(ctx, "GET", "/api/logs", nil)
	if err != nil {
		return "", err
	}
	defer cancel()
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return string(b), err
}

// CloseApp ends the running app, which ends the stream (the client drops
// back to its app list instead of staring at a frozen picture).
func (c *Client) CloseApp(ctx context.Context) error {
	return c.do(ctx, "POST", "/api/apps/close", nil, nil)
}

// Restart asks Sunshine to re-exec itself. It may drop the connection
// before answering, which counts as success.
func (c *Client) Restart(ctx context.Context) error {
	err := c.do(ctx, "POST", "/api/restart", nil, nil)
	if err != nil && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || strings.Contains(err.Error(), "connection reset")) {
		return nil
	}
	return err
}

// serverInfo is the part of Sunshine's GameStream /serverinfo we read.
type serverInfo struct {
	XMLName     xml.Name `xml:"root"`
	State       string   `xml:"state"`
	CurrentGame int      `xml:"currentgame"`
}

// parseServerInfo reports whether Sunshine is streaming: its serverinfo
// state is SUNSHINE_SERVER_BUSY while an app session is live, even when
// the client's control connection is idle.
func parseServerInfo(data []byte) (bool, error) {
	var si serverInfo
	if err := xml.Unmarshal(data, &si); err != nil {
		return false, fmt.Errorf("serverinfo: %w", err)
	}
	return strings.TrimSpace(si.State) == "SUNSHINE_SERVER_BUSY", nil
}

// fetchServerInfo asks Sunshine's plain-HTTP GameStream port, which needs
// no pairing or credentials for serverinfo.
func fetchServerInfo(ctx context.Context, hc *http.Client, url string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return false, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("serverinfo: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return false, err
	}
	return parseServerInfo(data)
}
