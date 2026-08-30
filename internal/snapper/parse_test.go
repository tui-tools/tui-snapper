package snapper

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The samples in testdata were captured from a real snapper 0.13.1
// (libsnapper 8.0.0) driving a real btrfs filesystem on a loop device, inside
// an Arch container, with `--no-dbus`. They are pasted in verbatim: when a
// parser is wrong on someone's machine, their output becomes the next case.

// golden reads one captured sample.
func golden(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func TestParseConfigs(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    []Config
		wantErr bool
	}{
		{
			name:  "the captured list-configs",
			input: golden(t, "list-configs.json"),
			want:  []Config{{Name: "data", Subvolume: "/srv/root"}},
		},
		{
			name: "a machine with the usual two configs",
			input: `{"configs":[{"config":"root","subvolume":"/"},` +
				`{"config":"home","subvolume":"/home"}]}`,
			want: []Config{{Name: "root", Subvolume: "/"}, {Name: "home", Subvolume: "/home"}},
		},
		{
			name:  "no configs is an empty list, not an error",
			input: `{"configs":[]}`,
			want:  []Config{},
		},
		{name: "empty output is an error", input: "  \n ", wantErr: true},
		{name: "a snapper error message is not JSON", input: "Config 'nope' not found.", wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseConfigs(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseConfigs: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseSnapshots(t *testing.T) {
	got, err := ParseSnapshots(golden(t, "list.json"))
	if err != nil {
		t.Fatalf("ParseSnapshots: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("got %d snapshots, want 6", len(got))
	}

	// Newest first, with the live subvolume kept on top of it.
	wantOrder := []int{0, 5, 4, 3, 2, 1}
	for i, want := range wantOrder {
		if got[i].Number != want {
			t.Errorf("row %d is snapshot %d, want %d", i, got[i].Number, want)
		}
	}

	// Row 0 is snapper's placeholder for the live subvolume: no date, and it
	// must never be mistaken for a snapshot.
	live := got[0]
	if !live.Current() {
		t.Error("snapshot 0 should report itself as the live subvolume")
	}
	if !live.Date.IsZero() || live.RawDate != "" {
		t.Errorf("snapshot 0 should carry no date, got %q", live.RawDate)
	}

	// The post snapshot carries the pre it belongs to; every other row's
	// null pre-number must read as "none" rather than as snapshot 0.
	post := got[3]
	if post.Number != 3 || post.Type != TypePost || post.PreNumber != 2 {
		t.Errorf("post snapshot = %+v, want number 3 of type post paired with 2", post)
	}
	for _, s := range got {
		if s.Number != 3 && s.PreNumber != 0 {
			t.Errorf("snapshot %d has pre-number %d, want none", s.Number, s.PreNumber)
		}
	}

	// The userdata map, which snapper writes as an object and as null.
	withData := got[2]
	if withData.Number != 4 {
		t.Fatalf("row 2 is snapshot %d, want 4", withData.Number)
	}
	wantData := map[string]string{"important": "yes", "reason": "manual"}
	if !reflect.DeepEqual(withData.Userdata, wantData) {
		t.Errorf("userdata = %+v, want %+v", withData.Userdata, wantData)
	}
	if got, want := withData.UserdataString(), "important=yes, reason=manual"; got != want {
		t.Errorf("UserdataString = %q, want %q", got, want)
	}

	// Every row of this capture was taken without quotas, so used-space is
	// null everywhere and must not read as zero bytes.
	for _, s := range got {
		if s.UsedSpace != UsedSpaceUnknown {
			t.Errorf("snapshot %d used-space = %d, want unknown", s.Number, s.UsedSpace)
		}
	}
	if _, known := TotalUsedSpace(got); known {
		t.Error("a total should not be reported when quotas are off")
	}

	// The date is parsed from snapper's own format.
	want := time.Date(2026, 8, 29, 23, 39, 29, 0, time.Local)
	if !got[2].Date.Equal(want) {
		t.Errorf("date = %v, want %v", got[2].Date, want)
	}

	// Cleanup, description and user come straight through.
	if got[5].Description != "initial state" || got[5].Cleanup != "number" {
		t.Errorf("oldest snapshot = %+v", got[5])
	}
	// Pinned is what tells a kept snapshot from one a timer will remove.
	// Snapshot 5 was created with no cleanup algorithm; the live subvolume is
	// never pinned, whatever its empty cleanup field says.
	if got[0].Pinned() {
		t.Error("the live subvolume is not a pinned snapshot")
	}
	if !got[1].Pinned() {
		t.Errorf("snapshot %d has no cleanup algorithm, so it is pinned", got[1].Number)
	}
	if got[3].Pinned() {
		t.Errorf("snapshot %d is bound to %q, so it is not pinned",
			got[3].Number, got[3].Cleanup)
	}
}

func TestParseSnapshotsWithQuotas(t *testing.T) {
	// The same command on a config with `snapper setup-quota` run: snapper
	// fills used-space in, and the header total becomes a real figure.
	got, err := ParseSnapshots(golden(t, "list-quota.json"))
	if err != nil {
		t.Fatalf("ParseSnapshots: %v", err)
	}
	total, known := TotalUsedSpace(got)
	if !known {
		t.Fatal("used-space is present in this capture, so a total should be known")
	}
	if want := int64(3 * 16384); total != want {
		t.Errorf("total = %d, want %d", total, want)
	}
	for _, s := range got {
		if s.Current() {
			continue
		}
		if s.UsedSpace != 16384 {
			t.Errorf("snapshot %d used-space = %d, want 16384", s.Number, s.UsedSpace)
		}
	}
}

func TestParseSnapshotsRejectsJunk(t *testing.T) {
	tests := []struct{ name, input string }{
		{"empty", "   "},
		{"a snapper error", "Config 'nope' not found."},
		{"more than one config", `{"a":[],"b":[]}`},
		{"an array instead of an object", `[{"number":1}]`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseSnapshots(tc.input); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

func TestParseStatus(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []Change
	}{
		{
			name:  "the captured pre/post comparison",
			input: golden(t, "status.txt"),
			want: []Change{
				{Kind: Modified, Path: "/srv/root/etc/conf", Status: "c....."},
				{Kind: Deleted, Path: "/srv/root/etc/doomed.conf", Status: "-....."},
				{Kind: Created, Path: "/srv/root/etc/new.conf", Status: "+....."},
			},
		},
		{
			name: "metadata-only changes and a type change",
			input: ".p.... /etc/ssh/sshd_config\n" +
				"..ug.. /var/lib/thing\n" +
				"t..... /usr/bin/vi\n" +
				"c...x. /etc/sudoers\n",
			want: []Change{
				{Kind: Modified, Path: "/etc/ssh/sshd_config", Status: ".p....", Metadata: true},
				{Kind: Modified, Path: "/var/lib/thing", Status: "..ug..", Metadata: true},
				{Kind: TypeChanged, Path: "/usr/bin/vi", Status: "t....."},
				{Kind: Modified, Path: "/etc/sudoers", Status: "c...x.", Metadata: true},
			},
		},
		{
			name:  "a path containing spaces keeps them",
			input: "c..... /home/you/My Documents/notes.txt\n",
			want: []Change{
				{Kind: Modified, Path: "/home/you/My Documents/notes.txt", Status: "c....."},
			},
		},
		{
			name:  "nothing changed",
			input: "",
			want:  nil,
		},
		{
			name: "lines that are not status lines are skipped",
			input: "some warning snapper printed first\n" +
				"c..... /etc/conf\n" +
				"\n",
			want: []Change{{Kind: Modified, Path: "/etc/conf", Status: "c....."}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseStatus(tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestVersionFlags(t *testing.T) {
	// Captured verbatim from `snapper --version` on the container.
	out := "snapper 0.13.1\nlibsnapper 8.0.0\n" +
		"flags btrfs,bcachefs,lvm,ext4,xattrs,rollback,btrfs-quota,no-selinux"
	flags := versionFlags(out)
	want := []string{"btrfs", "bcachefs", "lvm", "ext4", "xattrs",
		"rollback", "btrfs-quota", "no-selinux"}
	if !reflect.DeepEqual(flags, want) {
		t.Fatalf("flags = %q, want %q", flags, want)
	}
	if !(Platform{SnapperFlags: flags}).HasRollbackFlag() {
		t.Error("this build reports the rollback flag")
	}
	if (Platform{SnapperFlags: []string{"btrfs", "lvm"}}).HasRollbackFlag() {
		t.Error("a build without the flag should not claim rollback support")
	}
	if versionFlags("snapper 0.13.1") != nil {
		t.Error("a version output with no flags line should report none")
	}
}

func TestParseTimerState(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  TimerState
	}{
		{
			name:  "an enabled timer",
			input: "ActiveState=active\nUnitFileState=enabled\n",
			want: TimerState{Unit: "snapper-timeline.timer",
				Active: "active", Enabled: "enabled"},
		},
		{
			name:  "a masked timer",
			input: "ActiveState=inactive\nUnitFileState=masked\n",
			want: TimerState{Unit: "snapper-timeline.timer",
				Active: "inactive", Enabled: "masked"},
		},
		{
			name:  "a unit systemd never heard of",
			input: "ActiveState=\nUnitFileState=\n",
			want: TimerState{Unit: "snapper-timeline.timer",
				Err: "systemd does not know this unit"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseTimerState("snapper-timeline.timer", tc.input)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{16384, "16.0 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1_482_119_680, "1.4 GiB"},
	}
	for _, tc := range tests {
		if got := FormatBytes(tc.in); got != tc.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSortAndEdges(t *testing.T) {
	snapshots, err := ParseSnapshots(golden(t, "list.json"))
	if err != nil {
		t.Fatalf("ParseSnapshots: %v", err)
	}
	oldest, ok := Oldest(snapshots)
	if !ok || oldest.Number != 1 {
		t.Errorf("oldest = %+v, want snapshot 1", oldest)
	}
	newest, ok := Newest(snapshots)
	if !ok || newest.Number != 5 {
		t.Errorf("newest = %+v, want snapshot 5", newest)
	}
	// A list holding only the live subvolume has no real ends.
	if _, ok := Oldest([]Snapshot{{Number: 0}}); ok {
		t.Error("the live subvolume is not the oldest snapshot")
	}
}
