package web

// iconPaths is the UI's icon set: 24×24 outline drawings, stroked with
// currentColor by the .icon CSS class. Each page inlines them once as an SVG
// sprite of <symbol>s, and markup (server or JS) references them with
// <use href="#i-name">, so an icon costs a few bytes wherever it is used and
// needs no extra request. The code that does that is in icons.go. Both UI
// sets use these names, so while both exist names are only ever added.
var iconPaths = map[string]string{
	"home":     `<path d="M3.5 10.5 12 3.5l8.5 7"/><path d="M5.5 9v11h13V9"/><path d="M10 20v-5.5h4V20"/>`,
	"phone":    `<rect x="6.5" y="2.5" width="11" height="19" rx="2.5"/><path d="M10.5 18.5h3"/>`,
	"gamepad":  `<path d="M7 7.5h10a4.5 4.5 0 0 1 4.5 4.5v2a3.5 3.5 0 0 1-6.2 2.2L14 14.5h-4l-1.3 1.7A3.5 3.5 0 0 1 2.5 14v-2A4.5 4.5 0 0 1 7 7.5z"/><path d="M7.5 10.5v3M6 12h3"/><path d="M15.5 11h.01M17.5 13h.01"/>`,
	"monitor":  `<rect x="2.5" y="3.5" width="19" height="13" rx="2"/><path d="M8 20.5h8M12 16.5v4"/>`,
	"drive":    `<path d="M5.2 5.3 2.5 13v5a2 2 0 0 0 2 2h15a2 2 0 0 0 2-2v-5l-2.7-7.7A2 2 0 0 0 16.9 4H7.1a2 2 0 0 0-1.9 1.3z"/><path d="M2.5 13h19"/><path d="M6.5 16.5h.01M10 16.5h.01"/>`,
	"update":   `<path d="M20.5 12a8.5 8.5 0 1 1-2.6-6.1"/><path d="M20.5 4v4.5H16"/><path d="M12 8v4.5l3 1.5"/>`,
	"power":    `<path d="M12 2.5v8.5"/><path d="M6.3 6.2a8 8 0 1 0 11.4 0"/>`,
	"sliders":  `<path d="M4 6h9M17 6h3M4 12h3M11 12h9M4 18h11M19 18h1"/><circle cx="15" cy="6" r="2"/><circle cx="9" cy="12" r="2"/><circle cx="17" cy="18" r="2"/>`,
	"logout":   `<path d="M14.5 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3"/><path d="m9.5 16-4-4 4-4"/><path d="M5.5 12h10"/>`,
	"menu":     `<path d="M4 7h16M4 12h16M4 17h16"/>`,
	"close":    `<path d="M6 6l12 12M18 6 6 18"/>`,
	"check":    `<path d="m5 12.5 4.5 4.5L19 7.5"/>`,
	"alert":    `<path d="M10.3 4.2 2.6 17.5A2 2 0 0 0 4.3 20.5h15.4a2 2 0 0 0 1.7-3L13.7 4.2a2 2 0 0 0-3.4 0z"/><path d="M12 9.5v4M12 17h.01"/>`,
	"info":     `<circle cx="12" cy="12" r="9"/><path d="M12 11v5.5M12 7.5h.01"/>`,
	"external": `<path d="M14 4h6v6M20 4l-8.5 8.5"/><path d="M18 14v4a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h4"/>`,
	"download": `<path d="M12 3.5v11.5M7 10l5 5 5-5"/><path d="M4.5 20h15"/>`,
	"key":      `<circle cx="7.5" cy="15.5" r="4"/><path d="m10.5 12.5 9-9M16 7l2.5 2.5M13.5 9.5l2 2"/>`,
	"terminal": `<rect x="2.5" y="4" width="19" height="16" rx="2"/><path d="m7 9.5 3 2.5-3 2.5M12.5 15H17"/>`,
	"restart":  `<path d="M3.5 12a8.5 8.5 0 1 0 2.6-6.1"/><path d="M3.5 4v4.5H8"/>`,
	"coffee":   `<path d="M4 9h13v4.5a5.5 5.5 0 0 1-5.5 5.5h-2A5.5 5.5 0 0 1 4 13.5z"/><path d="M17 10h1.5a2.5 2.5 0 0 1 0 5H17"/><path d="M8 3.5V6M12 3.5V6"/>`,
	"moon":     `<path d="M20 14.5A8.5 8.5 0 1 1 9.5 4a6.5 6.5 0 0 0 10.5 10.5z"/>`,
	"plus":     `<path d="M12 5v14M5 12h14"/>`,
	"trash":    `<path d="M4 7h16M9.5 7V4.5h5V7M6.5 7l1 13h9l1-13"/>`,
	"globe":    `<circle cx="12" cy="12" r="9"/><path d="M3 12h18"/><path d="M12 3c2.4 2.5 3.6 5.5 3.6 9s-1.2 6.5-3.6 9c-2.4-2.5-3.6-5.5-3.6-9S9.6 5.5 12 3z"/>`,
	"zap":      `<path d="M13 2.5 4.5 13.5H12l-1 8 8.5-11H12z"/>`,
	"cpu":      `<rect x="6" y="6" width="12" height="12" rx="2"/><path d="M9.5 2.5v3M14.5 2.5v3M9.5 18.5v3M14.5 18.5v3M2.5 9.5h3M2.5 14.5h3M18.5 9.5h3M18.5 14.5h3"/>`,
	"wifi":     `<path d="M2.5 9a14 14 0 0 1 19 0M5.5 12.5a9.5 9.5 0 0 1 13 0M8.5 16a5 5 0 0 1 7 0"/><path d="M12 19.5h.01"/>`,
	"shield":   `<path d="M12 3 4.5 6v5.5c0 4.5 3.2 8.2 7.5 9.5 4.3-1.3 7.5-5 7.5-9.5V6z"/><path d="m9 12 2.2 2.2L15.5 10"/>`,
	"hdr":      `<rect x="2.5" y="6" width="19" height="12" rx="2.5"/><path d="M6.5 9.5v5M9.5 9.5v5M6.5 12h3M12.5 9.5v5h1.2a2.5 2.5 0 0 0 0-5z"/><path d="M17.5 14.5v-5h1a1.5 1.5 0 0 1 0 3h-1M18.8 12.5l1.2 2"/>`,
	"usb":      `<path d="M9 2.5h6v5.5H9z"/><path d="M7 8h10v7.5a5 5 0 0 1-10 0z"/><path d="M10.5 5h.01M13.5 5h.01"/>`,
	"library":  `<path d="M4 4.5h3.5v15H4zM10 4.5h3.5v15H10z"/><path d="m16 5.2 3.3-.9 3.2 14.3-3.3.9z"/>`,
	"clock":    `<circle cx="12" cy="12" r="9"/><path d="M12 7v5.5l3.5 2"/>`,
	"sparkle":  `<path d="M12 3.5 13.8 10.2 20.5 12l-6.7 1.8L12 20.5l-1.8-6.7L3.5 12l6.7-1.8z"/>`,
	"eye":      `<path d="M2.5 12S6 5.5 12 5.5 21.5 12 21.5 12 18 18.5 12 18.5 2.5 12 2.5 12z"/><circle cx="12" cy="12" r="3"/>`,
	"file":     `<path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z"/><path d="M14 3v5h5M9 13h6M9 17h6"/>`,
	"chevron":  `<path d="m9 6 6 6-6 6"/>`,
	"back":     `<path d="m15 6-6 6 6 6"/>`,
}
