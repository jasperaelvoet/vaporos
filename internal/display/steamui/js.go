package steamui

import (
	"encoding/json"
	"strings"
)

// The expressions vosd evaluates in Steam's SharedJSContext. None of them
// waits (Go waits between reads), every one reads Steam's state
// defensively, and numbers reach them only as JSON arguments (call).
//
// jsState opens each: Steam's display settings (settingsStore.settings,
// a copy of what the client sends), its two setters, VaporOS's marker
// window.__vosScale ({gen, value}: the generation that last set the scale
// and what it set, 0 for Steam's automatic scale) and whether Steam is
// ready, which is when its own display settings would let the user
// change the scale: the settings are loaded (bDisplayIsUsingAutoScale
// defined, Steam's own test), the display has a name, as Steam shows it
// (prefix and |||suffix cut, never xwayland…), and both setters exist.
// Without bounds Steam's slider uses 0.5 and 2.5, and so does a set.
const jsState = `
const st = window.settingsStore?.settings;
const sc = typeof SteamClient !== "undefined" && SteamClient !== null ? SteamClient : null;
const w = sc?.Window;
const api = typeof w?.SetGamepadUIAutoDisplayScale === "function" &&
	typeof w?.SetGamepadUIManualDisplayScaleFactor === "function";
const num = (v) => typeof v === "number" && Number.isFinite(v) ? v : null;
const name = typeof st?.strDisplayName === "string" ? st.strDisplayName : "";
let shown = name.replace("Internal: ", "").replace("External: ", "");
if (shown.lastIndexOf("|||") > 0) shown = shown.substring(0, shown.lastIndexOf("|||"));
const ready = !!st && st.bDisplayIsUsingAutoScale !== undefined && shown !== "" &&
	!shown.toLowerCase().startsWith("xwayland") && api;
const lo = num(st?.flMinDisplayScaleFactor) ?? 0.5;
const hi = num(st?.flMaxDisplayScaleFactor) ?? 2.5;
const mk = window.__vosScale;
const mgen = Number.isSafeInteger(mk?.gen) && mk.gen > 0 ? mk.gen : 0;
const refuse = (gen) => mgen > gen ? {status: "stale", gen: mgen}
	: !ready ? {status: sc !== null && !api ? "unsupported" : "notready"} : null;
`

// readJS returns Steam's scale and the marker (readResult).
const readJS = `(() => {` + jsState + `
return {ready, client: sc !== null, api, name: name.slice(0, 256),
	external: st?.bDisplayIsExternal === true, auto: st?.bDisplayIsUsingAutoScale === true,
	current: num(st?.flCurrentDisplayScaleFactor), autoValue: num(st?.flAutoDisplayScaleFactor),
	min: lo, max: hi, gen: mgen, value: mgen > 0 ? num(mk.value) : null};
})()`

// setJS(gen, s): unless a newer generation is in, the manual factor s
// clamped to Steam's bounds, as Steam's own slider sets it (applyResult).
const setJS = `(gen, s) => {` + jsState + `
const no = refuse(gen);
if (no) return no;
const v = Math.min(hi, Math.max(lo, s));
w.SetGamepadUIAutoDisplayScale(false);
w.SetGamepadUIManualDisplayScaleFactor(v);
window.__vosScale = {gen, value: v};
return {status: "ok", value: v};
}`

// autoJS(gen): unless a newer generation is in, Steam's automatic scale.
const autoJS = `(gen) => {` + jsState + `
const no = refuse(gen);
if (no) return no;
w.SetGamepadUIAutoDisplayScale(true);
window.__vosScale = {gen, value: 0};
return {status: "ok", value: 0};
}`

// viewJS is a view's scale and height, in that view's own target.
const viewJS = `(() => ({dpr: typeof devicePixelRatio === "number" ? devicePixelRatio : 0,
	h: typeof innerHeight === "number" ? innerHeight : 0}))()`

// Steam's sleep (docs/CONTRACTS.md, Display policy, "Steam's sleep").
// SetSetting takes a serialized CMsgClientSettings holding the one field
// it changes, in base64, as Steam's own settings page sends it: field
// 24004 (system_idle_suspend_ac_sec) and 24003 (..._battery_sec), each a
// varint 0, "Never".
const (
	idleSuspendACNever      = "oNwLAA=="
	idleSuspendBatteryNever = "mNwLAA=="
)

// jsQuiet keeps a promise Steam returns from becoming an unhandled
// rejection, without waiting for it: Runtime.evaluate would await a
// promise returned to it.
const jsQuiet = `
const sc = typeof SteamClient !== "undefined" && SteamClient !== null ? SteamClient : null;
const quiet = (p) => {
	try {
		if (p !== null && (typeof p === "object" || typeof p === "function") && typeof p.then === "function") p.then(undefined, () => {});
	} catch (e) {}
};
`

// sleepJS reads Steam's suspend state (window.SuspendResumeStore) and its
// idle sleep settings (sleepResult). It never throws: what Steam does not
// have, or throws on, reads as missing.
const sleepJS = `(() => {` + jsQuiet + `
const get = (f) => { try { return f(); } catch (e) { return undefined; } };
const num = (v) => typeof v === "number" && Number.isFinite(v) ? v : null;
const sr = get(() => window.SuspendResumeStore);
const cs = get(() => window.settingsStore?.clientSettings);
return {store: typeof sr === "object" && sr !== null,
	suspending: get(() => sr?.suspending) === true,
	progress: num(get(() => typeof sr?.GetSuspendResumeState === "function" ? sr.GetSuspendResumeState() : null)),
	resume: typeof get(() => sr?.InitiateResume) === "function",
	set: typeof get(() => sc?.Settings?.SetSetting) === "function",
	ac: num(get(() => cs?.system_idle_suspend_ac_sec)),
	battery: num(get(() => cs?.system_idle_suspend_battery_sec))};
})()`

// neverSleepJS sets both idle sleep settings to Never (statusResult).
const neverSleepJS = `(() => {` + jsQuiet + `
const s = sc?.Settings;
if (typeof s?.SetSetting !== "function") return {status: "unsupported"};
quiet(s.SetSetting("` + idleSuspendACNever + `"));
quiet(s.SetSetting("` + idleSuspendBatteryNever + `"));
return {status: "ok"};
})()`

// resumeJS takes Steam's interface out of its suspend screen, as Steam
// does when the system resumes, only while it is in it, so a second send
// does nothing (statusResult). InitiateResume leaves the suspend screen
// before its first await and returns nothing.
const resumeJS = `(() => {` + jsQuiet + `
const sr = window.SuspendResumeStore;
if (typeof sr?.InitiateResume !== "function") return {status: "unsupported"};
if (sr.suspending !== true) return {status: "awake"};
quiet(sr.InitiateResume());
return {status: sr.suspending === true ? "suspending" : "resumed"};
})()`

// call is the expression that calls fn with args, each written as JSON.
// Callers pass only numbers they checked, which always encode.
func call(fn string, args ...any) string {
	var b strings.Builder
	b.WriteString("(" + fn + ")(")
	for i, a := range args {
		j, err := json.Marshal(a)
		if err != nil {
			panic("steamui: " + err.Error())
		}
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(j)
	}
	b.WriteByte(')')
	return b.String()
}
