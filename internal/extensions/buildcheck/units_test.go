package buildcheck

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestUnitRules(t *testing.T) {
	base := newBase(t)
	const unit = "usr/lib/systemd/system/demo.service"
	const dropIn = "usr/lib/systemd/system/demo.service.d/vos.conf"
	for name, c := range map[string]struct {
		files  map[string]string
		remove []string
		want   string // a problem, or "" for none
	}{
		"no drop-in":            {remove: []string{dropIn}, want: "demo.service: needs a drop-in (demo.service.d/*.conf)"},
		"drop-in without limit": {files: map[string]string{dropIn: "[Service]\nNice=5\n"}, want: "needs a drop-in"},
		"only the unit limits":  {files: map[string]string{unit: "[Service]\nExecStart=/usr/bin/demo\nTimeoutStartSec=10\n", dropIn: "[Service]\nNice=5\n"}, want: "needs a drop-in"},
		"infinity":              {files: map[string]string{dropIn: "[Service]\nTimeoutStartSec=infinity\n"}, want: "does not bound the start"},
		"zero":                  {files: map[string]string{dropIn: "[Service]\nTimeoutStartSec=0\n"}, want: "does not bound the start"},
		"reset":                 {files: map[string]string{dropIn: "[Service]\nTimeoutStartSec=30\nTimeoutStartSec=\n"}, want: "is not a time span"},
		"garbage":               {files: map[string]string{dropIn: "[Service]\nTimeoutStartSec=soon\n"}, want: "is not a time span"},
		"later drop-in unbounds": {files: map[string]string{
			"usr/lib/systemd/system/demo.service.d/zz.conf": "[Service]\nTimeoutSec=infinity\n",
		}, want: "does not bound the start"},
		"TimeoutSec":           {files: map[string]string{dropIn: "[Service]\nTimeoutSec=1min 30s\n"}},
		"Before a base target": {files: map[string]string{unit: "[Unit]\nBefore=multi-user.target\n[Service]\nUser=demo\n"}, want: unit + ": Before=multi-user.target"},
		"Before in a drop-in":  {files: map[string]string{dropIn: "[Unit]\nBefore=vosd.service\n[Service]\nTimeoutStartSec=30\n"}, want: dropIn + ": Before=vosd.service"},
		"Before a base instance": {files: map[string]string{
			unit: "[Unit]\nBefore=demo-helper.service getty@tty1.service\n[Service]\nUser=demo\n",
		}, want: "Before=getty@tty1.service"},
		"Before an /etc unit": {files: map[string]string{unit: "[Unit]\nBefore=local.service\n[Service]\nUser=demo\n"}, want: "Before=local.service"},
		"Before its own unit": {files: map[string]string{
			unit: "[Unit]\nBefore=demo-helper.service\n[Service]\nUser=demo\n",
			"usr/lib/systemd/system/demo-helper.service":            "[Service]\nUser=demo\n",
			"usr/lib/systemd/system/demo-helper.service.d/vos.conf": "[Service]\nTimeoutStartSec=5\n",
		}},
		"drop-in for a base unit": {files: map[string]string{
			"usr/lib/systemd/system/vosd.service.d/x.conf": "[Service]\nTimeoutStartSec=5\n",
		}, want: "usr/lib/systemd/system/vosd.service.d: drop-in for vosd.service, which the extension does not ship"},
		"drop-in for every service": {files: map[string]string{
			"usr/lib/systemd/system/service.d/x.conf": "[Service]\nTimeoutStartSec=5\n",
		}, want: "usr/lib/systemd/system/service.d: not a unit's drop-in"},
		"wants for a base unit": {files: map[string]string{
			"usr/lib/systemd/system/multi-user.target.wants/demo.service": "@../demo.service",
		}, want: "adds dependencies to multi-user.target"},
		"wants of its own unit": {files: map[string]string{
			"usr/lib/systemd/system/demo.service.wants/vosd.service": "@/usr/lib/systemd/system/vosd.service",
		}},
		"alias": {files: map[string]string{"usr/lib/systemd/system/demo-alias.service": "@demo.service"}},
		"mask":  {files: map[string]string{"usr/lib/systemd/system/demo-mask.service": "@/dev/null"}, want: "masks a unit"},
		"alias elsewhere": {files: map[string]string{
			"usr/lib/systemd/system/demo-alias.service": "@/usr/lib/demo/demo.service",
		}, want: "an alias must point at one of the extension's own units"},
		"template drop-in covers instance": {files: map[string]string{
			"usr/lib/systemd/system/demo@.service":            "[Service]\nUser=demo\n",
			"usr/lib/systemd/system/demo@.service.d/vos.conf": "[Service]\nTimeoutStartSec=5\n",
			"usr/lib/systemd/system/demo@x.service.d/x.conf":  "[Service]\nNice=1\n",
		}},
		"user drop-in for a base unit": {files: map[string]string{
			"usr/lib/systemd/user/vos-gamescope.service.d/x.conf": "[Service]\nEnvironment=A=1\n",
		}, want: "drop-in for vos-gamescope.service"},
		"listed service missing": {remove: []string{unit, dropIn}, want: "services[0]: demo.service is not a unit in usr/lib/systemd/system"},
		"instance drop-in orders before the base": {files: map[string]string{
			"usr/lib/systemd/system/demo@.service":            "[Service]\nUser=demo\n",
			"usr/lib/systemd/system/demo@.service.d/vos.conf": "[Service]\nTimeoutStartSec=5\n",
			"usr/lib/systemd/system/demo@x.service.d/x.conf":  "[Unit]\nBefore=multi-user.target\n",
		}, want: "usr/lib/systemd/system/demo@x.service.d/x.conf: Before=multi-user.target"},
		"instance drop-in unbounds": {files: map[string]string{
			"usr/lib/systemd/system/demo@.service":            "[Service]\nUser=demo\n",
			"usr/lib/systemd/system/demo@.service.d/vos.conf": "[Service]\nTimeoutStartSec=5\n",
			"usr/lib/systemd/system/demo@x.service.d/zz.conf": "[Service]\nTimeoutStartSec=infinity\n",
		}, want: "usr/lib/systemd/system/demo@x.service: TimeoutStartSec=infinity"},
		"instance drop-in hides the template's": {files: map[string]string{
			"usr/lib/systemd/system/demo@.service":             "[Service]\nUser=demo\n",
			"usr/lib/systemd/system/demo@.service.d/vos.conf":  "[Service]\nTimeoutStartSec=5\n",
			"usr/lib/systemd/system/demo@x.service.d/vos.conf": "[Service]\nNice=1\n",
		}, want: "usr/lib/systemd/system/demo@x.service: needs a drop-in"},
		"alias drop-in orders before the base": {files: map[string]string{
			"usr/lib/systemd/system/demo-alias.service":          "@demo.service",
			"usr/lib/systemd/system/demo-alias.service.d/x.conf": "[Unit]\nBefore=vosd.service\n",
		}, want: "usr/lib/systemd/system/demo-alias.service.d/x.conf: Before=vosd.service"},
		"alias drop-in unbounds": {files: map[string]string{
			"usr/lib/systemd/system/demo-alias.service":           "@demo.service",
			"usr/lib/systemd/system/demo-alias.service.d/zz.conf": "[Service]\nTimeoutSec=infinity\n",
		}, want: "usr/lib/systemd/system/demo.service: TimeoutSec=infinity"},
		"alias drop-in of the same name": {files: map[string]string{
			"usr/lib/systemd/system/demo-alias.service":            "@demo.service",
			"usr/lib/systemd/system/demo-alias.service.d/vos.conf": "[Service]\nTimeoutStartSec=5\n",
		}, want: "in an order systemd leaves open"},
		"aliased template's instance drop-in": {files: map[string]string{
			"usr/lib/systemd/system/demo@.service":                 "[Service]\nUser=demo\n",
			"usr/lib/systemd/system/demo@.service.d/vos.conf":      "[Service]\nTimeoutStartSec=5\n",
			"usr/lib/systemd/system/demo-alias@.service":           "@demo@.service",
			"usr/lib/systemd/system/demo-alias@x.service.d/x.conf": "[Unit]\nBefore=multi-user.target\n",
		}, want: "demo-alias@x.service.d/x.conf: Before=multi-user.target"},
		"symlinked drop-in": {files: map[string]string{
			"usr/lib/systemd/system/demo.service.d/zz.conf": "@/etc/passwd",
		}, want: "usr/lib/systemd/system/demo.service.d/zz.conf: a drop-in must be a file, not a symlink"},
		"a unit the base has in /etc": {files: map[string]string{
			"usr/lib/systemd/system/local.service":          "[Service]\nUser=demo\n",
			"usr/lib/systemd/system/local.service.d/x.conf": "[Service]\nTimeoutStartSec=5\n",
		}, want: "usr/lib/systemd/system/local.service: the base has local.service or its template"},
		"an instance of a base template": {files: map[string]string{
			"usr/lib/systemd/system/getty@ttyS0.service":          "[Service]\nUser=demo\n",
			"usr/lib/systemd/system/getty@ttyS0.service.d/x.conf": "[Service]\nTimeoutStartSec=5\n",
		}, want: "usr/lib/systemd/system/getty@ttyS0.service: the base has getty@ttyS0.service or its template"},
		"an alias the base has": {files: map[string]string{
			"usr/lib/systemd/system/local.service": "@demo.service",
		}, want: "usr/lib/systemd/system/local.service: the base has local.service"},
		"socket with commands": {files: map[string]string{
			"usr/lib/systemd/system/demo.socket": "[Socket]\nListenStream=1234\nExecStartPre=/usr/bin/demo\nUser=demo\n",
		}, want: "usr/lib/systemd/system/demo.socket: needs a drop-in (demo.socket.d/*.conf) that sets a finite TimeoutSec="},
		"socket without commands": {files: map[string]string{
			"usr/lib/systemd/system/demo.socket": "[Socket]\nListenStream=1234\n",
		}},
		"socket bounded": {files: map[string]string{
			"usr/lib/systemd/system/demo.socket":            "[Socket]\nListenStream=1234\nExecStartPre=/usr/bin/demo\nUser=demo\n",
			"usr/lib/systemd/system/demo.socket.d/vos.conf": "[Socket]\nTimeoutSec=10\n",
		}},
		"a mount": {files: map[string]string{
			"usr/lib/systemd/system/srv-demo.mount":            "[Mount]\nWhat=tmpfs\nWhere=/srv/demo\n",
			"usr/lib/systemd/system/srv-demo.mount.d/vos.conf": "[Mount]\nTimeoutSec=10\n",
		}, want: "usr/lib/systemd/system/srv-demo.mount: systemd and generators name .mount units at runtime"},
		"a drop-in for the ESP's mount": {files: map[string]string{
			"usr/lib/systemd/system/efi.mount.d/x.conf": "[Mount]\nOptions=rw\n",
		}, want: "usr/lib/systemd/system/efi.mount.d: systemd and generators name .mount units"},
		"wants of the journal's mount": {files: map[string]string{
			"usr/lib/systemd/system/var-log-journal.mount.wants/demo.service": "@../demo.service",
		}, want: "usr/lib/systemd/system/var-log-journal.mount.wants: systemd and generators name .mount units"},
		"the store's mount": {files: map[string]string{
			"usr/lib/systemd/system/var-lib-vos.mount": "@demo.service",
		}, want: "usr/lib/systemd/system/var-lib-vos.mount: systemd and generators name .mount units"},
		"a user's slice": {files: map[string]string{
			"usr/lib/systemd/system/user-1000.slice": "[Slice]\nCPUWeight=10000\n",
		}, want: "usr/lib/systemd/system/user-1000.slice: systemd and generators name .slice units"},
		"requires of a slice": {files: map[string]string{
			"usr/lib/systemd/system/system.slice.requires/demo.service": "@../demo.service",
		}, want: "usr/lib/systemd/system/system.slice.requires: systemd and generators name .slice units"},
		"a user slice drop-in": {files: map[string]string{
			"usr/lib/systemd/user/app.slice.d/x.conf": "[Slice]\nCPUWeight=1\n",
		}, want: "usr/lib/systemd/user/app.slice.d: systemd and generators name .slice units"},
		"a swap":       {files: map[string]string{"usr/lib/systemd/system/demo.swap": "[Swap]\nWhat=/swap\n"}, want: "name .swap units"},
		"an automount": {files: map[string]string{"usr/lib/systemd/system/srv-demo.automount": "[Automount]\nWhere=/srv/demo\n"}, want: "name .automount units"},
		"a scope":      {files: map[string]string{"usr/lib/systemd/system/demo.scope": "[Scope]\n"}, want: "name .scope units"},
		"a device":     {files: map[string]string{"usr/lib/systemd/system/dev-sda.device.wants/demo.service": "@../demo.service"}, want: "name .device units"},

		"Conflicts with a base unit":     {files: map[string]string{unit: "[Unit]\nConflicts=vosd.service\n[Service]\nUser=demo\n"}, want: unit + ": Conflicts=vosd.service stops, whenever it starts, a unit of the base"},
		"Conflicts with shutdown":        {files: map[string]string{unit: "[Unit]\nConflicts=shutdown.target\n[Service]\nUser=demo\n"}},
		"Conflicts with a mount":         {files: map[string]string{unit: "[Unit]\nConflicts=efi.mount\n[Service]\nUser=demo\n"}, want: "Conflicts=efi.mount stops, whenever it starts, a .mount unit, which systemd and generators name at runtime"},
		"OnFailure in a drop-in":         {files: map[string]string{dropIn: "[Unit]\nOnFailure=multi-user.target\n[Service]\nTimeoutStartSec=30\n"}, want: dropIn + ": OnFailure=multi-user.target starts, when it fails, a unit of the base"},
		"OnSuccess a base instance":      {files: map[string]string{unit: "[Unit]\nOnSuccess=getty@tty1.service\n[Service]\nUser=demo\n"}, want: "OnSuccess=getty@tty1.service starts, when it succeeds, a unit of the base"},
		"OnFailure its own template":     {files: map[string]string{unit: "[Unit]\nOnFailure=demo-notify@%n.service\n[Service]\nUser=demo\n"}},
		"PropagatesStopTo":               {files: map[string]string{unit: "[Unit]\nPropagatesStopTo=local.service\n[Service]\nUser=demo\n"}, want: "PropagatesStopTo=local.service stops, whenever it stops, a unit of the base"},
		"StopPropagatedFrom a base unit": {files: map[string]string{unit: "[Unit]\nStopPropagatedFrom=vosd.service\n[Service]\nUser=demo\n"}},
		"PropagatesReloadTo":             {files: map[string]string{unit: "[Unit]\nPropagatesReloadTo=vosd.service\n[Service]\nUser=demo\n"}, want: "PropagatesReloadTo=vosd.service reloads"},
		"PartOf a base unit":             {files: map[string]string{unit: "[Unit]\nPartOf=multi-user.target\n[Service]\nUser=demo\n"}},
		"PartOf a slice":                 {files: map[string]string{unit: "[Unit]\nPartOf=user-1000.slice\n[Service]\nUser=demo\n"}},
		"PartOf in a drop-in":            {files: map[string]string{dropIn: "[Unit]\nPartOf=vosd.service\nStopPropagatedFrom=local.service\n[Service]\nTimeoutStartSec=30\n"}},
		"Upholds":                        {files: map[string]string{unit: "[Unit]\nUpholds=demo-helper.service vosd.service\n[Service]\nUser=demo\n"}, want: "Upholds=vosd.service keeps restarting a unit of the base"},
		"BindsTo a base unit":            {files: map[string]string{unit: "[Unit]\nBindsTo=vosd.service\n[Service]\nUser=demo\n"}},
		"BindsTo a device":               {files: map[string]string{unit: "[Unit]\nBindsTo=dev-sda.device\n[Service]\nUser=demo\n"}},
		"BindTo a base instance":         {files: map[string]string{unit: "[Unit]\nBindTo=getty@tty1.service\n[Service]\nUser=demo\n"}},
		"JoinsNamespaceOf":               {files: map[string]string{unit: "[Unit]\nJoinsNamespaceOf=vosd.service\n[Service]\nUser=demo\n"}, want: "JoinsNamespaceOf=vosd.service joins the namespaces of a unit of the base"},
		"Before a mount":                 {files: map[string]string{unit: "[Unit]\nBefore=var-lib-vos.mount\n[Service]\nUser=demo\n"}, want: "Before=var-lib-vos.mount orders it before a .mount unit"},
		"a specifier":                    {files: map[string]string{unit: "[Unit]\nConflicts=%N-x.service\n[Service]\nUser=demo\n"}, want: "Conflicts=%N-x.service stops, whenever it starts, a unit named with specifiers"},
		"a base template's instance":     {files: map[string]string{unit: "[Unit]\nConflicts=getty@%i.service\n[Service]\nUser=demo\n"}, want: "Conflicts=getty@%i.service stops, whenever it starts, a unit of the base"},
		"wants of its own unit for the base": {files: map[string]string{
			"usr/lib/systemd/system/demo.service.requires/vosd.service": "@/usr/lib/systemd/system/vosd.service",
		}},
		"upholds of its own unit for the base": {files: map[string]string{
			"usr/lib/systemd/system/demo.service.upholds/vosd.service": "@/usr/lib/systemd/system/vosd.service",
		}, want: "usr/lib/systemd/system/demo.service.upholds/vosd.service: keeps restarting a unit of the base"},
		"a timer for a base service": {files: map[string]string{
			"usr/lib/systemd/system/vosd.timer": "[Timer]\nOnCalendar=daily\n",
		}, want: "usr/lib/systemd/system/vosd.timer: starts vosd.service, a unit of the base"},
		"a timer's Unit=": {files: map[string]string{
			"usr/lib/systemd/system/demo.timer": "[Timer]\nOnCalendar=daily\nUnit=multi-user.target\n",
		}, want: "usr/lib/systemd/system/demo.timer: starts multi-user.target, a unit of the base"},
		"a timer's own Unit=": {files: map[string]string{
			"usr/lib/systemd/system/demo.timer": "[Timer]\nOnCalendar=daily\nUnit=demo.service\n",
		}},
		// systemd keeps a timer's first Unit=: the main file's, not the
		// drop-in's that a check reading the last would see.
		"a timer's first Unit= is the base's": {files: map[string]string{
			"usr/lib/systemd/system/demo.timer":          "[Timer]\nOnCalendar=daily\nUnit=vosd.service\n",
			"usr/lib/systemd/system/demo.timer.d/x.conf": "[Timer]\nUnit=demo.service\n",
		}, want: "usr/lib/systemd/system/demo.timer: starts vosd.service, a unit of the base"},
		"a timer's drop-in names the base": {files: map[string]string{
			"usr/lib/systemd/system/demo.timer":          "[Timer]\nOnCalendar=daily\nUnit=demo.service\n",
			"usr/lib/systemd/system/demo.timer.d/x.conf": "[Timer]\nUnit=\nUnit=vosd.service\n",
		}, want: "usr/lib/systemd/system/demo.timer: starts vosd.service, a unit of the base"},
		"an empty Unit= keeps the default": {files: map[string]string{
			"usr/lib/systemd/system/vosd.path": "[Path]\nPathExists=/run/demo/go\nUnit=\n",
		}, want: "usr/lib/systemd/system/vosd.path: starts vosd.service, a unit of the base"},
		"a path's first Unit= is the base's": {files: map[string]string{
			"usr/lib/systemd/system/demo.path":          "[Path]\nPathExists=/run/demo/go\nUnit=multi-user.target\n",
			"usr/lib/systemd/system/demo.path.d/x.conf": "[Path]\nUnit=demo.service\n",
		}, want: "usr/lib/systemd/system/demo.path: starts multi-user.target, a unit of the base"},
		"a path for its own service": {files: map[string]string{
			"usr/lib/systemd/system/demo.path": "[Path]\nPathExists=/run/demo/go\n",
		}},
		"an accepting socket for a base template": {files: map[string]string{
			"usr/lib/systemd/system/getty.socket": "[Socket]\nListenStream=1234\nAccept=yes\n",
		}, want: "usr/lib/systemd/system/getty.socket: starts getty@.service, a unit of the base"},
		// systemd ignores a value it cannot use and falls back to the
		// service of the unit's own name.
		"an invalid Unit= falls back to a base service": {files: map[string]string{
			"usr/lib/systemd/system/vosd.timer": "[Timer]\nOnBootSec=1min\nUnit=junk\n",
		}, want: "usr/lib/systemd/system/vosd.timer: starts vosd.service, a unit of the base"},
		"a Unit= naming the timer itself": {files: map[string]string{
			"usr/lib/systemd/system/vosd.timer": "[Timer]\nOnBootSec=1min\nUnit=vosd.timer\n",
		}, want: "usr/lib/systemd/system/vosd.timer: starts vosd.service, a unit of the base"},
		"a socket's Service= that is no service": {files: map[string]string{
			"usr/lib/systemd/system/vosd.socket": "[Socket]\nListenStream=1234\nService=demo.target\n",
		}, want: "usr/lib/systemd/system/vosd.socket: starts vosd.service, a unit of the base"},
		"a socket's Service=": {files: map[string]string{
			"usr/lib/systemd/system/demo.socket": "[Socket]\nListenStream=1234\nService=vosd.service\n",
		}, want: "usr/lib/systemd/system/demo.socket: starts vosd.service, a unit of the base"},
	} {
		t.Run(name, func(t *testing.T) {
			tree, d := newDemo(t)
			for _, p := range c.remove {
				if err := os.Remove(filepath.Join(tree, filepath.FromSlash(p))); err != nil {
					t.Fatal(err)
				}
			}
			writeTree(t, tree, c.files)
			if _, ok := c.files["usr/lib/systemd/user/vos-gamescope.service.d/x.conf"]; ok {
				d = declare(t, tree, "service", "user-service")
			}
			r := run(t, tree, base, d)
			if c.want == "" {
				wantClean(t, r)
			} else {
				wantProblem(t, r, c.want)
			}
		})
	}
}

func TestRunsAsRoot(t *testing.T) {
	base := newBase(t)
	const unit = "usr/lib/systemd/system/demo.service"
	for name, c := range map[string]struct {
		files map[string]string
		root  bool
	}{
		"User":            {map[string]string{}, false},
		"no User":         {map[string]string{unit: "[Service]\nExecStart=/usr/bin/demo\n"}, true},
		"User=root":       {map[string]string{unit: "[Service]\nUser=root\n"}, true},
		"DynamicUser":     {map[string]string{unit: "[Service]\nDynamicUser=yes\n"}, false},
		"drop-in resets":  {map[string]string{"usr/lib/systemd/system/demo.service.d/zz.conf": "[Service]\nUser=\n"}, true},
		"drop-in sets":    {map[string]string{unit: "[Service]\n", "usr/lib/systemd/system/demo.service.d/zz.conf": "[Service]\nUser=demo\n"}, false},
		"User in [Unit]":  {map[string]string{unit: "[Unit]\nUser=demo\n[Service]\n"}, true},
		"a root template": {map[string]string{"usr/lib/systemd/system/demo@.service": "[Service]\n", "usr/lib/systemd/system/demo@.service.d/vos.conf": "[Service]\nTimeoutStartSec=5\n"}, true},
		"+":               {map[string]string{unit: "[Service]\nUser=demo\nExecStart=+/usr/bin/demo\n"}, true},
		"!":               {map[string]string{unit: "[Service]\nUser=demo\nExecStart=/usr/bin/demo\nExecStartPre=-!/usr/bin/demo prep\n"}, true},
		"!!":              {map[string]string{unit: "[Service]\nUser=demo\nExecStart=/usr/bin/demo\nExecStopPost=!!/usr/bin/demo stop\n"}, true},
		"@ and -":         {map[string]string{unit: "[Service]\nUser=demo\nExecStart=-@/usr/bin/demo demo\n"}, false},
		"reset +": {map[string]string{
			unit: "[Service]\nUser=demo\nExecStart=/usr/bin/demo\nExecStartPre=+/usr/bin/demo prep\n",
			"usr/lib/systemd/system/demo.service.d/zz.conf": "[Service]\nExecStartPre=\nExecStartPre=/usr/bin/demo prep\n",
		}, false},
		"PermissionsStartOnly":                 {map[string]string{unit: "[Service]\nUser=demo\nPermissionsStartOnly=yes\nExecStartPre=/usr/bin/demo prep\nExecStart=/usr/bin/demo\n"}, true},
		"PermissionsStartOnly, ExecStart only": {map[string]string{unit: "[Service]\nUser=demo\nPermissionsStartOnly=yes\nExecStart=/usr/bin/demo\n"}, false},
		"an instance's drop-in resets User": {map[string]string{
			"usr/lib/systemd/system/demo@.service":            "[Service]\nUser=demo\n",
			"usr/lib/systemd/system/demo@.service.d/vos.conf": "[Service]\nTimeoutStartSec=5\n",
			"usr/lib/systemd/system/demo@x.service.d/x.conf":  "[Service]\nUser=\n",
		}, true},
		"socket with commands": {map[string]string{
			"usr/lib/systemd/system/demo.socket":            "[Socket]\nListenStream=1234\nExecStartPre=/usr/bin/demo\n",
			"usr/lib/systemd/system/demo.socket.d/vos.conf": "[Socket]\nTimeoutSec=10\n",
		}, true},
		"socket with commands and User": {map[string]string{
			"usr/lib/systemd/system/demo.socket":            "[Socket]\nListenStream=1234\nExecStartPre=/usr/bin/demo\nUser=demo\n",
			"usr/lib/systemd/system/demo.socket.d/vos.conf": "[Socket]\nTimeoutSec=10\n",
		}, false},
		"socket without commands": {map[string]string{"usr/lib/systemd/system/demo.socket": "[Socket]\nListenStream=1234\n"}, false},
		"user service": {map[string]string{
			"usr/lib/systemd/user/demo.service": "[Service]\nExecStart=+/usr/bin/demo\n",
		}, false},
	} {
		t.Run(name, func(t *testing.T) {
			tree, d := newDemo(t)
			writeTree(t, tree, c.files)
			if _, ok := c.files["usr/lib/systemd/user/demo.service"]; ok {
				d = declare(t, tree, "service", "user-service")
			}
			r := run(t, tree, base, d)
			wantClean(t, r)
			if r.RunsAsRoot != c.root {
				t.Fatalf("runs_as_root %v, want %v", r.RunsAsRoot, c.root)
			}
		})
	}
}

func TestTemplateWithBaseInstances(t *testing.T) {
	base := newBase(t)
	writeTree(t, base, map[string]string{"etc/systemd/system/demo-tty@tty1.service": "[Service]\n"})
	tree, d := newDemo(t)
	writeTree(t, tree, map[string]string{
		"usr/lib/systemd/system/demo-tty@.service":            "[Service]\nUser=demo\n",
		"usr/lib/systemd/system/demo-tty@.service.d/vos.conf": "[Service]\nTimeoutStartSec=5\n",
	})
	wantProblem(t, run(t, tree, base, d), "usr/lib/systemd/system/demo-tty@.service: the base has instances of demo-tty@.service")
}

func TestInstanceOf(t *testing.T) {
	for _, c := range [][3]string{
		{"foo@.service", "bar@x.service", "foo@x.service"},
		{"foo@.service", "bar@a.b.service", "foo@a.b.service"},
		{"foo@.service", "bar@x.socket", ""},
		{"foo@.service", "bar@.service", ""},
		{"foo.service", "bar@x.service", ""},
	} {
		got, ok := instanceOf(c[0], c[1])
		if got != c[2] || ok != (c[2] != "") {
			t.Errorf("instanceOf(%s, %s) = %q %v, want %q", c[0], c[1], got, ok, c[2])
		}
	}
}

func TestParseTimespan(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"90":        90 * time.Second,
		"90s":       90 * time.Second,
		"1min 30s":  90 * time.Second,
		"1min30s":   90 * time.Second,
		"5m":        5 * time.Minute,
		"1.5h":      90 * time.Minute,
		"500ms":     500 * time.Millisecond,
		" 2 hours ": 2 * time.Hour,
		"0":         0,
	} {
		d, inf, ok := parseTimespan(in)
		if !ok || inf || d != want {
			t.Errorf("%q: %v %v %v, want %v", in, d, inf, ok, want)
		}
	}
	if _, inf, ok := parseTimespan("infinity"); !ok || !inf {
		t.Error("infinity")
	}
	for _, in := range []string{"", "soon", "5 parsecs", "s", "1..2s"} {
		if _, _, ok := parseTimespan(in); ok {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestParseUnit(t *testing.T) {
	got := parseUnit("u", []byte("# comment\n[Unit]\nDescription=A \\\n  long \\\n# skipped\n  name\n; also a comment\n[Service]\n ExecStart = /bin/x\nnot an assignment\nUser=\n\\"))
	want := []assignment{
		{"u", "Unit", "Description", "A  long  name"},
		{"u", "Service", "ExecStart", "/bin/x"},
		{"u", "Service", "User", ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%q\nwant %q", got, want)
	}
}

func TestUnitTemplate(t *testing.T) {
	for in, want := range map[string]string{
		"getty@tty1.service": "getty@.service",
		"getty@.service":     "",
		"a@b.c@d.service":    "a@.service",
		"plain.service":      "",
	} {
		got, ok := unitTemplate(in)
		if (want == "") == ok || got != want {
			t.Errorf("%s: %q %v, want %q", in, got, ok, want)
		}
	}
}
