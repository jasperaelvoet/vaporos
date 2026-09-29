package web

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/boot"
	"github.com/jasperaelvoet/vaporos/internal/manifest"
	"github.com/jasperaelvoet/vaporos/internal/update"
)

// The dev server's fake of updates (internal/update): the A/B slots,
// checking, staging with the real phases, cancelling, going back, and
// restarting into the staged version. Document: base/update.json, the
// GET /update answer (the update-state plus config, slots and progress).

// updateStateKeys are the update-state fields (update.State), which
// update.state carries and replaces.
var updateStateKeys = []string{"booted", "staged", "failed", "available", "checked", "last_error", "held"}

// stagePhases are Stage's phases in order, with the share of the overall
// percent each covers (update/stage.go stageSpans), the bytes each
// counts, and how long the fake takes for it.
var stagePhases = []struct {
	name     string
	from, to int
	bytes    int64 // 0: the image's size
	took     time.Duration
}{
	{"download", 0, 2, 64_000_000, time.Second},
	{"write", 2, 80, 0, 8 * time.Second},
	{"verify", 80, 98, 0, 3 * time.Second},
	{"install", 98, 100, 64_000_000, 500 * time.Millisecond},
}

func stagePhaseIndex(phase string) int {
	for i, p := range stagePhases {
		if p.name == phase {
			return i
		}
	}
	return -1
}

// fakeStageRun is a stage in progress.
type fakeStageRun struct {
	progress  map[string]any // the last update.progress
	cancelled bool
}

func (f *devFake) updateRoutes(add fakeAdder) {
	add("GET", "/update", api.Authed, func(w http.ResponseWriter, r *http.Request) any { return f.updateAnswerLocked() })
	add("POST", "/update/check", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		u := f.doc("update")
		now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)
		u["checked"] = now
		if a := asObj(u["available"]); len(a) > 0 {
			a["checked"] = now
		}
		if asStr(u["last_error"]) != "" && len(asStr(u["last_error"])) > 7 && asStr(u["last_error"])[:7] == "check: " {
			u["last_error"] = ""
		}
		f.emitLocked("update.state", f.updateStateLocked())
		return map[string]any{"available": u["available"]}
	})
	add("POST", "/update/stage", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var req struct {
			Version string `json:"version"`
		}
		if err := api.ReadJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
			api.Error(w, http.StatusBadRequest, "%v", err)
			return nil
		}
		if req.Version != "" && !manifest.ValidVersion(req.Version) {
			api.Error(w, http.StatusBadRequest, "invalid version %q", req.Version)
			return nil
		}
		if f.stage != nil {
			api.Error(w, http.StatusConflict, "%v", update.ErrBusy)
			return nil
		}
		f.startStageLocked(req.Version, fakeStagePoint{})
		return fakeOK
	})
	add("POST", "/update/cancel", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		switch {
		case f.stage == nil:
			api.Error(w, http.StatusConflict, "no update is running")
		case f.stage.progress["phase"] == "install" || f.stage.progress["phase"] == "done":
			api.Error(w, http.StatusConflict, "VaporOS is already installing the update; it takes a few seconds")
		default:
			f.stage.cancelled = true
			return fakeOK
		}
		return nil
	})
	add("POST", "/update/activate", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		u := f.doc("update")
		staged := asObj(u["staged"])
		switch {
		case f.stage != nil:
			api.Error(w, http.StatusConflict, "an update is still being installed")
			return nil
		case asStr(staged["version"]) == "":
			api.Error(w, http.StatusConflict, "no update is staged")
			return nil
		}
		f.restartLocked(8*time.Second, nil) // the boot starts next_boot, the staged version
		return fakeOK
	})
	add("POST", "/update/rollback", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		if err := f.rollbackLocked(); err != nil {
			api.Error(w, http.StatusConflict, "%v", err)
			return nil
		}
		return fakeOK
	})
	add("PUT", "/update/settings", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var req struct {
			Channel *string `json:"channel"`
			Auto    *string `json:"auto"`
		}
		if !strictBody(w, r, &req) {
			return nil
		}
		if req.Channel != nil && *req.Channel != "" && !update.ValidChannel(*req.Channel) {
			api.Error(w, http.StatusBadRequest, "invalid channel %q", *req.Channel)
			return nil
		}
		if req.Auto != nil && *req.Auto != "stage" && *req.Auto != "off" {
			api.Error(w, http.StatusBadRequest, `auto must be "stage" or "off"`)
			return nil
		}
		u := f.doc("update")
		cfg := asObj(u["config"])
		if req.Channel != nil {
			ch := *req.Channel
			if ch == "" {
				ch = "main" // back to the image's channel
			}
			if cfg["channel"] != ch {
				// What the old channel offered says nothing about the new one.
				u["available"] = nil
				f.emitLocked("update.state", f.updateStateLocked())
			}
			cfg["channel"] = ch
		}
		if req.Auto != nil {
			cfg["auto"] = *req.Auto
		}
		u["config"] = cfg
		return fakeOK
	})
}

// updateAnswerLocked is GET /update: busy and progress while a stage runs.
func (f *devFake) updateAnswerLocked() map[string]any {
	u := cloneDoc(f.doc("update"))
	u["busy"], u["progress"] = f.stage != nil, nil
	if f.stage != nil {
		u["progress"] = f.stage.progress
	}
	return u
}

// updateStateLocked is the update-state part of the document, as
// update.state carries it (held and checked only when set).
func (f *devFake) updateStateLocked() map[string]any {
	u, st := f.doc("update"), map[string]any{}
	for _, k := range updateStateKeys {
		if v, ok := u[k]; ok && !((k == "held" || k == "checked") && (v == nil || v == "")) {
			st[k] = deepCopyJSON(v)
		}
	}
	return st
}

// startStageLocked starts a stage of version ("" = what is available),
// optionally from a point in its phases (a preset's download in progress).
func (f *devFake) startStageLocked(version string, from fakeStagePoint) {
	f.stage = &fakeStageRun{progress: map[string]any{"phase": "check", "percent": 0, "bytes": 0, "total": 0, "version": ""}}
	if from.Phase == "" {
		f.emitLocked("update.progress", f.stage.progress)
	}
	go f.runStage(version, from)
}

// runStage walks the real phases (update/stage.go), publishing
// update.progress as it goes: percent covers the whole update, bytes and
// total the current phase.
func (f *devFake) runStage(version string, from fakeStagePoint) {
	sleep := func(d time.Duration) bool {
		select {
		case <-f.ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	if from.Phase == "" && !sleep(600*time.Millisecond) {
		return
	}
	f.mu.Lock()
	u := f.doc("update")
	target, size := version, int64(asNum(asObj(u["available"])["size"]))
	if target == "" {
		target = asStr(asObj(u["available"])["version"])
	}
	if size == 0 {
		size = 1_420_000_000
	}
	refuse := func(p map[string]any) {
		f.stage = nil
		f.emitLocked("update.progress", p)
		f.mu.Unlock()
	}
	switch {
	case f.preset.Sim.Trial:
		refuse(map[string]any{"phase": "error", "percent": 0, "bytes": 0, "total": 0, "version": "",
			"error": fmt.Sprintf("%v (%s, 2 tries left)", update.ErrOnTrial, asStr(u["booted"]))})
		return
	case u["next_boot"] != nil && asStr(asObj(u["staged"])["version"]) == "":
		refuse(map[string]any{"phase": "error", "percent": 0, "bytes": 0, "total": 0, "version": "",
			"error": update.ErrRollbackPending.Error()})
		return
	case target == "" || target == asStr(asObj(u["staged"])["version"]) || target == asStr(u["booted"]):
		// Nothing to do ends with idle (update.go doStage, IsBenign).
		refuse(map[string]any{"phase": "idle", "percent": 0, "bytes": 0, "total": 0, "version": ""})
		return
	}
	f.mu.Unlock()

	start := stagePhaseIndex(from.Phase)
	for i, ph := range stagePhases {
		if i < start {
			continue
		}
		total := ph.bytes
		if total == 0 {
			total = size
		}
		first := 0
		if i == start {
			first = (from.Percent - ph.from) * 100 / max(ph.to-ph.from, 1)
		}
		steps := int(ph.took / (250 * time.Millisecond))
		for s := 0; s <= steps; s++ {
			own := first + (100-first)*s/max(steps, 1)
			f.mu.Lock()
			if f.stage == nil {
				f.mu.Unlock()
				return
			}
			if f.stage.cancelled && ph.name != "install" {
				p := map[string]any{"phase": "cancelled", "percent": f.stage.progress["percent"], "bytes": 0, "total": 0, "version": target}
				f.stage = nil
				f.emitLocked("update.progress", p)
				f.mu.Unlock()
				return
			}
			f.stage.progress = map[string]any{"phase": ph.name, "percent": ph.from + (ph.to-ph.from)*own/100,
				"bytes": total * int64(own) / 100, "total": total, "version": target}
			f.emitLocked("update.progress", f.stage.progress)
			f.mu.Unlock()
			if !sleep(250 * time.Millisecond) {
				return
			}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u = f.doc("update")
	other := "b"
	if u["booted_slot"] == "b" {
		other = "a"
	}
	u["staged"] = map[string]any{"version": target, "slot": other, "at": time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)}
	u["last_error"] = ""
	u["other_slot"] = map[string]any{"version": target, "running": false, "bootable": true, "counting": true,
		"tries_left": 3, "tries_done": 0, "entry": "vos-" + target + "+3.conf"}
	u["next_boot"] = map[string]any{"slot": other, "version": target}
	if held := asObj(u["held"]); len(held) > 0 && boot.CompareVersions(target, asStr(held["version"])) >= 0 {
		delete(u, "held")
	}
	f.stage = nil
	f.emitLocked("update.state", f.updateStateLocked())
	f.emitLocked("update.progress", map[string]any{"phase": "done", "percent": 100, "bytes": 0, "total": 0, "version": target})
}

// rollbackLocked makes the other slot start next (update.Rollback): a
// version that failed or used up its tries is refused.
func (f *devFake) rollbackLocked() error {
	u := f.doc("update")
	other := asObj(u["other_slot"])
	slot := "b"
	if u["booted_slot"] == "b" {
		slot = "a"
	}
	v := asStr(other["version"])
	switch {
	case v == "":
		return fmt.Errorf("slot %s has nothing bootable", slot)
	case containsString(asList(u["failed"]), v):
		return fmt.Errorf("%w: %s in slot %s", update.ErrFailedBefore, v, slot)
	case other["bootable"] != true && asNum(other["tries_done"]) > 1:
		return fmt.Errorf("slot %s (%s) used up its boot attempts without starting", slot, v)
	}
	booted := asStr(u["booted"])
	if boot.CompareVersions(v, booted) < 0 {
		u["held"] = map[string]any{"version": booted, "rollback_index": 1790684100}
	} else if held := asObj(u["held"]); len(held) > 0 && boot.CompareVersions(v, asStr(held["version"])) >= 0 {
		delete(u, "held")
	}
	other["bootable"] = true
	u["next_boot"] = map[string]any{"slot": slot, "version": v}
	f.emitLocked("update.state", f.updateStateLocked())
	return nil
}

// bootNext is what a restart starts: next_boot when set (a staged update
// or a rollback), which becomes the running version, with the old one in
// the other slot.
func bootNext(docs map[string]any) {
	u, sys := asObj(docs["update"]), asObj(docs["system"])
	next := asObj(u["next_boot"])
	version, slot := asStr(next["version"]), asStr(next["slot"])
	if version == "" {
		return
	}
	old := asStr(u["booted"])
	u["booted"], u["booted_slot"], sys["version"], sys["booted_slot"] = version, slot, version, slot
	u["staged"], u["next_boot"] = nil, nil
	// Going back marks the version it leaves bad (boot.MarkBad).
	bootable, entry := true, "vos-"+old+".conf"
	if asStr(asObj(u["held"])["version"]) == old {
		bootable, entry = false, "vos-"+old+"+0-1.conf"
	}
	u["other_slot"] = map[string]any{"version": old, "running": false, "bootable": bootable, "counting": false,
		"tries_left": 0, "tries_done": 0, "entry": entry}
	if a := asObj(u["available"]); len(a) > 0 && boot.CompareVersions(asStr(a["version"]), version) <= 0 {
		u["available"] = nil
	}
}
