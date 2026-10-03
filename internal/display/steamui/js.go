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
