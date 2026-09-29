package install

import (
	"fmt"
	"net/url"
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
	if o.Hostname != "" && !hostnameRE.MatchString(o.Hostname) {
		return fmt.Errorf("invalid hostname %q: use 1-63 letters, digits and hyphens", o.Hostname)
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

	o.Source = strings.TrimSpace(o.Source)
	if _, err := parseSource(o.Source); err != nil {
		return err
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

// sourceSpec is a parsed Options.Source.
type sourceSpec struct {
	kind string // "live", "dir" or "http"
	dir  string
	url  *url.URL
}

// parseSource accepts "" (the live medium), an absolute directory, a
// file:// URL or an http(s):// URL. OCI registries are the update
// package's business and not supported here yet.
func parseSource(s string) (sourceSpec, error) {
	switch {
	case s == "":
		return sourceSpec{kind: "live"}, nil
	case strings.HasPrefix(s, "oci://"):
		return sourceSpec{}, fmt.Errorf("oci:// sources are not supported by the installer; install from the live medium, a directory or an http(s) URL")
	case strings.HasPrefix(s, "http://"), strings.HasPrefix(s, "https://"):
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return sourceSpec{}, fmt.Errorf("invalid source URL %q", s)
		}
		return sourceSpec{kind: "http", url: u}, nil
	case strings.HasPrefix(s, "file://"):
		u, err := url.Parse(s)
		if err != nil || (u.Host != "" && u.Host != "localhost") || !filepath.IsAbs(u.Path) {
			return sourceSpec{}, fmt.Errorf("invalid source URL %q: use file:///absolute/dir", s)
		}
		return sourceSpec{kind: "dir", dir: filepath.Clean(u.Path)}, nil
	case filepath.IsAbs(s):
		return sourceSpec{kind: "dir", dir: filepath.Clean(s)}, nil
	}
	return sourceSpec{}, fmt.Errorf("unsupported source %q: use an absolute directory or an http(s) URL", s)
}
