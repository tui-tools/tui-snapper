// Package snapper holds the model tui-snapper renders, the actions it can
// perform, and the interface every backend satisfies. The UI knows only these
// types: it never assembles a snapper command line itself. Mutations are
// runner.Command values produced by BuildCommand, shown in a preview dialog
// and only then executed.
package snapper

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config is one snapper configuration: a name and the subvolume it protects.
// `snapper list-configs` returns these, and every other command is scoped to
// one of them with `-c <name>`.
type Config struct {
	Name      string
	Subvolume string
}

// Snapshot is one row of `snapper -c <config> --jsonout list`.
type Snapshot struct {
	// Number is the snapshot id. Number 0 is not a snapshot at all: snapper
	// reports the live subvolume under it so ranges can name "the current
	// state", and it can never be deleted or modified.
	Number int
	// Type is single, pre or post.
	Type string
	// PreNumber is the pre snapshot a post snapshot belongs to, and zero
	// when there is none. snapper reports it as null for every other type.
	PreNumber int
	// Date is the creation time, parsed from snapper's "2026-08-29 23:39:29".
	// It is the zero time for snapshot 0, which has no date.
	Date time.Time
	// RawDate is the date exactly as snapper printed it, so a format this
	// tool does not understand is still shown rather than swallowed.
	RawDate string
	// User is the account that created the snapshot.
	User string
	// Cleanup is the cleanup algorithm bound to the snapshot: number,
	// timeline, empty-pre-post, or empty when nothing will ever remove it.
	Cleanup string
	// Description is the free-text label, usually the command that ran.
	Description string
	// Userdata is the key/value map snapper stores alongside a snapshot.
	Userdata map[string]string
	// UsedSpace is the exclusive space the snapshot holds, in bytes. It is
	// -1 when snapper reported null, which is what happens whenever btrfs
	// quotas are not enabled for the config.
	UsedSpace int64
	// Default and Active are the btrfs flags snapper reports for a
	// rollback-capable layout.
	Default bool
	Active  bool
	// Subvolume is the config's subvolume, repeated on every row by snapper.
	Subvolume string
}

// The snapshot types worth naming in code.
const (
	TypeSingle = "single"
	TypePre    = "pre"
	TypePost   = "post"
)

// UsedSpaceUnknown is the UsedSpace of a snapshot snapper reported null for,
// which means btrfs quotas are not enabled on the config.
const UsedSpaceUnknown int64 = -1

// Current reports whether this is snapper's placeholder for the live
// subvolume rather than a real snapshot.
func (s Snapshot) Current() bool { return s.Number == 0 }

// Paired reports whether the snapshot belongs to a pre/post pair.
func (s Snapshot) Paired() bool { return s.Type == TypePre || s.Type == TypePost }

// Pinned reports whether no cleanup algorithm will ever remove the snapshot,
// which is the difference between a snapshot that survives and one that does
// not.
func (s Snapshot) Pinned() bool { return !s.Current() && s.Cleanup == "" }

// UserdataString renders the userdata map the way snapper's own table does,
// with the keys in a stable order.
func (s Snapshot) UserdataString() string {
	if len(s.Userdata) == 0 {
		return ""
	}
	keys := make([]string, 0, len(s.Userdata))
	for key := range s.Userdata {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+s.Userdata[key])
	}
	return strings.Join(pairs, ", ")
}

// Haystack is the text the list filter matches against.
func (s Snapshot) Haystack() string {
	return strings.Join([]string{
		strconv.Itoa(s.Number), s.Type, s.User, s.Cleanup,
		s.Description, s.RawDate, s.UserdataString(),
	}, " ")
}

// SortSnapshots orders the list newest first, which is how anyone reads a
// snapshot history: the thing that just happened is the reason the tool was
// opened. Snapshot 0, the live subvolume, always stays on top of it.
func SortSnapshots(snapshots []Snapshot) {
	sort.SliceStable(snapshots, func(i, j int) bool {
		if snapshots[i].Current() != snapshots[j].Current() {
			return snapshots[i].Current()
		}
		return snapshots[i].Number > snapshots[j].Number
	})
}

// TotalUsedSpace sums the space the snapshots hold, and reports whether the
// figure is real. It is only real when btrfs quotas are enabled for the
// config: without them snapper reports null for every row, and a total of
// zero would be a lie rather than a fact.
func TotalUsedSpace(snapshots []Snapshot) (total int64, known bool) {
	for _, s := range snapshots {
		if s.Current() || s.UsedSpace == UsedSpaceUnknown {
			continue
		}
		total += s.UsedSpace
		known = true
	}
	return total, known
}

// Oldest and Newest are the ends of the history, ignoring the live subvolume.
// The second result is false when there are no real snapshots at all.
func Oldest(snapshots []Snapshot) (Snapshot, bool) { return edge(snapshots, true) }

// Newest returns the most recent real snapshot.
func Newest(snapshots []Snapshot) (Snapshot, bool) { return edge(snapshots, false) }

// edge walks the list once and returns the lowest or the highest numbered
// real snapshot.
func edge(snapshots []Snapshot, lowest bool) (Snapshot, bool) {
	var found Snapshot
	ok := false
	for _, s := range snapshots {
		if s.Current() {
			continue
		}
		if !ok || (lowest && s.Number < found.Number) || (!lowest && s.Number > found.Number) {
			found, ok = s, true
		}
	}
	return found, ok
}

// ChangeKind is what `snapper status` reports happened to one path between
// two snapshots.
type ChangeKind string

// The kinds a status line reports in its first column.
const (
	Created  ChangeKind = "created"
	Deleted  ChangeKind = "deleted"
	Modified ChangeKind = "modified"
	// TypeChanged is snapper's `t`: the path is still there but it is now a
	// different kind of thing, a file where a symlink used to be.
	TypeChanged ChangeKind = "type changed"
)

// Change is one line of `snapper -c <config> status <from>..<to>`.
type Change struct {
	// Kind is what happened to the path.
	Kind ChangeKind
	// Path is the path exactly as snapper printed it. snapper prints the
	// path it will also accept back in `diff` and `undochange`, so it is
	// passed through untouched rather than rebuilt.
	Path string
	// Status is the raw six-column flag string, "c.....".
	Status string
	// Metadata reports that something other than the contents changed:
	// permissions, owner, group, extended attributes or ACL.
	Metadata bool
}

// Haystack is the text the diff filter matches against.
func (c Change) Haystack() string { return c.Status + " " + string(c.Kind) + " " + c.Path }

// TimerState is the read-only state of one of snapper's systemd timers.
type TimerState struct {
	// Unit is the timer unit name.
	Unit string
	// Active is the runtime state: active, inactive, failed. It is empty
	// when systemd could not be asked.
	Active string
	// Enabled is the unit file state: enabled, disabled, static, masked.
	Enabled string
	// Err holds why the state could not be read, so the screen can say so
	// instead of showing a blank row.
	Err string
}

// TimerUnits are the two units that decide whether snapshots are taken and
// removed on a schedule. tui-snapper only reports their state: turning them
// on or off is systemd's job, and tui-systemd already does it.
var TimerUnits = []string{"snapper-timeline.timer", "snapper-cleanup.timer"}

// BootEntry is one snapshot entry of the boot menu, parsed read-only from a
// limine configuration. On an Omarchy-style layout this is where a rollback
// actually happens, so the tool shows what the boot menu offers rather than
// pretending it can swap subvolumes itself.
type BootEntry struct {
	// Title is the entry as the boot menu shows it.
	Title string
	// Number is the snapshot number parsed out of the title, and zero when
	// the title does not carry one.
	Number int
	// Comment is limine's own comment line for the entry, when it has one.
	Comment string
}

// RollbackKind is how a rollback is performed on this machine.
type RollbackKind string

// The layouts tui-snapper knows how to talk about.
const (
	// RollbackSnapper is the openSUSE-style layout, where `snapper
	// rollback` swaps the default subvolume.
	RollbackSnapper RollbackKind = "snapper"
	// RollbackBootMenu is the Omarchy / limine-snapper-sync layout, where
	// the boot menu lists the snapshots and booting one is the rollback.
	RollbackBootMenu RollbackKind = "boot-menu"
	// RollbackUnsupported is every other layout: the config is not the root
	// filesystem, or snapper was built without rollback support.
	RollbackUnsupported RollbackKind = "unsupported"
)

// Platform describes how this machine rolls back, which is the one thing that
// genuinely differs between an openSUSE box and an Omarchy one.
type Platform struct {
	// Kind is the layout that was detected.
	Kind RollbackKind
	// Reason explains the detection in one sentence, for the rollback
	// screen. It is shown whatever the kind, so the user can tell a real
	// answer from a guess.
	Reason string
	// BootConfig is the limine configuration the boot entries came from.
	BootConfig string
	// Entries are the snapshot entries of the boot menu, read-only.
	Entries []BootEntry
	// SnapperFlags are the feature flags `snapper --version` reported.
	SnapperFlags []string
}

// HasRollbackFlag reports whether snapper itself was built with rollback
// support.
func (p Platform) HasRollbackFlag() bool {
	for _, flag := range p.SnapperFlags {
		if flag == "rollback" {
			return true
		}
	}
	return false
}
