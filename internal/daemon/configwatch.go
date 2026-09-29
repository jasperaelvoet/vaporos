package daemon

import (
	"log"
	"os"
	"sync"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// configWatch answers "is web.allow_public set?" from config.json on disk.
// The file is stat'ed at most once a second and parsed only when it
// changed, so the check is cheap enough to run per request. Services save
// the shared config through config.Save, so their edits land here too.
type configWatch struct {
	mu      sync.Mutex
	now     func() time.Time
	checked time.Time
	modTime time.Time
	size    int64
	allow   bool
}

func newConfigWatch() *configWatch { return &configWatch{now: time.Now, size: -1} }

func (c *configWatch) allowPublic() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if !c.checked.IsZero() && now.Sub(c.checked) < time.Second && !now.Before(c.checked) {
		return c.allow
	}
	c.checked = now
	fi, err := os.Stat(config.ConfigPath())
	if err != nil {
		// Missing file: the default, which is local-only.
		c.allow, c.size = false, -1
		return false
	}
	if fi.Size() == c.size && fi.ModTime().Equal(c.modTime) {
		return c.allow
	}
	cfg, err := config.Load()
	if err != nil {
		// Fail closed: a config we cannot read does not open the UI to the
		// internet. Keep the stat so the log line appears once per edit.
		log.Printf("config: %v (web UI stays local-only)", err)
		c.allow = false
	} else {
		c.allow = cfg.Web.AllowPublic
	}
	c.modTime, c.size = fi.ModTime(), fi.Size()
	return c.allow
}
