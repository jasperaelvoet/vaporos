package steamui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
)

// Target is one entry of the debugger's /json/list. Its
// webSocketDebuggerUrl is never read: Dial builds the URL from ID.
type Target struct {
	ID    string `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

// validID is what a target id must look like before it goes into a URL.
var validID = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`).MatchString

// sharedTitles are the titles of Steam's SharedJSContext, where
// SteamClient and settingsStore live.
var sharedTitles = []string{"SharedJSContext", "Steam Shared Context presented by Valve™"}

// steamOrigin is where SharedJSContext is served from; a web page Steam
// shows can choose its own title, not this URL.
const steamOrigin = "https://steamloopback.host/"

// Targets returns the debugger's targets with a valid id, after checking
// who listens on the port.
func (c *Client) Targets(ctx context.Context) ([]Target, error) {
	if err := c.checkListener(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+c.addr()+"/json/list", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoDebugger, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("steamui: /json/list answered %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("steamui: /json/list: %v", err)
	}
	if len(body) > maxBody {
		return nil, errors.New("steamui: /json/list is over 1 MiB")
	}
	var all []Target
	if err := json.Unmarshal(body, &all); err != nil {
		return nil, fmt.Errorf("steamui: /json/list: %v", err)
	}
	out := make([]Target, 0, len(all))
	for _, t := range all {
		if validID(t.ID) {
			out = append(out, t)
		}
	}
	return out, nil
}

// Shared picks Steam's SharedJSContext: its title, and served by Steam.
func Shared(ts []Target) (Target, bool) {
	for _, t := range ts {
		for _, title := range sharedTitles {
			if t.Title == title && strings.HasPrefix(t.URL, steamOrigin) {
				return t, true
			}
		}
	}
	return Target{}, false
}

// Views picks the views Steam lays out late: Quick Access and the main
// menu (titled QuickAccess_uid2, MainMenu_uid2). Their URLs say nothing
// (about:blank), so only the title counts; vosd only reads their scale.
func Views(ts []Target) []Target {
	var out []Target
	for _, t := range ts {
		if strings.HasPrefix(t.Title, "QuickAccess_") || strings.HasPrefix(t.Title, "MainMenu_") {
			out = append(out, t)
		}
	}
	return out
}

// httpClient asks the debugger only: no proxy, no redirect, no
// compression (the body cap counts what arrives), no keep-alive, and a
// dialer that refuses every other address.
func (c *Client) httpClient() *http.Client {
	c.hcOnce.Do(func() {
		addr := c.addr()
		tr := &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, network, a string) (net.Conn, error) {
				if a != addr {
					return nil, fmt.Errorf("steamui: refusing to dial %s", a)
				}
				var d net.Dialer
				return d.DialContext(ctx, "tcp", addr)
			},
			DisableKeepAlives:      true,
			DisableCompression:     true,
			MaxResponseHeaderBytes: 64 << 10,
			ResponseHeaderTimeout:  listTimeout,
		}
		c.hc = &http.Client{
			Transport: tr,
			Timeout:   listTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	})
	return c.hc
}
