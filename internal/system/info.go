package system

import (
	"bufio"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Kernel interfaces read by Info; variables so tests can use fixtures.
var (
	hwmonRoot   = "/sys/class/hwmon"
	uptimePath  = "/proc/uptime"
	cpuinfoPath = "/proc/cpuinfo"
)

// readOSRelease parses an os-release file (KEY=value, optionally quoted).
func readOSRelease(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		} else {
			v = strings.Trim(v, `"'`)
		}
		out[k] = v
	}
	return out
}

// uptimeSeconds reads the first field of /proc/uptime; 0 if unavailable.
func uptimeSeconds() int64 {
	b, err := os.ReadFile(uptimePath)
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(b))
	if len(fields) == 0 {
		return 0
	}
	f, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || f < 0 {
		return 0
	}
	return int64(f)
}

// cpuModel is the first "model name" in /proc/cpuinfo (x86), else the ARM
// "Hardware"/"Processor" line, else the architecture.
func cpuModel() string {
	f, err := os.Open(cpuinfoPath)
	if err == nil {
		defer f.Close()
		fallback := ""
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			k, v, ok := strings.Cut(sc.Text(), ":")
			if !ok {
				continue
			}
			k, v = strings.TrimSpace(k), strings.Join(strings.Fields(v), " ")
			switch k {
			case "model name":
				if v != "" {
					return v
				}
			case "Hardware", "Processor":
				if fallback == "" {
					fallback = v
				}
			}
		}
		if fallback != "" {
			return fallback
		}
	}
	return runtime.GOARCH
}

// Plausible range for a temperature reading. Sensors that are absent or
// unwired commonly report -273 °C, 0 or 255 °C; those are left out.
const (
	minTempC = -40.0
	maxTempC = 150.0
)

// readTemps collects every hwmon temperature as "<chip> <label>" in °C,
// rounded to 0.1. Duplicate names (two NVMe drives) get " #2", " #3".
func readTemps(root string) []Temp {
	temps := []Temp{}
	chips, err := os.ReadDir(root)
	if err != nil {
		return temps
	}
	names := make([]string, 0, len(chips))
	for _, c := range chips {
		names = append(names, c.Name())
	}
	sortNatural(names)
	for _, chip := range names {
		dir := filepath.Join(root, chip)
		chipName := readFirstLine(filepath.Join(dir, "name"))
		if chipName == "" {
			chipName = chip
		}
		inputs, _ := filepath.Glob(filepath.Join(dir, "temp*_input"))
		sortNatural(inputs)
		for _, in := range inputs {
			raw := readFirstLine(in)
			milli, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				continue
			}
			c := float64(milli) / 1000
			if c <= minTempC || c >= maxTempC {
				continue
			}
			name := chipName
			if label := readFirstLine(strings.TrimSuffix(in, "_input") + "_label"); label != "" {
				name += " " + label
			} else if len(inputs) > 1 {
				name += " " + strings.TrimSuffix(filepath.Base(in), "_input")
			}
			temps = append(temps, Temp{Name: name, C: math.Round(c*10) / 10})
		}
	}
	seen := map[string]int{}
	for i := range temps {
		n := temps[i].Name
		seen[n]++
		if k := seen[n]; k > 1 {
			temps[i].Name = n + " #" + strconv.Itoa(k)
		}
	}
	return temps
}

func readFirstLine(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(b), "\n")
	return strings.TrimSpace(line)
}

// sortNatural orders strings so embedded numbers compare by value
// (hwmon2 < hwmon10, temp2_input < temp10_input).
func sortNatural(s []string) {
	sort.SliceStable(s, func(i, j int) bool { return naturalLess(s[i], s[j]) })
}

func naturalLess(a, b string) bool {
	for a != "" && b != "" {
		ad, bd := isDigit(a[0]), isDigit(b[0])
		if ad && bd {
			na, ra := leadingDigits(a)
			nb, rb := leadingDigits(b)
			ta, tb := strings.TrimLeft(na, "0"), strings.TrimLeft(nb, "0")
			if len(ta) != len(tb) {
				return len(ta) < len(tb)
			}
			if ta != tb {
				return ta < tb
			}
			a, b = ra, rb
			continue
		}
		if a[0] != b[0] {
			return a[0] < b[0]
		}
		a, b = a[1:], b[1:]
	}
	return len(a) < len(b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func leadingDigits(s string) (digits, rest string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	return s[:i], s[i:]
}
