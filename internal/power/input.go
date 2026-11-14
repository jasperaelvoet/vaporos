package power

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// A Moonlight stream keeps the machine awake only while someone is at it.
// A client left connected to Steam's home screen (a TV that stayed on, a
// laptop that never slept) looks to Sunshine like any other stream, and
// kept the PC on for as long as it stayed. So a stream counts as busy
// only with input in the last attendedWindow; a game that runs keeps the
// machine awake by itself, input or not.
const (
	// attendedWindow is how long after the last input a stream still
	// counts as attended. It spans several passes of the policy, so
	// someone reading a page between two presses is not idle in between.
	attendedWindow = time.Minute
	// inputPollEvery bounds how often the devices are read.
	inputPollEvery = time.Second
	// inputReads bounds the reads of one device in one poll.
	inputReads = 64
	// propAccelerometer is INPUT_PROP_ACCELEROMETER: a controller's motion
	// sensors report all the time, held or not.
	propAccelerometer = 6
)

// InputWatch notices input on the machine's input devices: the virtual
// keyboard, mouse and controllers Sunshine feeds a client's input into,
// and anything plugged in.
type InputWatch struct {
	dev string // /dev/input
	sys string // /sys/class/input
	now func() time.Time

	mu     sync.Mutex
	fds    map[string]int // open event devices by name
	last   time.Time      // the last input seen
	polled time.Time
}

// NewInputWatch watches the running system. The time it is made counts as
// input, so a stream that vosd finds on its start is not idle at once.
func NewInputWatch() *InputWatch {
	return &InputWatch{dev: "/dev/input", sys: "/sys/class/input", now: time.Now}
}

// Attended wraps a stream's busy check: busy only while f says so and
// there was input within attendedWindow. The stream starting, and its
// reason changing (a client that left or came back), count as input.
func (w *InputWatch) Attended(f BusyFunc) BusyFunc {
	var mu sync.Mutex
	prev := ""
	return func() (bool, string) {
		busy, why := f()
		if !busy {
			why = ""
		} else if why == "" {
			why = "busy"
		}
		mu.Lock()
		changed := why != prev
		prev = why
		mu.Unlock()
		if !busy {
			return false, ""
		}
		if changed {
			w.touch()
		}
		if !w.recent() {
			return false, ""
		}
		return true, why
	}
}

func (w *InputWatch) touch() {
	w.mu.Lock()
	w.last = w.now()
	w.mu.Unlock()
}

// recent reports whether there was input within attendedWindow.
func (w *InputWatch) recent() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now()
	if w.last.IsZero() {
		w.last = now
	}
	if w.polled.IsZero() || now.Sub(w.polled) >= inputPollEvery || now.Before(w.polled) {
		w.polled = now
		if w.pollLocked() {
			w.last = now
		}
	}
	return now.Sub(w.last) < attendedWindow
}

// pollLocked opens the event devices that appeared, forgets the ones that
// went and reports whether any has events since the last poll. A device
// that appears counts as input: someone connected a controller.
func (w *InputWatch) pollLocked() bool {
	if w.fds == nil {
		w.fds = map[string]int{}
	}
	active := false
	present := map[string]bool{}
	ents, _ := os.ReadDir(w.dev)
	for _, e := range ents {
		name := e.Name()
		if !strings.HasPrefix(name, "event") {
			continue
		}
		present[name] = true
		if _, ok := w.fds[name]; ok || w.motionSensor(name) {
			continue
		}
		fd, err := syscall.Open(filepath.Join(w.dev, name), syscall.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
		if err != nil {
			continue
		}
		w.fds[name] = fd
		active = true
	}
	buf := make([]byte, 24*64) // whole input_event structs
	for name, fd := range w.fds {
		got, gone := drain(fd, buf)
		active = active || got
		if gone || !present[name] {
			syscall.Close(fd)
			delete(w.fds, name)
		}
	}
	return active
}

// drain reads what fd has without waiting. gone means the device is no
// longer there.
func drain(fd int, buf []byte) (got, gone bool) {
	for range inputReads {
		n, err := syscall.Read(fd, buf)
		switch {
		case errors.Is(err, syscall.EINTR):
		case errors.Is(err, syscall.EAGAIN):
			return got, false
		case err != nil:
			return got, true
		case n <= 0:
			return got, false
		default:
			got = true
		}
	}
	return got, false
}

// motionSensor reports whether an event device is a controller's motion
// sensors, from the properties bitmap in sysfs.
func (w *InputWatch) motionSensor(name string) bool {
	b, err := os.ReadFile(filepath.Join(w.sys, name, "device", "properties"))
	if err != nil {
		return false
	}
	// The bitmap is printed as words, the lowest last.
	f := strings.Fields(string(b))
	if len(f) == 0 {
		return false
	}
	v, err := strconv.ParseUint(f[len(f)-1], 16, 64)
	return err == nil && v&(1<<propAccelerometer) != 0
}
