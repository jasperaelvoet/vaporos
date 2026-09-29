package update

import (
	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/config"
)

// SlotStatus describes what one slot's loader entry boots.
type SlotStatus struct {
	Version   string `json:"version"`
	Running   bool   `json:"running"`
	Bootable  bool   `json:"bootable"` // not out of tries
	Counting  bool   `json:"counting"` // still on trial
	TriesLeft int    `json:"tries_left"`
	TriesDone int    `json:"tries_done"`
	Entry     string `json:"entry"`
}

// Status is `vos status --json`: the running image, both slots and the
// update state.
type Status struct {
	State
	Version    string                 `json:"version"`
	Channel    string                 `json:"channel"`
	Mode       string                 `json:"mode"` // "os" | "live"
	BootedSlot string                 `json:"booted_slot"`
	Slots      map[string]*SlotStatus `json:"slots"`               // "a", "b"; null when empty
	ESPError   string                 `json:"esp_error,omitempty"` // why slots are unknown
}

// GetStatus gathers Status. It never fails: what cannot be read is left
// empty and, for the ESP, explained in ESPError.
func GetStatus() *Status {
	img := bootedImage()
	s := &Status{Version: img.Version, Channel: img.Channel, Mode: "os", BootedSlot: config.BootedSlot()}
	if st, err := LoadState(); err == nil {
		s.State = *st
	} else {
		s.State = State{Failed: []string{}}
	}
	s.Booted = img.Version
	if config.IsLive() {
		s.Mode = "live"
		return s
	}
	if err := boot.EnsureESP(config.ESP); err != nil {
		s.ESPError = err.Error()
		return s
	}
	s.Slots = map[string]*SlotStatus{}
	for _, slot := range []string{"a", "b"} {
		ss, err := slotStatus(slot, s.BootedSlot)
		if err != nil {
			s.ESPError = err.Error()
		}
		s.Slots[slot] = ss
	}
	return s
}

// slotStatus returns nil for a slot without an entry.
func slotStatus(slot, booted string) (*SlotStatus, error) {
	e, err := boot.EntryForSlot(config.ESP, slot)
	if err != nil || e == nil {
		return nil, err
	}
	return &SlotStatus{
		Version:   e.Version,
		Running:   slot == booted,
		Bootable:  e.Bootable(),
		Counting:  e.Counting,
		TriesLeft: e.Left,
		TriesDone: e.Done,
		Entry:     e.Name(),
	}, nil
}

// NextBoot is the entry a restart starts, in GET /update.
type NextBoot struct {
	Slot    string `json:"slot"`
	Version string `json:"version"`
}

// nextBoot returns what a restart starts when that is not the running slot
// (a staged update, or a rollback waiting for a restart), or nil. VaporOS
// entries share one sort-key, and stages and rollbacks clear systemd-boot's
// saved choices, so it starts the entry that sorts first across both slots:
// bootable first, then the newest version. On a tie the running slot stays.
func nextBoot(booted string) (*NextBoot, error) {
	es, err := boot.Entries(config.ESP)
	if err != nil {
		return nil, err
	}
	var next *boot.Entry
	for i := range es {
		e := &es[i]
		if next == nil || bootsFirst(e, next) || (e.Slot == booted && !bootsFirst(next, e)) {
			next = e
		}
	}
	if next == nil || next.Slot == booted {
		return nil, nil
	}
	return &NextBoot{Slot: next.Slot, Version: next.Version}, nil
}

// bootsFirst is systemd-boot's order for two VaporOS entries.
func bootsFirst(a, b *boot.Entry) bool {
	if a.Bootable() != b.Bootable() {
		return a.Bootable()
	}
	return boot.CompareVersions(a.Version, b.Version) > 0
}
