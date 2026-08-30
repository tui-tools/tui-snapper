package snapper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The parsers in this package are the one place where output tui-snapper did
// not write becomes what the screen shows and what a command is then built
// around: a snapshot number to roll back to, a path to undo a change on, the
// state of a timer. `go test` runs the seeds below on every commit, and
// `go test -fuzz=FuzzParseSnapshots ./internal/snapper` explores past them
// locally — see tui-kit/templates/FUZZING.md for the family rule.
//
// The seeds are the captured fixtures the table tests replay, so the corpus
// starts on real snapper and limine output and mutates from there instead of
// guessing the shapes.

// seed adds every named testdata file to the corpus, plus the shapes a real
// capture never has: nothing, blank lines, a truncated document.
func seed(f *testing.F, names ...string) {
	f.Helper()
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join("testdata", name)) //nolint:gosec // the name is a literal in the tests, and testdata is in the repository
		if err != nil {
			f.Fatalf("read fixture %s: %v", name, err)
		}
		f.Add(string(raw))
	}
	f.Add("")
	f.Add("\n\n\n")
	f.Add("{}")
	f.Add("{\"root\":[{}]}")
	f.Add("/")
}

// oneLine asserts that a field the UI puts in a row is a single line: the
// tables are drawn a line at a time, so an embedded newline would break the
// screen rather than one cell.
func oneLine(t *testing.T, what, value string) {
	t.Helper()
	if strings.Contains(value, "\n") {
		t.Fatalf("%s spans lines: %q", what, value)
	}
}

// FuzzParseConfigs asserts the family's error rule as well as the shape: a
// config name is what every later command is addressed to (`snapper -c
// <name>`), so a blank one must never reach the list.
func FuzzParseConfigs(f *testing.F) {
	seed(f, "list-configs.json")
	f.Fuzz(func(t *testing.T, out string) {
		configs, err := ParseConfigs(out)
		if err != nil {
			if configs != nil {
				t.Fatalf("error returned with configs: %+v", configs)
			}
			return
		}
		for _, c := range configs {
			if c.Name == "" {
				t.Fatalf("config with no name: %+v", c)
			}
			oneLine(t, "config name", c.Name)
			oneLine(t, "subvolume", c.Subvolume)
		}
	})
}

func FuzzParseSnapshots(f *testing.F) {
	seed(f, "list.json", "list-quota.json")
	f.Fuzz(func(t *testing.T, out string) {
		snapshots, err := ParseSnapshots(out)
		if err != nil {
			if snapshots != nil {
				t.Fatalf("error returned with snapshots: %+v", snapshots)
			}
			return
		}
		for i, s := range snapshots {
			// The order the screen relies on: the live subvolume first, then
			// the snapshots newest number down.
			if i > 0 {
				prev := snapshots[i-1]
				if s.Current() && !prev.Current() {
					t.Fatalf("the live subvolume sorted below a snapshot at %d", i)
				}
				if prev.Current() == s.Current() && prev.Number < s.Number {
					t.Fatalf("numbers out of order: %d before %d", prev.Number, s.Number)
				}
			}
			// A used space that is neither a real size nor the "quotas are
			// off" marker would be shown as a size, and the header would add
			// it into a total.
			if s.UsedSpace < 0 && s.UsedSpace != UsedSpaceUnknown {
				t.Fatalf("used space is neither a size nor unknown: %d", s.UsedSpace)
			}
			oneLine(t, "description", s.Description)
			oneLine(t, "cleanup", s.Cleanup)
			oneLine(t, "type", s.Type)
			// A date that could not be read leaves RawDate to show instead,
			// so the row is never blank where snapper said something.
			if s.Date.IsZero() && s.RawDate != "" {
				oneLine(t, "raw date", s.RawDate)
			}
		}
		// The header facts are computed from whatever came back, on every
		// input, so they belong inside the target too.
		_, _ = TotalUsedSpace(snapshots)
		_, _ = Oldest(snapshots)
		_, _ = Newest(snapshots)
	})
}

// FuzzParseStatus is the target that matters most: a path it returns is
// handed straight back to `snapper undochange`, so what comes out has to be a
// path the input really carried.
func FuzzParseStatus(f *testing.F) {
	seed(f, "status.txt")
	f.Fuzz(func(t *testing.T, out string) {
		for _, c := range ParseStatus(out) {
			if c.Path == "" {
				t.Fatal("a change with no path")
			}
			oneLine(t, "path", c.Path)
			if !strings.Contains(out, c.Path) {
				t.Fatalf("path %q is not in the input", c.Path)
			}
			switch c.Kind {
			case Created, Deleted, Modified, TypeChanged:
			default:
				t.Fatalf("unknown change kind %q", c.Kind)
			}
			if len(c.Status) != statusColumns {
				t.Fatalf("status field is not %d columns: %q", statusColumns, c.Status)
			}
		}
	})
}

func FuzzParseTimerState(f *testing.F) {
	f.Add("ActiveState=active\nUnitFileState=enabled\n")
	f.Add("ActiveState=inactive\nUnitFileState=disabled")
	f.Add("ActiveState=\nUnitFileState=")
	f.Add("")
	f.Add("=")
	f.Fuzz(func(t *testing.T, out string) {
		state := ParseTimerState("snapper-timeline.timer", out)
		if state.Unit != "snapper-timeline.timer" {
			t.Fatalf("the unit asked about was not the unit answered for: %q", state.Unit)
		}
		oneLine(t, "active state", state.Active)
		oneLine(t, "enabled state", state.Enabled)
		// A row with nothing in it says why, rather than showing two blanks.
		if state.Active == "" && state.Enabled == "" && state.Err == "" {
			t.Fatal("an empty state with no reason given")
		}
	})
}

func FuzzVersionFlags(f *testing.F) {
	f.Add("snapper 0.13.1\n\nflags btrfs,lvm,ext4,xattrs,rollback,btrfs-quota,no-selinux\n")
	f.Add("flags ")
	f.Add("flags ,,,")
	f.Add("")
	f.Fuzz(func(t *testing.T, out string) {
		for _, flag := range versionFlags(out) {
			// Each flag is compared against a feature name, so a blank one or
			// one that still carries the separator would never match.
			if flag == "" {
				t.Fatal("blank feature flag")
			}
			if strings.ContainsAny(flag, ",\n") || strings.TrimSpace(flag) != flag {
				t.Fatalf("feature flag kept the list syntax around it: %q", flag)
			}
			if !strings.Contains(out, flag) {
				t.Fatalf("feature flag %q is not in the input", flag)
			}
		}
	})
}

// FuzzParseBootEntries reads a file limine-snapper-sync owns. tui-snapper
// never writes it, but the number it reads out of an entry is the number a
// rollback would name, so it has to come from the file rather than from a
// half-matched title.
func FuzzParseBootEntries(f *testing.F) {
	seed(f, "limine-omarchy-server.conf")
	f.Add("//Snapshots\n///4 | 2026-08-29 21:27:27\ncomment: a\n////boot\nprotocol: linux\n")
	f.Fuzz(func(t *testing.T, text string) {
		for _, e := range ParseBootEntries(text) {
			if e.Number < 0 {
				t.Fatalf("negative snapshot number: %d", e.Number)
			}
			oneLine(t, "title", e.Title)
			oneLine(t, "comment", e.Comment)
			if e.Title != "" && !strings.Contains(text, e.Title) {
				t.Fatalf("title %q is not in the input", e.Title)
			}
			if e.Comment != "" && !strings.Contains(text, e.Comment) {
				t.Fatalf("comment %q is not in the input", e.Comment)
			}
		}
	})
}
