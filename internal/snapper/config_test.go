package snapper

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests cover the config-authoring half of the tool: what a form is
// allowed to send to snapper, and what BuildCommand does with it. The
// refusals matter as much as the successes — a config name reaches snapper as
// an argv element, so nothing can be injected through it, but a name with a
// slash or a path with a ".." in it fails long after the user confirmed, and
// this is where that is caught instead.

func TestValidateConfigNameRefusesAnythingButAName(t *testing.T) {
	for _, name := range []string{"root", "home", "srv-data", "var-log", "x", "a.b_c-1"} {
		if err := ValidateConfigName(name); err != nil {
			t.Errorf("ValidateConfigName(%q) = %v, want it accepted", name, err)
		}
	}

	refused := []string{
		"",
		".",
		"..",
		"../../etc/snapper",
		"home/../root",
		"root; rm -rf /",
		"root && reboot",
		"$(id)",
		"`id`",
		"root\nhome",
		"-c",
		"--jsonout",
		".hidden",
		"has space",
		strings.Repeat("a", maxConfigNameLen+1),
	}
	for _, name := range refused {
		if err := ValidateConfigName(name); err == nil {
			t.Errorf("ValidateConfigName(%q) was accepted", name)
		}
	}
}

func TestValidateSubvolumePathRefusesAnythingButACleanAbsolutePath(t *testing.T) {
	for _, path := range []string{"/", "/home", "/srv/data", "/var/log"} {
		if err := ValidateSubvolumePath(path); err != nil {
			t.Errorf("ValidateSubvolumePath(%q) = %v, want it accepted", path, err)
		}
	}
	refused := []string{
		"",
		"home",
		"./home",
		"/home/",
		"/home/../etc",
		"/home//data",
		"/home\nrm -rf /",
		"/home\x00",
		"-/home",
	}
	for _, path := range refused {
		if err := ValidateSubvolumePath(path); err == nil {
			t.Errorf("ValidateSubvolumePath(%q) was accepted", path)
		}
	}
}

func TestValidateSettingValue(t *testing.T) {
	number, ok := SettingFor(FieldNumberLimit)
	if !ok {
		t.Fatal("NUMBER_LIMIT is not in the settings table")
	}
	for _, value := range []string{"0", "50", "10-50"} {
		if err := ValidateSettingValue(number, value); err != nil {
			t.Errorf("NUMBER_LIMIT %q = %v, want it accepted", value, err)
		}
	}
	for _, value := range []string{"", "-1", "ten", "50;reboot", "1-", "1-x", "1.5"} {
		if err := ValidateSettingValue(number, value); err == nil {
			t.Errorf("NUMBER_LIMIT %q was accepted", value)
		}
	}

	timeline, ok := SettingFor(FieldTimelineCreate)
	if !ok {
		t.Fatal("TIMELINE_CREATE is not in the settings table")
	}
	for _, value := range []string{"yes", "no"} {
		if err := ValidateSettingValue(timeline, value); err != nil {
			t.Errorf("TIMELINE_CREATE %q = %v, want it accepted", value, err)
		}
	}
	for _, value := range []string{"", "true", "YES", "yes no"} {
		if err := ValidateSettingValue(timeline, value); err == nil {
			t.Errorf("TIMELINE_CREATE %q was accepted", value)
		}
	}
}

func TestCreateConfigCommand(t *testing.T) {
	spec, ok := ConfigSpec(CreateConfig)
	if !ok {
		t.Fatal("create-config is not in the config action table")
	}
	cmd, err := BuildCommand(spec, Request{
		Config: "srv",
		Values: map[FieldKind]string{FieldSubvolume: "/srv"},
	})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	want := []string{"snapper", "-c", "srv", "create-config", "-f", "btrfs", "/srv"}
	if strings.Join(cmd.Argv, " ") != strings.Join(want, " ") {
		t.Errorf("argv = %v, want %v", cmd.Argv, want)
	}

	// A name or a path the form would have refused must not get through here
	// either: BuildCommand is the last gate before the argv exists at all.
	for _, req := range []Request{
		{Config: "srv; reboot", Values: map[FieldKind]string{FieldSubvolume: "/srv"}},
		{Config: "srv", Values: map[FieldKind]string{FieldSubvolume: "/srv/../etc"}},
		{Config: "srv", Values: map[FieldKind]string{FieldSubvolume: "srv"}},
		{Config: "srv", Values: map[FieldKind]string{}},
	} {
		if _, err := BuildCommand(spec, req); err == nil {
			t.Errorf("BuildCommand accepted %+v", req)
		}
	}
}

func TestSetConfigWritesOnlyTheChangedKeys(t *testing.T) {
	spec, ok := ConfigSpec(SetConfig)
	if !ok {
		t.Fatal("set-config is not in the config action table")
	}
	current := map[FieldKind]string{
		FieldNumberLimit:    "50",
		FieldTimelineCreate: "yes",
		FieldTimelineHourly: "10",
	}
	values := map[FieldKind]string{
		FieldNumberLimit:    "50", // unchanged
		FieldTimelineCreate: "no", // changed
		FieldTimelineHourly: "24", // changed
	}
	cmd, err := BuildCommand(spec, Request{Config: "home", Values: values, Current: current})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	want := "snapper -c home set-config TIMELINE_CREATE=no TIMELINE_LIMIT_HOURLY=24"
	if got := strings.Join(cmd.Argv, " "); got != want {
		t.Errorf("argv = %q, want %q", got, want)
	}

	// A form nobody changed builds nothing rather than a command that would
	// rewrite the same values back.
	if _, err := BuildCommand(spec, Request{
		Config: "home", Values: current, Current: current,
	}); err == nil {
		t.Error("an unchanged form built a command")
	}

	// A value the picker could not have produced is refused before it can be
	// written into a config file.
	if _, err := BuildCommand(spec, Request{
		Config: "home",
		Values: map[FieldKind]string{FieldTimelineCreate: "yes\nALLOW_USERS=mallory"},
	}); err == nil {
		t.Error("a smuggled second key was accepted")
	}
	if _, err := BuildCommand(spec, Request{
		Config: "home",
		Values: map[FieldKind]string{FieldNumberLimit: "50 ALLOW_GROUPS=wheel"},
	}); err == nil {
		t.Error("a smuggled second key was accepted")
	}
}

func TestDeleteConfigRefusesRoot(t *testing.T) {
	spec, ok := ConfigSpec(DeleteConfig)
	if !ok {
		t.Fatal("delete-config is not in the config action table")
	}
	if _, err := BuildCommand(spec, Request{Config: ProtectedConfig}); err == nil {
		t.Fatalf("BuildCommand deleted the %q config", ProtectedConfig)
	}
	cmd, err := BuildCommand(spec, Request{Config: "home"})
	if err != nil {
		t.Fatalf("BuildCommand: %v", err)
	}
	if got := strings.Join(cmd.Argv, " "); got != "snapper -c home delete-config" {
		t.Errorf("argv = %q", got)
	}
	if !cmd.Destructive {
		t.Error("deleting a config is not marked destructive")
	}
}

func TestParseConfigSettingsReadsTheTable(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "get-config.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	settings := ParseConfigSettings(string(raw))
	for key, want := range map[string]string{
		"NUMBER_LIMIT":           "50",
		"NUMBER_LIMIT_IMPORTANT": "10",
		"TIMELINE_CREATE":        "yes",
		"TIMELINE_LIMIT_HOURLY":  "10",
		"TIMELINE_LIMIT_YEARLY":  "2",
		"EMPTY_PRE_POST_CLEANUP": "yes",
		"ALLOW_GROUPS":           "",
	} {
		if got := settings[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	// The heading and the rule under it are not settings.
	for _, key := range []string{"Key", "", "-----------------------"} {
		if _, ok := settings[key]; ok {
			t.Errorf("%q was read as a setting", key)
		}
	}
	// Every key the form writes has to be readable out of a real capture, or
	// the form would seed itself with blanks.
	for _, setting := range EditableSettings {
		if _, ok := settings[setting.Key]; !ok {
			t.Errorf("%s is missing from the captured get-config", setting.Key)
		}
	}
}

func TestParseMountInfoAndFilesystemOf(t *testing.T) {
	const table = `21 30 0:19 / /proc rw,nosuid - proc proc rw
25 30 0:22 / /sys rw,nosuid - sysfs sysfs rw
30 1 0:29 /@ / rw,relatime shared:1 - btrfs /dev/nvme0n1p2 rw,ssd,subvol=/@
44 30 0:29 /@home /home rw,relatime shared:2 - btrfs /dev/nvme0n1p2 rw,subvol=/@home
52 30 259:1 / /boot rw,relatime - vfat /dev/nvme0n1p1 rw
60 30 0:29 /@srv /srv/my\040data rw,relatime - btrfs /dev/nvme0n1p2 rw`
	mounts := ParseMountInfo(table)
	if len(mounts) != 6 {
		t.Fatalf("parsed %d mounts, want 6: %+v", len(mounts), mounts)
	}
	// The kernel escapes a space in a mount point, and a path with one in it
	// is still a path a user can ask for a config on.
	if mounts[5].Point != "/srv/my data" || mounts[5].FSType != BtrfsFSType {
		t.Errorf("escaped mount point = %+v", mounts[5])
	}

	for path, want := range map[string]string{
		"/":                   BtrfsFSType,
		"/home":               BtrfsFSType,
		"/home/user/projects": BtrfsFSType,
		"/boot":               "vfat",
		"/boot/efi":           "vfat",
		"/proc/self":          "proc",
	} {
		// A btrfs subvolume is usually not a mount point of its own, so the
		// answer has to come from the longest mount point above the path.
		got, found := FilesystemOf(path, mounts)
		if !found || got != want {
			t.Errorf("FilesystemOf(%q) = %q %v, want %q", path, got, found, want)
		}
	}
}

// TestCheckSubvolumeOnThisMachine runs the real Inspector against a temporary
// directory. Whether that directory is a subvolume depends on the machine
// running the test, so this only pins the refusals that do not: a missing
// path, a file, a relative path and the wrong filesystem.
func TestCheckSubvolumeOnThisMachine(t *testing.T) {
	dir := t.TempDir()
	mounts := []Mount{{Point: "/", FSType: "ext4"}, {Point: dir, FSType: BtrfsFSType}}

	// A path on a filesystem snapper cannot snapshot is refused with the
	// filesystem named, which is the fact the user needs.
	if err := CheckSubvolume("/", mounts); err == nil {
		t.Error("a non-btrfs path was accepted")
	} else if !strings.Contains(err.Error(), "ext4") {
		t.Errorf("the refusal does not name the filesystem: %v", err)
	}
	if err := CheckSubvolume(filepath.Join(dir, "nope"), mounts); err == nil {
		t.Error("a path that does not exist was accepted")
	}

	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := CheckSubvolume(file, mounts); err == nil {
		t.Error("a plain file was accepted as a subvolume")
	}
	if err := CheckSubvolume("relative", mounts); err == nil {
		t.Error("a relative path was accepted")
	}

	// A directory the test made is never a subvolume root, whatever the
	// filesystem, so the real Inspector has to refuse it as one.
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var refusal *SubvolumeRefusal
	if err := CheckSubvolume(sub, mounts); !errors.As(err, &refusal) {
		t.Errorf("a plain directory was not refused as one: %v", err)
	}
}

// fakeFS is an Inspector over a map, for the shapes a test machine cannot
// produce: subvolume roots, a mounted .snapshots.
func fakeFS(paths map[string]PathEntry) Inspector {
	return func(path string, _ bool) (PathEntry, error) {
		if entry, ok := paths[path]; ok {
			return entry, nil
		}
		return PathEntry{}, fs.ErrNotExist
	}
}

// TestCheckSubvolume covers tui-tools/tui-snapper#18: the two paths snapper
// itself refuses after the confirm are refused before the preview, each with
// a reason and the way out, and a subvolume with no .snapshots goes through.
func TestCheckSubvolume(t *testing.T) {
	subvolume := PathEntry{IsDir: true, Inode: SubvolumeRootInode}
	emptySubvolume := PathEntry{IsDir: true, Inode: SubvolumeRootInode, Empty: true}
	mounts := []Mount{
		{Point: "/", FSType: BtrfsFSType},
		{Point: "/.snapshots", FSType: BtrfsFSType},
		{Point: "/boot", FSType: "vfat"},
	}
	cases := []struct {
		name  string
		path  string
		paths map[string]PathEntry
		// wantOK is a path that goes through; otherwise the refusal must be
		// a SubvolumeRefusal whose reason and advice hold these.
		wantOK     bool
		wantReason string
		wantAdvice []string
		notAdvice  []string
	}{
		{
			name:   "a subvolume with no .snapshots",
			path:   "/srv/data/vol",
			paths:  map[string]PathEntry{"/srv/data/vol": subvolume},
			wantOK: true,
		},
		{
			name:       "a directory that is not a subvolume",
			path:       "/srv/data/notsv",
			paths:      map[string]PathEntry{"/srv/data/notsv": {IsDir: true, Inode: 4711}},
			wantReason: "not a subvolume",
			wantAdvice: []string{
				"  btrfs subvolume list /",
				"  btrfs subvolume create /srv/data/notsv.new",
			},
		},
		{
			name: "the archinstall / Omarchy layout: @snapshots mounted at /.snapshots",
			path: "/",
			paths: map[string]PathEntry{
				"/":           subvolume,
				"/.snapshots": subvolume,
			},
			wantReason: "/.snapshots is already a mounted subvolume",
			wantAdvice: []string{
				"archinstall",
				"  umount /.snapshots",
				"  rmdir /.snapshots",
				"  snapper -c root create-config -f btrfs /",
				"  btrfs subvolume delete /.snapshots",
				"  mkdir /.snapshots",
				"  mount -a",
			},
		},
		{
			name: "an empty leftover .snapshots subvolume",
			path: "/srv/data/vol",
			paths: map[string]PathEntry{
				"/srv/data/vol":            subvolume,
				"/srv/data/vol/.snapshots": emptySubvolume,
			},
			wantReason: "/srv/data/vol/.snapshots already exists",
			wantAdvice: []string{"  btrfs subvolume delete /srv/data/vol/.snapshots"},
			notAdvice:  []string{"umount", "mv "},
		},
		{
			name: "a .snapshots subvolume that holds something",
			path: "/srv/data/vol",
			paths: map[string]PathEntry{
				"/srv/data/vol":            subvolume,
				"/srv/data/vol/.snapshots": subvolume,
			},
			wantReason: "already exists",
			wantAdvice: []string{
				"  mv /srv/data/vol/.snapshots /srv/data/vol/.snapshots.old",
			},
			// Snapshot history is never offered for deletion.
			notAdvice: []string{"delete", "rmdir", "rm "},
		},
		{
			name: "an empty .snapshots directory",
			path: "/srv/data/vol",
			paths: map[string]PathEntry{
				"/srv/data/vol":            subvolume,
				"/srv/data/vol/.snapshots": {IsDir: true, Inode: 999, Empty: true},
			},
			wantReason: "already exists",
			wantAdvice: []string{"  rmdir /srv/data/vol/.snapshots"},
		},
		{
			name: "a .snapshots that is a file",
			path: "/srv/data/vol",
			paths: map[string]PathEntry{
				"/srv/data/vol":            subvolume,
				"/srv/data/vol/.snapshots": {Inode: 999},
			},
			wantReason: "already exists",
			wantAdvice: []string{"not a directory", "  mv /srv/data/vol/.snapshots"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSubvolume(tc.path, mounts, fakeFS(tc.paths))
			if tc.wantOK {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			var refusal *SubvolumeRefusal
			if !errors.As(err, &refusal) {
				t.Fatalf("err = %v, want a SubvolumeRefusal", err)
			}
			if !strings.Contains(refusal.Reason, tc.wantReason) {
				t.Errorf("reason = %q, want it to hold %q", refusal.Reason, tc.wantReason)
			}
			if strings.Contains(refusal.Reason, "\n") {
				t.Errorf("the reason is for a status line, and has a newline: %q",
					refusal.Reason)
			}
			for _, want := range tc.wantAdvice {
				if !strings.Contains(refusal.Advice, want) {
					t.Errorf("the advice is missing %q:\n%s", want, refusal.Advice)
				}
			}
			for _, not := range tc.notAdvice {
				if strings.Contains(refusal.Advice, not) {
					t.Errorf("the advice holds %q:\n%s", not, refusal.Advice)
				}
			}
		})
	}

	// The earlier refusals keep their plain wording.
	if err := checkSubvolume("/boot", mounts, fakeFS(map[string]PathEntry{
		"/boot": subvolume,
	})); err == nil || !strings.Contains(err.Error(), "vfat") {
		t.Errorf("a vfat path: %v", err)
	}
	if err := checkSubvolume("/nope", mounts, fakeFS(nil)); err == nil ||
		!strings.Contains(err.Error(), "does not exist") {
		t.Errorf("a missing path: %v", err)
	}
}

func TestSuggestConfigName(t *testing.T) {
	for path, want := range map[string]string{
		"/":         ProtectedConfig,
		"/home":     "home",
		"/var/log":  "var-log",
		"/srv/data": "srv-data",
		"/ ":        "",
	} {
		if got := SuggestConfigName(path); got != want {
			t.Errorf("SuggestConfigName(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestConfigActionKeysDoNotCollideWithTheSnapshotOnes(t *testing.T) {
	// The two tables drive two different screens, so their keys may repeat —
	// but a key must mean exactly one thing on each of them.
	seen := map[string]bool{}
	for _, spec := range ConfigActions {
		if seen[spec.Key] {
			t.Errorf("key %q is bound twice on the config screen", spec.Key)
		}
		seen[spec.Key] = true
		if spec.Label == "" || spec.Body == "" {
			t.Errorf("config action %q has no label or no body", spec.Action)
		}
	}
}
