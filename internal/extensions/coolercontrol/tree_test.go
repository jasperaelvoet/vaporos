package coolercontrol

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jasperaelvoet/vaporos/internal/extensions"
	"github.com/jasperaelvoet/vaporos/internal/extensions/buildcheck"
	"github.com/jasperaelvoet/vaporos/internal/extensions/descriptor"
)

// source is the extension in the repository.
const source = "../../../extensions/coolercontrol"

func loadSource(t *testing.T) *descriptor.Descriptor {
	t.Helper()
	d, err := descriptor.Load(filepath.Join(source, "extension.json"))
	must(t, err)
	must(t, d.ValidateSource())
	return d
}

func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, filepath.FromSlash(name))
		must(t, os.MkdirAll(filepath.Dir(p), 0o755))
		must(t, os.WriteFile(p, []byte(content), 0o644))
	}
}

// upstreamUnit is coolercontrold.service as the package ships it.
const upstreamUnit = `[Unit]
Description=CoolerControl Daemon
After=network.target

[Service]
Type=notify
ExecStart=/usr/bin/coolercontrold
Restart=always
RestartSec=1
TimeoutStopSec=10
TimeoutStartSec=300
WatchdogSec=30

[Install]
WantedBy=multi-user.target
`

// The image the build would make from the source (its files/, its own
// directory, stand-ins for the packages with the udev rule stripped)
// passes vos ext check-tree against a base, with exactly the permissions
// the descriptor declares, as a root service.
func TestImageTreePassesCheckTree(t *testing.T) {
	d := loadSource(t)
	base, tree := t.TempDir(), t.TempDir()
	writeTree(t, base, map[string]string{
		"usr/bin/vos": "vos",
		"usr/lib/systemd/system/multi-user.target": "[Unit]\n",
		"usr/lib/systemd/system/network.target":    "[Unit]\n",
		"usr/lib/systemd/system/vosd.service":      "[Service]\nExecStart=/usr/bin/vos daemon\n",
		"usr/lib/modules-load.d/vos.conf":          "uinput\n",
		"usr/lib/vos/cmdline":                      "quiet\n",
		"usr/share/vos/extensions/proton.json":     "{}",
	})
	files := map[string]string{
		"usr/bin/coolercontrold":                              "#!/bin/sh\n",
		"usr/lib/systemd/system/coolercontrold.service":       upstreamUnit,
		"usr/bin/liquidctl":                                   "#!/usr/bin/python3\n",
		"usr/lib/python3/site-packages/liquidctl/__init__.py": "",
		"usr/share/licenses/liquidctl/LICENSE":                "GPL",
		"usr/lib/vos/ext/coolercontrol/packages.txt":          "coolercontrold 5.0.1-1\nliquidctl 1.15.0-1\n",
		"usr/lib/vos/ext/coolercontrol/module-options":        "amdgpu ppfeaturemask\nit87 ignore_resource_conflict\n",
	}
	b, err := os.ReadFile(filepath.Join(source, "extension.json"))
	must(t, err)
	files["usr/lib/vos/ext/coolercontrol/extension.json"] = string(b)
	must(t, filepath.WalkDir(filepath.Join(source, "files"), func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(filepath.Join(source, "files"), p)
		b, err := os.ReadFile(p)
		files[filepath.ToSlash(rel)] = string(b)
		return err
	}))
	writeTree(t, tree, files)

	r := buildcheck.CheckTree(buildcheck.Options{ID: id, Descriptor: d, Tree: tree, Base: base})
	if len(r.Problems) > 0 {
		t.Fatalf("check-tree:\n%s", strings.Join(r.Problems, "\n"))
	}
	if !slices.Equal(r.Permissions, d.Permissions) || !r.RunsAsRoot {
		t.Fatalf("permissions %v (declared %v), runs as root %v", r.Permissions, d.Permissions, r.RunsAsRoot)
	}

	// The rule the descriptor strips would be a warning, and a udev
	// permission it does not declare.
	writeTree(t, tree, map[string]string{"usr/lib/udev/rules.d/71-liquidctl.rules": `TAG+="uaccess"` + "\n"})
	if r := buildcheck.CheckTree(buildcheck.Options{ID: id, Descriptor: d, Tree: tree, Base: base}); len(r.Problems) == 0 {
		t.Fatal("check-tree passed the liquidctl udev rule")
	}
}

// The drop-in and the code agree: where prepare looks, where vosd
// proxies to, the order of the start steps. A start that fails, slowly or
// not, stops being tried, and detection that loads nothing does not stop
// the daemon.
func TestDropInMatches(t *testing.T) {
	d := loadSource(t)
	f, err := os.Open(filepath.Join(source, "files/usr/lib/systemd/system/coolercontrold.service.d/vos.conf"))
	must(t, err)
	defer f.Close()
	env := map[string]string{}
	var pre, post []string
	settings, sections := map[string]string{}, map[string]string{}
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok && !strings.HasPrefix(k, "#") {
			sections[k] = section
		}
		switch {
		case strings.HasPrefix(k, "[") && !ok:
			section = strings.Trim(k, "[]")
		case !ok || strings.HasPrefix(k, "#"):
		case k == "Environment":
			ek, ev, _ := strings.Cut(v, "=")
			env[ek] = ev
		case k == "ExecStartPre":
			pre = append(pre, v)
		case k == "ExecStopPost":
			post = append(post, v)
		default:
			settings[k] = v
		}
	}
	must(t, sc.Err())

	area := "/var/lib/vos/ext/data/" + id
	want := map[string]string{
		"CC_CONFIG_DIR": area + "/config", "CC_DATA_DIR": area + "/data",
		"CC_PLUGINS_DIR": "/usr/lib/vos/ext/coolercontrol/plugins",
		"CC_HOST_IP4":    "127.0.0.1", "CC_HOST_IP6": "::1", "CC_PORT": "11985",
		"CC_TLS": "OFF", "CC_SERVICE_MANAGER": "OFF",
	}
	for k, v := range want {
		if env[k] != v {
			t.Errorf("Environment %s=%q, want %q", k, env[k], v)
		}
	}
	if up := d.Network.Ports[0].Upstream; up != env["CC_HOST_IP4"]+":"+env["CC_PORT"] || d.Web.Port != d.Network.Ports[0].Port {
		t.Errorf("descriptor upstream %s, web %d; the daemon listens on %s:%s", up, d.Web.Port, env["CC_HOST_IP4"], env["CC_PORT"])
	}
	for _, s := range forcedSettings {
		if s.key == "port" && s.value != env["CC_PORT"] {
			t.Errorf("config.toml port %s, CC_PORT %s", s.value, env["CC_PORT"])
		}
	}
	if up := upstream(&extensions.Ext{ID: id}); up != d.Network.Ports[0].Upstream {
		t.Errorf("the status check's own upstream %s, the descriptor's %s", up, d.Network.Ports[0].Upstream)
	}
	// coolercontrold's gRPC server takes CC_PORT+1 on the same addresses:
	// on 11987 it took vosd's web UI port.
	port, err := strconv.Atoi(env["CC_PORT"])
	must(t, err)
	declared := []int{11987, d.Web.Port}
	for _, p := range d.Network.Ports {
		declared = append(declared, p.Port)
	}
	if slices.Contains(declared, port+1) || slices.Contains(declared, port) {
		t.Errorf("CC_PORT=%d: it or its gRPC port %d is one of %v, where vosd listens", port, port+1, declared)
	}
	if settings["StateDirectory"] != "vos/ext/data/"+id || settings["ReadWritePaths"] != area || settings["ProtectSystem"] != "strict" {
		t.Errorf("sandbox %v", settings)
	}
	if strings.Contains(settings["CapabilityBoundingSet"], "CAP_SYS_ADMIN") {
		t.Error("CAP_SYS_ADMIN in the bounding set")
	}
	wantPre := []string{"/usr/bin/vos ext coolercontrol prepare", "-/usr/bin/coolercontrold detect --load",
		"+/usr/bin/vos ext coolercontrol fans snapshot"}
	if !slices.Equal(pre, wantPre) || !slices.Equal(post, []string{"+/usr/bin/vos ext coolercontrol fans restore"}) {
		t.Errorf("ExecStartPre %q, ExecStopPost %q", pre, post)
	}

	if sections["StartLimitIntervalSec"] != "Unit" || sections["StartLimitBurst"] != "Unit" || sections["TimeoutStartSec"] != "Service" {
		t.Fatalf("sections %v", sections)
	}
	interval, err := time.ParseDuration(strings.TrimSuffix(settings["StartLimitIntervalSec"], "in"))
	must(t, err)
	timeout, err := time.ParseDuration(settings["TimeoutStartSec"])
	must(t, err)
	burst, err := strconv.Atoi(settings["StartLimitBurst"])
	must(t, err)
	if settings["StartLimitIntervalSec"] != "30min" || burst != 5 || interval < time.Duration(burst)*timeout {
		t.Errorf("StartLimitIntervalSec=%s StartLimitBurst=%d: %d starts of %v each never fill it", settings["StartLimitIntervalSec"], burst, burst, timeout)
	}
}
