package web

import (
	"strings"
)

// timezones is the installer's choice of IANA zones: every zone a person is
// likely to live in, not the full 400-entry database. The probe's detected
// zone is preselected, and the page adds it to the list when it is missing
// here, so an unusual zone still installs correctly.
var timezones = []string{
	"UTC",
	"Africa/Abidjan", "Africa/Accra", "Africa/Algiers", "Africa/Cairo", "Africa/Casablanca",
	"Africa/Johannesburg", "Africa/Lagos", "Africa/Nairobi", "Africa/Tunis",
	"America/Anchorage", "America/Argentina/Buenos_Aires", "America/Bogota", "America/Caracas",
	"America/Chicago", "America/Denver", "America/Edmonton", "America/Halifax", "America/Havana",
	"America/Lima", "America/Los_Angeles", "America/Mexico_City", "America/Montevideo",
	"America/New_York", "America/Panama", "America/Phoenix", "America/Puerto_Rico",
	"America/Santiago", "America/Sao_Paulo", "America/St_Johns", "America/Toronto",
	"America/Vancouver", "America/Winnipeg",
	"Asia/Almaty", "Asia/Baghdad", "Asia/Baku", "Asia/Bangkok", "Asia/Dhaka", "Asia/Dubai",
	"Asia/Ho_Chi_Minh", "Asia/Hong_Kong", "Asia/Jakarta", "Asia/Jerusalem", "Asia/Karachi",
	"Asia/Kathmandu", "Asia/Kolkata", "Asia/Kuala_Lumpur", "Asia/Manila", "Asia/Riyadh",
	"Asia/Seoul", "Asia/Shanghai", "Asia/Singapore", "Asia/Taipei", "Asia/Tashkent",
	"Asia/Tbilisi", "Asia/Tehran", "Asia/Tokyo", "Asia/Yangon", "Asia/Yerevan",
	"Atlantic/Azores", "Atlantic/Canary", "Atlantic/Reykjavik",
	"Australia/Adelaide", "Australia/Brisbane", "Australia/Darwin", "Australia/Hobart",
	"Australia/Melbourne", "Australia/Perth", "Australia/Sydney",
	"Europe/Amsterdam", "Europe/Athens", "Europe/Belgrade", "Europe/Berlin", "Europe/Brussels",
	"Europe/Bucharest", "Europe/Budapest", "Europe/Copenhagen", "Europe/Dublin", "Europe/Helsinki",
	"Europe/Istanbul", "Europe/Kyiv", "Europe/Lisbon", "Europe/London", "Europe/Luxembourg",
	"Europe/Madrid", "Europe/Moscow", "Europe/Oslo", "Europe/Paris", "Europe/Prague", "Europe/Riga",
	"Europe/Rome", "Europe/Sofia", "Europe/Stockholm", "Europe/Tallinn", "Europe/Vienna",
	"Europe/Vilnius", "Europe/Warsaw", "Europe/Zurich",
	"Pacific/Auckland", "Pacific/Fiji", "Pacific/Guam", "Pacific/Honolulu",
}

// tzGroup is one <optgroup> of the timezone select.
type tzGroup struct {
	Region string
	Zones  []tzOption
}

type tzOption struct {
	Value string // IANA name, what the installer receives
	Label string // "Buenos Aires"
}

// timezoneGroups groups timezones by region, in list order.
func timezoneGroups() []tzGroup {
	var groups []tzGroup
	for _, tz := range timezones {
		region, city, ok := strings.Cut(tz, "/")
		if !ok {
			region, city = "Other", tz
		}
		if i := strings.LastIndex(city, "/"); i >= 0 {
			city = city[i+1:]
		}
		opt := tzOption{Value: tz, Label: strings.ReplaceAll(city, "_", " ")}
		if n := len(groups); n > 0 && groups[n-1].Region == region {
			groups[n-1].Zones = append(groups[n-1].Zones, opt)
			continue
		}
		groups = append(groups, tzGroup{Region: region, Zones: []tzOption{opt}})
	}
	return groups
}
