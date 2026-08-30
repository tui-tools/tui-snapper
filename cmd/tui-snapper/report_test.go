package main

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/config"
)

// baseConfig is the configuration a run starts from, with nothing read off
// this machine: the declared defaults and nothing else.
func baseConfig() config.Config {
	return config.Config{Tool: toolName, Values: defaults()}
}

// TestRunReportDemo checks the half of the block this tool owns. The kit's own
// tests cover the machine facts and the scrubbing; what has to be right here is
// that --demo says demo, that it names snapper as the thing the fake imitates,
// and that the settings which decide every command line are on the block.
func TestRunReportDemo(t *testing.T) {
	var out strings.Builder
	if err := runReport(baseConfig(), options{demo: true, report: true}, &out); err != nil {
		t.Fatalf("runReport: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"backend: demo\n",
		"mode: demo (sample data, the system was not read)\n",
		"demo backend: snapper\n",
		"snapper config: (first on the machine)\n",
		"no-dbus: no\n",
		"boot config: /boot/limine.conf\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, toolName+" ") {
		t.Errorf("report should start with the tool name:\n%s", got)
	}
}

// TestRunReportSettings checks that the lines a reproduction depends on follow
// the configuration rather than the defaults, since a bug filed against a
// machine with no snapperd is a different bug.
func TestRunReportSettings(t *testing.T) {
	cfg := baseConfig()
	cfg.Set(keyConfig, "data")
	cfg.Set(keyNoDBus, "true")
	cfg.Set(keyBootConfig, "/boot/other/limine.conf")

	var out strings.Builder
	if err := runReport(cfg, options{demo: true, report: true}, &out); err != nil {
		t.Fatalf("runReport: %v", err)
	}

	got := out.String()
	for _, want := range []string{
		"snapper config: data\n",
		"no-dbus: yes\n",
		"boot config: /boot/other/limine.conf\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("report is missing %q:\n%s", want, got)
		}
	}
}

// TestRunReportNeverNamesTheUser is the privacy promise in test form: the block
// is pasted into a public issue, so neither the user's name nor a path under
// their home directory may reach it, whatever the configuration says.
func TestRunReportNeverNamesTheUser(t *testing.T) {
	t.Setenv("HOSTNAME", "workstation")
	t.Setenv("USER", "alice")
	t.Setenv("HOME", "/home/alice")

	cfg := baseConfig()
	cfg.Set(keyBootConfig, "/home/alice/boot/limine.conf")

	var out strings.Builder
	if err := runReport(cfg, options{demo: true, report: true}, &out); err != nil {
		t.Fatalf("runReport: %v", err)
	}

	got := out.String()
	for _, forbidden := range []string{"alice", "workstation", "/home/"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("the report leaked %q:\n%s", forbidden, got)
		}
	}
	if !strings.Contains(got, "boot config: (elsewhere)\n") {
		t.Errorf("a boot config under a home directory must be replaced:\n%s", got)
	}
}

// TestScrubPath is that rule in table form, for the one path this tool adds to
// the block itself.
func TestScrubPath(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"the default is printed", "/boot/limine.conf", "/boot/limine.conf"},
		{"so is any other system path", "/mnt/boot/limine.conf", "/mnt/boot/limine.conf"},
		{"a home path is replaced", "/home/alice/limine.conf", "(elsewhere)"},
		{"including the directory itself", "/home", "(elsewhere)"},
		{"root's home too", "/root/limine.conf", "(elsewhere)"},
		{"a lookalike outside home is kept", "/homebrew/limine.conf", "/homebrew/limine.conf"},
		{"an unset path stays unset", "  ", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := scrubPath(tc.path); got != tc.want {
				t.Errorf("scrubPath(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}
