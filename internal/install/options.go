package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	defaultHostname = "vapor"
	minPasswordLen  = 8
	// maxPasswordLen bounds the argon2 input; nobody types more.
	maxPasswordLen = 1024
)

var (
	// hostnameRE is one RFC 1123 label: the name is also advertised over
	// mDNS as <hostname>.local, so dots are not allowed.
	hostnameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	// timezoneRE matches tz database names ("Europe/Brussels", "Etc/GMT+1",
	// "America/Argentina/Buenos_Aires"). No dots, so no path traversal.
	timezoneRE = regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+){0,3}$`)
	// fsUUIDRE covers ext4/btrfs/xfs UUIDs, "4760-BB01" (FAT) and NTFS serials.
	fsUUIDRE = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
)

// normalize validates opts without touching the system, fills defaults and
// canonicalises values. Everything that needs the disk or the image is
// checked later by the installer's probe step.
func (o *Options) normalize() error {
	o.Disk = strings.TrimSpace(o.Disk)
	if o.Disk == "" {
		return fmt.Errorf("no disk given")
	}
	o.Mode = strings.ToLower(strings.TrimSpace(o.Mode))
	switch o.Mode {
	case "":
		o.Mode = ModeErase
	case ModeErase, ModeRepair:
	default:
		return fmt.Errorf("mode must be %q or %q, not %q", ModeErase, ModeRepair, o.Mode)
	}

	o.Hostname = strings.ToLower(strings.TrimSpace(o.Hostname))
	if o.Hostname == "" && o.Mode == ModeErase {
		o.Hostname = defaultHostname
	}
	if o.Hostname != "" {
		if err := checkHostname(o.Hostname); err != nil {
			return err
		}
	}

	if o.Password != "" {
		n := utf8.RuneCountInString(o.Password)
		if n < minPasswordLen {
			return fmt.Errorf("the admin password needs at least %d characters", minPasswordLen)
		}
		if len(o.Password) > maxPasswordLen || !utf8.ValidString(o.Password) || strings.ContainsRune(o.Password, 0) {
			return fmt.Errorf("the admin password is not valid")
		}
	}

	o.Timezone = strings.TrimSpace(o.Timezone)
	if o.Timezone != "" && !validTimezoneName(o.Timezone) {
		return fmt.Errorf("invalid timezone %q", o.Timezone)
	}

	seen := map[string]bool{}
	libs := []string{}
	for _, u := range o.Libraries {
		u = strings.TrimSpace(u)
		if u == "" || seen[u] {
			continue
		}
		if !fsUUIDRE.MatchString(u) {
			return fmt.Errorf("invalid filesystem UUID %q", u)
		}
		seen[u] = true
		libs = append(libs, u)
	}
	o.Libraries = libs

	return o.normalizeSource()
}

// normalizeSource checks Source and Channel (parseSource, checkChannel).
// The live medium is always "", whether it was given as "" or "live".
func (o *Options) normalizeSource() error {
	o.Source = strings.TrimSpace(o.Source)
	spec, err := parseSource(o.Source)
	if err != nil {
		return err
	}
	if spec.kind == srcLive {
		o.Source = ""
	}
	o.Channel = strings.TrimSpace(o.Channel)
	return checkChannel(o.Channel)
}

// checkHostname is PUT /system/hostname's rule (system.ValidateHostname):
// one lower-case label, and not "localhost", which would shadow the
// loopback name on every client.
func checkHostname(name string) error {
	if !hostnameRE.MatchString(name) {
		return fmt.Errorf("invalid hostname %q: use 1-63 letters, digits and hyphens", name)
	}
	if name == "localhost" {
		return errors.New(`invalid hostname: "localhost" is reserved`)
	}
	return nil
}

func validTimezoneName(tz string) bool {
	return timezoneRE.MatchString(tz)
}

// checkTimezone reports whether tz exists as a zone file in the tz
// database rooted at zoneinfo.
func checkTimezone(zoneinfo, tz string) error {
	if !validTimezoneName(tz) {
		return fmt.Errorf("invalid timezone %q", tz)
	}
	fi, err := os.Stat(filepath.Join(zoneinfo, tz))
	if err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("unknown timezone %q", tz)
	}
	return nil
}
