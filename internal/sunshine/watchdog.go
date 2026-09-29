package sunshine

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/config"
)

// The KMS plane-loss freeze. Stock Sunshine captures one DRM plane chosen
// when the stream starts. If gamescope later scans out through another
// plane (after a modeset, or when it stops compositing), that plane has no
// framebuffer any more and every capture attempt logs
//
//	Couldn't get drm fb for plane [N]: No such file or directory
//
// and returns a timeout, forever: Moonlight shows a frozen picture and
// never reconnects. A brief blip recovers on its own (the next frame finds
// a framebuffer again); a real freeze keeps logging at the frame rate.
// So: at least watchThreshold such lines within watchWindow, spread over at
// least watchMinSpan, mean a freeze, and vosd ends the app and restarts
// Sunshine so the client can reconnect to a fresh capture.
const (
	watchWindow    = 10 * time.Second
	watchThreshold = 5
	watchMinSpan   = 2 * time.Second
	// watchCooldown gives a restarted Sunshine time to come back before
	// the watchdog may act again.
	watchCooldown = 60 * time.Second
	// maxWatchHits bounds memory at high frame rates.
	maxWatchHits = 4096
)

// planeErrorMarkers are Sunshine's kmsgrab messages for a plane that lost
// its framebuffer.
var planeErrorMarkers = []string{
	"Couldn't get drm fb for plane",
	"Couldn't get drm plane",
}

func isPlaneError(line string) bool {
	for _, m := range planeErrorMarkers {
		if strings.Contains(line, m) {
			return true
		}
	}
	return false
}

// planeWatch counts plane errors in a sliding window.
type planeWatch struct {
	hits []time.Time
}

// observe feeds one log line and reports whether the stream is frozen.
func (p *planeWatch) observe(line string, now time.Time) bool {
	if !isPlaneError(line) {
		return false
	}
	p.hits = append(p.hits, now)
	cut := 0
	for cut < len(p.hits) && now.Sub(p.hits[cut]) > watchWindow {
		cut++
	}
	if len(p.hits)-cut > maxWatchHits {
		cut = len(p.hits) - maxWatchHits
	}
	p.hits = p.hits[cut:]
	return len(p.hits) >= watchThreshold && now.Sub(p.hits[0]) >= watchMinSpan
}

func (p *planeWatch) reset() { p.hits = nil }

// followJournal streams the vos-sunshine user unit's log lines until ctx
// ends: the last historyLines of this boot first, then new ones. vosd is
// root, which reads the gaming user's journal directly; matching on the
// unit and uid picks exactly Sunshine's output (journalctl --user-unit
// would match root's uid instead).
//
// The history lets the client count include clients that connected before
// vosd started following (it follows only once an app runs, and vosd may
// have restarted mid-stream). short-unix output puts the journal timestamp
// in front of each line, which tells the replayed history (lineAt) apart
// from new lines; the markers are matched anywhere in a line.
func followJournal(ctx context.Context) (<-chan string, error) {
	cmd := exec.CommandContext(ctx, "journalctl", "--follow", "--boot", "--quiet", "--no-pager",
		"--lines="+strconv.Itoa(historyLines), "--output=short-unix",
		"_SYSTEMD_USER_UNIT="+unitName, "_UID="+strconv.Itoa(config.GamerUID))
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	lines := make(chan string, 256)
	go func() {
		defer close(lines)
		defer cmd.Wait()
		sc := bufio.NewScanner(out)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			select {
			case lines <- sc.Text():
			case <-ctx.Done():
				return
			}
		}
	}()
	return lines, nil
}

// journalTail returns the unit's last n log lines, for when neither
// Sunshine nor its log file can supply them.
func journalTail(ctx context.Context, n int) (string, error) {
	out, err := exec.CommandContext(ctx, "journalctl", "--output=cat", "--no-pager", "--lines="+strconv.Itoa(n),
		"_SYSTEMD_USER_UNIT="+unitName, "_UID="+strconv.Itoa(config.GamerUID)).Output()
	return string(out), err
}
