package buildcheck

import (
	"strings"
	"testing"
)

func TestUnitSettings(t *testing.T) {
	base := newBase(t)
	writeTree(t, base, map[string]string{
		"usr/lib/systemd/system/reboot.target":       "[Unit]\n",
		"usr/lib/systemd/system/runlevel6.target":    "@reboot.target",
		"etc/systemd/system/vos-restart.target":      "@/usr/lib/systemd/system/runlevel6.target",
		"usr/lib/systemd/system/runlevel3.target":    "@multi-user.target",
		"usr/lib/systemd/user/exit.target":           "[Unit]\n",
		"usr/lib/systemd/user/vos-exit-alias.target": "@exit.target",
	})
	const unit = "usr/lib/systemd/system/demo.service"
	const dropIn = "usr/lib/systemd/system/demo.service.d/vos.conf"
	const userUnit = "usr/lib/systemd/user/demo-user.service"
	svc := func(unitSection string) string { return "[Unit]\n" + unitSection + "[Service]\nUser=demo\n" }
	for name, c := range map[string]struct {
		files map[string]string
		want  string // a problem, or "" for none
	}{
		"FailureAction":                 {files: map[string]string{unit: svc("FailureAction=reboot\n")}, want: unit + ": FailureAction=reboot: an extension's unit may only set none"},
		"FailureAction none":            {files: map[string]string{unit: svc("FailureAction=none\nSuccessAction=none\nStartLimitAction=none\nJobTimeoutAction=none\n")}},
		"FailureAction in [Service]":    {files: map[string]string{unit: "[Service]\nUser=demo\nFailureAction=poweroff-force\n"}, want: "FailureAction=poweroff-force: an extension's unit may only set none"},
		"SuccessAction":                 {files: map[string]string{unit: svc("SuccessAction=exit\n")}, want: "SuccessAction=exit: an extension's unit may only set none"},
		"StartLimitAction":              {files: map[string]string{unit: svc("StartLimitAction=reboot-immediate\n")}, want: "StartLimitAction=reboot-immediate: an extension's unit may only set none"},
		"StartLimitAction in [Service]": {files: map[string]string{unit: "[Service]\nUser=demo\nStartLimitAction=reboot\n"}, want: "StartLimitAction=reboot: an extension's unit may only set none"},
		"JobTimeoutAction in a drop-in": {files: map[string]string{dropIn: "[Unit]\nJobTimeoutAction=reboot-force\n[Service]\nTimeoutStartSec=30\n"}, want: dropIn + ": JobTimeoutAction=reboot-force: an extension's unit may only set none"},
		"an empty action":               {files: map[string]string{unit: svc("FailureAction=\n")}, want: "FailureAction=: an extension's unit may only set none"},
		"a user unit's action":          {files: map[string]string{userUnit: "[Unit]\nFailureAction=exit-force\n[Service]\nExecStart=/usr/bin/demo\n"}, want: userUnit + ": FailureAction=exit-force"},

		"OnFailureJobMode=isolate":  {files: map[string]string{unit: svc("OnFailure=demo-notify.service\nOnFailureJobMode=isolate\n")}, want: "OnFailureJobMode=isolate stops every unit"},
		"OnSuccessJobMode=isolate":  {files: map[string]string{dropIn: "[Unit]\nOnSuccessJobMode=isolate\n[Service]\nTimeoutStartSec=30\n"}, want: dropIn + ": OnSuccessJobMode=isolate stops every unit"},
		"OnFailureJobMode=flush":    {files: map[string]string{unit: svc("OnFailureJobMode=flush\n")}, want: "OnFailureJobMode=flush cancels every other queued job"},
		"OnFailureJobMode=replace":  {files: map[string]string{unit: svc("OnFailure=demo-notify.service\nOnFailureJobMode=replace\nOnSuccessJobMode=fail\n")}},
		"OnFailureIsolate":          {files: map[string]string{unit: svc("OnFailureIsolate=yes\n")}, want: "OnFailureIsolate=yes stops every unit"},
		"OnFailureIsolate=no":       {files: map[string]string{unit: svc("OnFailureIsolate=no\n")}},
		"AllowIsolate":              {files: map[string]string{"usr/lib/systemd/system/demo.target": "[Unit]\nAllowIsolate=yes\n"}, want: "usr/lib/systemd/system/demo.target: AllowIsolate=yes lets it be isolated"},
		"AllowIsolate=no":           {files: map[string]string{"usr/lib/systemd/system/demo.target": "[Unit]\nAllowIsolate=no\n"}},
		"a job mode outside [Unit]": {files: map[string]string{unit: "[Service]\nUser=demo\nOnFailureJobMode=isolate\nAllowIsolate=yes\n"}},

		"Wants reboot":                {files: map[string]string{unit: svc("Wants=network-online.target reboot.target\n")}, want: unit + ": Wants=reboot.target names a unit that changes the system's state"},
		"Requires a sleep service":    {files: map[string]string{unit: svc("Requires=systemd-suspend.service\n")}, want: "Requires=systemd-suspend.service names a unit that changes"},
		"Requisite rescue":            {files: map[string]string{dropIn: "[Unit]\nRequisite=rescue.target\n[Service]\nTimeoutStartSec=30\n"}, want: dropIn + ": Requisite=rescue.target names a unit that changes"},
		"Upholds poweroff":            {files: map[string]string{unit: svc("Upholds=poweroff.target\n")}, want: "Upholds=poweroff.target names a unit that changes"},
		"BindsTo shutdown":            {files: map[string]string{unit: svc("BindsTo=shutdown.target\n")}, want: "BindsTo=shutdown.target names a unit that changes"},
		"BindTo kexec":                {files: map[string]string{unit: svc("BindTo=systemd-kexec.service\n")}, want: "BindTo=systemd-kexec.service names a unit that changes"},
		"RequiresOverridable halt":    {files: map[string]string{unit: svc("RequiresOverridable=halt.target\n")}, want: "RequiresOverridable=halt.target names a unit that changes"},
		"Wants emergency's service":   {files: map[string]string{unit: svc("Wants=emergency.service\n")}, want: "Wants=emergency.service names a unit that changes"},
		"Wants through a base alias":  {files: map[string]string{unit: svc("Wants=runlevel6.target\n")}, want: "Wants=runlevel6.target names an alias the base has of reboot.target"},
		"Wants through an /etc alias": {files: map[string]string{unit: svc("Wants=vos-restart.target\n")}, want: "Wants=vos-restart.target names an alias the base has of reboot.target"},
		"Wants a harmless alias":      {files: map[string]string{unit: svc("Wants=runlevel3.target multi-user.target vosd.service\nRequires=local.service\n")}},
		"Wants with a specifier":      {files: map[string]string{unit: svc("Wants=%p-x.target\n")}, want: "Wants=%p-x.target names a unit named with specifiers"},
		"Wants its own instance":      {files: map[string]string{unit: svc("Wants=demo-notify@%n.service\n")}},
		"PartOf reboot":               {files: map[string]string{unit: svc("PartOf=reboot.target\nStopPropagatedFrom=poweroff.target\n")}},
		"a user unit wants exit":      {files: map[string]string{userUnit: "[Unit]\nWants=exit.target\n[Service]\nExecStart=/usr/bin/demo\n"}, want: userUnit + ": Wants=exit.target names a unit that changes"},
		"a user unit's alias of exit": {files: map[string]string{userUnit: "[Unit]\nBindsTo=vos-exit-alias.target\n[Service]\nExecStart=/usr/bin/demo\n"}, want: "BindsTo=vos-exit-alias.target names an alias the base has of exit.target"},

		"wants entry for reboot": {files: map[string]string{
			"usr/lib/systemd/system/demo.service.wants/reboot.target": "@/usr/lib/systemd/system/reboot.target",
		}, want: "usr/lib/systemd/system/demo.service.wants/reboot.target: a dependency on a unit that changes the system's state"},
		"requires entry for a sleep service": {files: map[string]string{
			"usr/lib/systemd/system/demo.service.requires/systemd-hibernate.service": "@/usr/lib/systemd/system/systemd-hibernate.service",
		}, want: "demo.service.requires/systemd-hibernate.service: a dependency on a unit that changes"},
		"upholds entry for halt": {files: map[string]string{
			"usr/lib/systemd/system/demo.service.upholds/halt.target": "@/usr/lib/systemd/system/halt.target",
		}, want: "demo.service.upholds/halt.target: a dependency on a unit that changes"},
		"wants entry through an alias": {files: map[string]string{
			"usr/lib/systemd/system/demo.service.wants/runlevel6.target": "@/usr/lib/systemd/system/runlevel6.target",
		}, want: "demo.service.wants/runlevel6.target: a dependency on an alias the base has of reboot.target"},
		"wants entry named for reboot, linked elsewhere": {files: map[string]string{
			"usr/lib/systemd/system/demo.service.wants/soft-reboot.target": "@demo-helper.service",
		}, want: "demo.service.wants/soft-reboot.target: a dependency on a unit that changes"},
		"a user wants entry for exit": {files: map[string]string{
			"usr/lib/systemd/user/demo-user.service.wants/systemd-exit.service": "@/usr/lib/systemd/user/systemd-exit.service",
		}, want: "demo-user.service.wants/systemd-exit.service: a dependency on a unit that changes"},
	} {
		t.Run(name, func(t *testing.T) {
			tree, d := newDemo(t)
			for f := range c.files {
				if strings.HasPrefix(f, "usr/lib/systemd/user/") {
					writeTree(t, tree, map[string]string{userUnit: "[Service]\nExecStart=/usr/bin/demo\n"})
					d = declare(t, tree, "service", "user-service")
				}
			}
			writeTree(t, tree, c.files)
			r := run(t, tree, base, d)
			if c.want == "" {
				wantClean(t, r)
				return
			}
			wantProblem(t, r, c.want)
			if len(r.Problems) != 1 {
				t.Errorf("problems %q, want one", r.Problems)
			}
		})
	}
}

func TestSystemStateNames(t *testing.T) {
	s := newUnitScope("system", true)
	base := t.TempDir()
	for _, u := range []string{
		"reboot.target", "poweroff.target", "halt.target", "kexec.target", "soft-reboot.target", "exit.target",
		"emergency.target", "rescue.target", "shutdown.target", "final.target", "ctrl-alt-del.target",
		"sleep.target", "suspend.target", "hibernate.target", "hybrid-sleep.target", "suspend-then-hibernate.target",
		"systemd-reboot.service", "systemd-poweroff.service", "systemd-halt.service", "systemd-kexec.service",
		"systemd-soft-reboot.service", "systemd-suspend.service", "systemd-hibernate.service",
		"systemd-hybrid-sleep.service", "systemd-suspend-then-hibernate.service",
	} {
		if s.systemState(base, u) == "" {
			t.Errorf("%s: not a system-state unit", u)
		}
	}
	for _, u := range []string{"multi-user.target", "graphical.target", "network-online.target", "reboot@x.service", "systemd-logind.service"} {
		if w := s.systemState(base, u); w != "" {
			t.Errorf("%s: %q", u, w)
		}
	}
}
