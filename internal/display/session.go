package display

import "time"

// Session is the Moonlight session in progress as session.begin and
// GET /status carry it (docs/CONTRACTS.md "Events").
type Session struct {
	Client string    `json:"client"`
	App    string    `json:"app,omitempty"` // Sunshine's app: "Steam" or a game's name
	Mode   string    `json:"mode"`
	HDR    bool      `json:"hdr"`
	Since  time.Time `json:"since"` // UTC, whole seconds
	// Screen is the device's screen as Begin resolved it, nil while
	// display.ui_scaling is false (docs/CONTRACTS.md, Display policy,
	// Scaling).
	Screen *ScreenRef `json:"screen,omitempty"`
}

func (s *sessionInfo) public() Session {
	p := Session{Client: s.Client, App: s.App, Mode: s.Mode, HDR: s.HDR, Since: s.Since.UTC().Truncate(time.Second)}
	if s.screen != nil {
		ref := *s.screen
		p.Screen = &ref
	}
	return p
}

// CurrentSession is the session in progress, nil without one.
func (m *Manager) CurrentSession() *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == nil {
		return nil
	}
	s := m.session.public()
	return &s
}
