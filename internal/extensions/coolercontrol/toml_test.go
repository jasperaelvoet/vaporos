package coolercontrol

import (
	"strings"
	"testing"
)

var (
	testForce    = []setting{{"trusted_proxies", `["127.0.0.1", "::1"]`}, {"port", "11986"}}
	testDefaults = []setting{{"poll_rate", "1.0"}, {"drivetemp_suspend", "true"}}
)

func TestPatchSettings(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty", "",
			"[settings]\ntrusted_proxies = [\"127.0.0.1\", \"::1\"]\nport = 11986\npoll_rate = 1.0\ndrivetemp_suspend = true\n"},
		{"no settings table", "# CoolerControl\n[devices]\nport = 5",
			"# CoolerControl\n[devices]\nport = 5\n\n[settings]\ntrusted_proxies = [\"127.0.0.1\", \"::1\"]\nport = 11986\npoll_rate = 1.0\ndrivetemp_suspend = true\n"},
		{"keys replaced, the user's kept, the rest added before the next table",
			"[settings]\n# keep me\nport = 11987 # theirs\npoll_rate = 0.5\nname = \"a [b] # c\"\n\n[settings.sub]\nport = 1\n",
			"[settings]\n# keep me\nport = 11986\npoll_rate = 0.5\nname = \"a [b] # c\"\ntrusted_proxies = [\"127.0.0.1\", \"::1\"]\ndrivetemp_suspend = true\n\n[settings.sub]\nport = 1\n"},
		{"a value over several lines",
			"[legacy690]\n[settings]\ntrusted_proxies = [\n  \"10.0.0.1\", # a LAN proxy\n  [\"nested\"],\n]\nport = 11986\npoll_rate = 1.0\ndrivetemp_suspend = false\n[devices]\nx = [\n[1]]\n",
			"[legacy690]\n[settings]\ntrusted_proxies = [\"127.0.0.1\", \"::1\"]\nport = 11986\npoll_rate = 1.0\ndrivetemp_suspend = false\n[devices]\nx = [\n[1]]\n"},
		{"a quoted key and a multi-line string",
			"[settings]\n\"port\" = 1\nnote = \"\"\"\n[not a table]\nport = 2\n\"\"\"\n",
			"[settings]\nport = 11986\nnote = \"\"\"\n[not a table]\nport = 2\n\"\"\"\ntrusted_proxies = [\"127.0.0.1\", \"::1\"]\npoll_rate = 1.0\ndrivetemp_suspend = true\n"},
		{"the last key at the end of the file, no newline",
			"[settings]\nport = 3",
			"[settings]\nport = 11986\ntrusted_proxies = [\"127.0.0.1\", \"::1\"]\npoll_rate = 1.0\ndrivetemp_suspend = true\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := patchSettings(tc.in, testForce, testDefaults)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tc.want)
			}
			again, err := patchSettings(got, testForce, testDefaults)
			if err != nil || again != got {
				t.Fatalf("not idempotent: %v\n%s", err, again)
			}
		})
	}
}

func TestPatchSettingsRefuses(t *testing.T) {
	for name, in := range map[string]string{
		"dotted keys":     "settings.port = 1\n",
		"an inline table": "settings = { port = 1 }\n",
		"two tables":      "[settings]\n[devices]\n[settings]\n",
		"a repeated key":  "[settings]\nport = 1\nport = 2\n",
		"an open array":   "[settings]\ntrusted_proxies = [\n\"a\",\n",
		"an open string":  "[settings]\nnote = '''\nnever closed\n",
	} {
		if out, err := patchSettings(in, testForce, testDefaults); err == nil {
			t.Errorf("%s: patched into\n%s", name, out)
		} else if !strings.Contains(err.Error(), "config.toml") {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestAddTables(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"empty", "", "[devices]\n\n[legacy690]\n\n[device-settings]\n"},
		{"only settings, no newline at the end", "[settings]\nport = 11986",
			"[settings]\nport = 11986\n\n[devices]\n\n[legacy690]\n\n[device-settings]\n"},
		{"all there, as headers, sub-tables and dotted keys",
			"legacy690.x = true\n[devices]\n[[profiles]]\n[device-settings.ab12]\npump = { speed_fixed = 30 }\n",
			"legacy690.x = true\n[devices]\n[[profiles]]\n[device-settings.ab12]\npump = { speed_fixed = 30 }\n"},
		{"headers inside values do not count",
			"[settings]\nnote = \"\"\"\n[devices]\n\"\"\"\nx = [\n[1]]\n\"legacy690\" = 1\n[\"device-settings\"]\n",
			"[settings]\nnote = \"\"\"\n[devices]\n\"\"\"\nx = [\n[1]]\n\"legacy690\" = 1\n[\"device-settings\"]\n\n[devices]\n\n[legacy690]\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := addTables(tc.in, requiredTables)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tc.want)
			}
			if again, err := addTables(got, requiredTables); err != nil || again != got {
				t.Fatalf("not idempotent: %v\n%s", err, again)
			}
		})
	}
	if out, err := addTables("[settings]\nnote = '''\nnever closed\n", requiredTables); err == nil {
		t.Fatalf("patched an open string into\n%s", out)
	}
}
