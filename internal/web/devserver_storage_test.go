package web

import (
	"fmt"
	"net/http"
	"path"
	"regexp"

	"github.com/jasperaelvoet/vaporos/internal/api"
	"github.com/jasperaelvoet/vaporos/internal/storage"
)

// The dev server's fake of storage (internal/storage): drives and the
// Steam libraries VaporOS adopts. Document: base/storage.json, the GET
// /storage answer. Adopting a drive while a stream runs (Steam is busy)
// leaves its library pending, as on the box.

var fakeFSUUIDRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z-]{0,63}$`) // storage.validUUID

func (f *devFake) storageRoutes(add fakeAdder) {
	add("GET", "/storage", api.Authed, func(w http.ResponseWriter, r *http.Request) any { return f.doc("storage") })
	add("POST", "/storage/libraries", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		var req struct {
			UUID string `json:"uuid"`
		}
		if !strictBody(w, r, &req) {
			return nil
		}
		if !fakeFSUUIDRe.MatchString(req.UUID) {
			api.Error(w, http.StatusBadRequest, "invalid filesystem uuid")
			return nil
		}
		var d map[string]any
		for _, x := range asList(f.doc("storage")["disks"]) {
			if x := asObj(x); asStr(x["uuid"]) == req.UUID && asStr(x["fstype"]) != "" && x["missing"] != true {
				d = x
			}
		}
		switch {
		case d == nil:
			api.Error(w, http.StatusNotFound, "no filesystem with uuid %s is attached", req.UUID)
			return nil
		case d["is_system"] == true:
			api.Error(w, http.StatusBadRequest, "%s is part of the VaporOS system disk", asStr(d["path"]))
			return nil
		case !storage.LibraryFS(asStr(d["fstype"])):
			api.Error(w, http.StatusBadRequest, "%s filesystems cannot hold a Steam library (use ext4, btrfs, xfs, f2fs or NTFS)", asStr(d["fstype"]))
			return nil
		}
		mp := "/var/mnt/" + asStr(d["label"])
		library, hadGames := mp, d["steam_library"] == true
		if dir := asStr(d["library_dir"]); hadGames && dir != "" && dir != "." {
			library = path.Join(mp, dir)
		} else if !hadGames {
			library = path.Join(mp, "SteamLibrary") // made on adoption, owned by vapor
			d["steam_library"], d["library_dir"] = true, "SteamLibrary"
		}
		pending := f.doc("sunshine")["streaming"] == true
		d["adopted"], d["mounted_at"], d["registered"] = true, mp, !pending
		if d["free"] == nil {
			d["free"] = asNum(d["size"]) * 0.6
		}
		delete(d, "registration_pending")
		if pending {
			d["registration_pending"] = true
		}
		return map[string]any{"mountpoint": mp, "library": library, "registered": !pending,
			"registration_pending": pending, "hint": fakeAdoptHint(library, hadGames, !pending, pending)}
	})
	add("DELETE", "/storage/libraries/{uuid}", api.Authed, func(w http.ResponseWriter, r *http.Request) any {
		uuid := r.PathValue("uuid")
		if !fakeFSUUIDRe.MatchString(uuid) {
			api.Error(w, http.StatusBadRequest, "invalid filesystem uuid")
			return nil
		}
		st := f.doc("storage")
		keep, found := []any{}, false
		for _, x := range asList(st["disks"]) {
			d := asObj(x)
			if asStr(d["uuid"]) != uuid || d["adopted"] != true {
				keep = append(keep, x)
				continue
			}
			found = true
			if d["missing"] == true {
				continue // an adopted drive that is not attached is simply forgotten
			}
			d["adopted"], d["registered"] = false, false
			for _, k := range []string{"mounted_at", "free", "registration_pending"} {
				delete(d, k)
			}
			keep = append(keep, d)
		}
		if !found {
			api.Error(w, http.StatusNotFound, "no adopted library with uuid %s", uuid)
			return nil
		}
		st["disks"] = keep
		return fakeOK
	})
}

// fakeAdoptHint is storage.adoptHint's text.
func fakeAdoptHint(library string, hadGames, registered, pending bool) string {
	games := ""
	if hadGames {
		games = " Its installed games appear in Steam without downloading."
	}
	switch {
	case registered:
		return fmt.Sprintf("%s is one of Steam's game libraries.%s", library, games)
	case pending:
		return fmt.Sprintf("VaporOS adds %s to Steam's game libraries the next time Steam is not running (at the latest after a restart).%s "+
			"To use it right away, open Steam > Settings > Storage > Add Drive and choose %s.", library, games, library)
	}
	return fmt.Sprintf("In Steam, open Settings > Storage > Add Drive and choose %s.%s", library, games)
}
