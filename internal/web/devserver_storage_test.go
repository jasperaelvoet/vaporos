package web

import (
	"fmt"
	"net/http"

	"github.com/jasperaelvoet/vaporos/internal/api"
)

// The dev server's fake of storage: drives and the Steam libraries VaporOS
// adopts. TestDevServer and the fake's core are in devserver_test.go.

func (f *fakeAPI) seedStorage() {
	f.libraries = map[string]string{"5e1d-aa01": "registered", "3c4d-5e6f": ""}
}

func (f *fakeAPI) storageRoutes() {
	ok := struct{}{}
	f.handle("GET /storage", true, func(w http.ResponseWriter, r *http.Request) any {
		disks := []map[string]any{
			{"path": "/dev/nvme0n1p4", "model": "Samsung SSD 990 PRO 1TB", "size": 960_000_000_000, "uuid": "sys-0001", "label": "vos_data", "fstype": "ext4", "mounted_at": "/state", "is_system": true, "free": 612_000_000_000},
			{"path": "/dev/sda1", "model": "Samsung SSD 870 EVO 1TB", "size": 1_000_000_000_000, "uuid": "5e1d-aa01", "label": "SATA1TB", "fstype": "ext4", "steam_library": true, "library_dir": "."},
			{"path": "/dev/sdb1", "model": "WDC WD20EZAZ", "size": 2_000_000_000_000, "uuid": "77b2-c9d0", "label": "Games2", "fstype": "btrfs", "steam_library": true, "library_dir": "SteamLibrary"},
			{"path": "/dev/sdc1", "model": "Crucial X9", "size": 500_000_000_000, "uuid": "E0A1-33F2", "label": "WINDATA", "fstype": "ntfs", "steam_library": false},
			{"path": "/dev/sdd1", "model": "SanDisk Extreme", "size": 256_000_000_000, "uuid": "6A1B-2C3D", "label": "CAMERA", "fstype": "exfat", "steam_library": false},
		}
		attached := map[string]bool{}
		for _, d := range disks {
			uuid := d["uuid"].(string)
			attached[uuid] = true
			st, adopted := f.libraries[uuid]
			d["registered"] = adopted && st == "registered"
			if !adopted {
				continue
			}
			d["adopted"], d["mounted_at"], d["free"] = true, "/var/mnt/"+d["label"].(string), 420_000_000_000
			d["registration_pending"] = st == "pending"
			if d["steam_library"] != true {
				d["steam_library"], d["library_dir"] = true, "SteamLibrary" // made on adoption
			}
		}
		for uuid, st := range f.libraries {
			if !attached[uuid] {
				disks = append(disks, map[string]any{"uuid": uuid, "label": "OldSSD", "fstype": "ext4", "is_system": false, "steam_library": false,
					"adopted": true, "missing": true, "registered": st == "registered", "registration_pending": st == "pending"})
			}
		}
		return map[string]any{"disks": disks}
	})
	f.handle("POST /storage/libraries", true, func(w http.ResponseWriter, r *http.Request) any {
		uuid := fmt.Sprint(body(r)["uuid"])
		label, ok := map[string]string{"5e1d-aa01": "SATA1TB", "77b2-c9d0": "Games2", "E0A1-33F2": "WINDATA"}[uuid]
		if !ok {
			api.Error(w, http.StatusBadRequest, "exfat filesystems cannot hold a Steam library (use ext4, btrfs, xfs, f2fs or NTFS)")
			return nil
		}
		// Games2 waits for Steam to stop, like a disk adopted mid-stream.
		st, hadGames := "registered", uuid != "E0A1-33F2"
		if uuid == "77b2-c9d0" {
			st = "pending"
		}
		f.libraries[uuid] = st
		mp, lib := "/var/mnt/"+label, "/var/mnt/"+label
		if uuid != "5e1d-aa01" {
			lib += "/SteamLibrary"
		}
		games := ""
		if hadGames {
			games = " Its installed games appear in Steam without downloading."
		}
		hint := lib + " is one of Steam's game libraries." + games
		if st == "pending" {
			hint = "VaporOS adds " + lib + " to Steam's game libraries the next time Steam is not running (at the latest after a restart)." + games +
				" To use it right away, open Steam > Settings > Storage > Add Drive and choose " + lib + "."
		}
		return map[string]any{"mountpoint": mp, "library": lib, "registered": st == "registered", "registration_pending": st == "pending", "hint": hint}
	})
	f.handle("DELETE /storage/libraries/{uuid}", true, func(w http.ResponseWriter, r *http.Request) any {
		delete(f.libraries, r.PathValue("uuid"))
		return ok
	})
}
