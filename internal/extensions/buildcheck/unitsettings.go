package buildcheck

import (
	"fmt"
	"os"
	"path"
	"strings"
)

// baseEffects are the [Unit] settings that do something to the units they
// name, so they may not name one of the base's: what the setting would do.
// Default dependencies add Conflicts=shutdown.target to every unit anyway.
// PartOf=, BindsTo= and StopPropagatedFrom= are not here: they only make
// the extension's own unit follow the one they name.
var baseEffects = map[string]string{
	"Before":             "orders it before",
	"Conflicts":          "stops, whenever it starts,",
	"OnFailure":          "starts, when it fails,",
	"OnSuccess":          "starts, when it succeeds,",
	"PropagatesStopTo":   "stops, whenever it stops,",
	"PropagatesReloadTo": "reloads, whenever it reloads,",
	"PropagateReloadTo":  "reloads, whenever it reloads,",
	"Upholds":            "keeps restarting",
	"JoinsNamespaceOf":   "joins the namespaces of",
}

// depKeys are the [Unit] settings that pull in or need the units they name
// (with the old names systemd still reads), so none may name a unit that
// changes the system's state.
var depKeys = map[string]bool{
	"Wants": true, "Requires": true, "Requisite": true, "Upholds": true, "BindsTo": true,
	"BindTo": true, "RequiresOverridable": true, "RequisiteOverridable": true,
}

// systemStateUnits are the units whose start reboots, powers off, suspends
// or rescues the box, or ends a manager: the targets and their services.
var systemStateUnits = map[string]bool{
	"reboot.target": true, "poweroff.target": true, "halt.target": true, "kexec.target": true,
	"soft-reboot.target": true, "exit.target": true, "emergency.target": true, "rescue.target": true,
	"shutdown.target": true, "final.target": true, "ctrl-alt-del.target": true, "sleep.target": true,
	"suspend.target": true, "hibernate.target": true, "hybrid-sleep.target": true,
	"suspend-then-hibernate.target": true,

	"systemd-reboot.service": true, "systemd-poweroff.service": true, "systemd-halt.service": true,
	"systemd-kexec.service": true, "systemd-soft-reboot.service": true, "systemd-suspend.service": true,
	"systemd-hibernate.service": true, "systemd-hybrid-sleep.service": true,
	"systemd-suspend-then-hibernate.service": true, "systemd-exit.service": true,
	"emergency.service": true, "rescue.service": true,
}

// actionKeys are the settings that make the manager reboot, power off or
// exit when a unit fails, succeeds, hits its start limit or a job of it
// times out.
var actionKeys = map[string]bool{"FailureAction": true, "SuccessAction": true, "StartLimitAction": true, "JobTimeoutAction": true}

// unsafeJobModes are the job modes that reach other units' jobs, with what
// each does.
var unsafeJobModes = map[string]string{
	"isolate": "stops every unit the started one does not need",
	"flush":   "cancels every other queued job",
}

// settingProblems applies the rules for one setting of an extension's
// unit: nothing that acts on the whole system, and nothing that acts on a
// unit of the base (baseEffects) or depends on the system's state.
func (s *unitScope) settingProblems(base string, a assignment) []string {
	setting := a.key + "=" + a.value
	switch {
	case actionKeys[a.key]:
		// Every section: [Service] still reads the old FailureAction= and
		// StartLimitAction=.
		if a.value != "none" {
			return []string{setting + ": an extension's unit may only set none, never make the manager reboot, power off or exit"}
		}
		return nil
	case a.section != "Unit":
		return nil
	case a.key == "OnFailureJobMode" || a.key == "OnSuccessJobMode":
		if why, ok := unsafeJobModes[a.value]; ok {
			return []string{setting + " " + why}
		}
		return nil
	case a.key == "OnFailureIsolate" && parseBool(a.value):
		return []string{setting + " " + unsafeJobModes["isolate"]}
	case a.key == "AllowIsolate" && parseBool(a.value):
		return []string{setting + " lets it be isolated, which stops every unit it does not need"}
	}
	var out []string
	for _, b := range strings.Fields(a.value) {
		if depKeys[a.key] {
			if what := s.systemState(base, b); what != "" {
				out = append(out, fmt.Sprintf("%s=%s names %s", a.key, b, what))
				continue
			}
		}
		effect, ok := baseEffects[a.key]
		if !ok || (a.key == "Conflicts" && b == "shutdown.target") {
			continue
		}
		if p := s.baseUnit(base, b); p != "" {
			out = append(out, fmt.Sprintf("%s=%s %s %s", a.key, b, effect, p))
		}
	}
	return out
}

// systemState says what u is when an extension's unit may not depend on
// it: one of systemStateUnits, also through the base's aliases (the
// runlevel6.target of reboot.target), or a name with specifiers outside its
// instance, which this check does not resolve. "" otherwise.
func (s *unitScope) systemState(base, u string) string {
	stem := u
	if t, ok := unitTemplate(u); ok {
		stem = t
	}
	if strings.Contains(stem, "%") {
		return "a unit named with specifiers this check does not resolve"
	}
	seen := map[string]bool{}
	for todo := []string{u}; len(todo) > 0 && len(seen) <= maxLinks; {
		n := todo[0]
		todo = todo[1:]
		switch {
		case seen[n]:
			continue
		case systemStateUnits[n] && n == u:
			return "a unit that changes the system's state (reboot, power off, sleep, rescue or exit)"
		case systemStateUnits[n]:
			return "an alias the base has of " + n + ", which changes the system's state"
		}
		seen[n] = true
		todo = append(todo, s.baseAliases(base, n)...)
	}
	return ""
}

// baseAliases returns the units the base's symlinks named n, in this
// scope, point at.
func (s *unitScope) baseAliases(base, n string) []string {
	if strings.Contains(n, "/") {
		return nil
	}
	var out []string
	for _, d := range s.baseDirs {
		host, err := resolveIn(base, d+"/"+n, false)
		if err != nil {
			continue
		}
		if t, err := os.Readlink(host); err == nil {
			out = append(out, path.Base(t))
		}
	}
	return out
}
