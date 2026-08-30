package snapper

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// This file turns snapper's output into the model. Every parser here is
// written against output captured from a real snapper 0.13.1 on a real btrfs
// filesystem; the captures live in testdata and the table tests replay them
// verbatim. When a parser is wrong on someone's machine, their output becomes
// the next case.

// dateLayout is how snapper prints a date in --jsonout, whatever the locale:
// "2026-08-29 23:39:29", in the machine's local time unless --utc was passed.
const dateLayout = "2006-01-02 15:04:05"

// configsPayload is the shape of `snapper --jsonout list-configs`.
type configsPayload struct {
	Configs []struct {
		Config    string `json:"config"`
		Subvolume string `json:"subvolume"`
	} `json:"configs"`
}

// ParseConfigs reads `snapper --jsonout list-configs`.
func ParseConfigs(out string) ([]Config, error) {
	text := strings.TrimSpace(out)
	if text == "" {
		return nil, fmt.Errorf("snapper printed nothing for list-configs")
	}
	var payload configsPayload
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return nil, fmt.Errorf("cannot read the config list: %w", err)
	}
	configs := make([]Config, 0, len(payload.Configs))
	for _, entry := range payload.Configs {
		if entry.Config == "" {
			continue
		}
		configs = append(configs, Config{Name: entry.Config, Subvolume: entry.Subvolume})
	}
	return configs, nil
}

// snapshotPayload is one entry of `snapper --jsonout -c <config> list`.
//
// pre-number, used-space and userdata are null for most rows, so every one of
// them is a pointer: a plain int would read a missing pre-number as 0, which
// is the live subvolume and a real snapshot number.
type snapshotPayload struct {
	Subvolume   string            `json:"subvolume"`
	Number      int               `json:"number"`
	Default     bool              `json:"default"`
	Active      bool              `json:"active"`
	Type        string            `json:"type"`
	PreNumber   *int              `json:"pre-number"`
	Date        string            `json:"date"`
	User        string            `json:"user"`
	UsedSpace   *int64            `json:"used-space"`
	Cleanup     string            `json:"cleanup"`
	Description string            `json:"description"`
	Userdata    map[string]string `json:"userdata"`
}

// ParseSnapshots reads `snapper --jsonout -c <config> list`.
//
// snapper keys the array by the config's own name rather than by a fixed
// field, so the payload is decoded into a one-key object and the single array
// it holds is taken. Passing the config name in would work too, but reading
// whatever key is there survives a config that was renamed between the two
// reads.
func ParseSnapshots(out string) ([]Snapshot, error) {
	text := strings.TrimSpace(out)
	if text == "" {
		return nil, fmt.Errorf("snapper printed nothing for list")
	}
	var payload map[string][]snapshotPayload
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		return nil, fmt.Errorf("cannot read the snapshot list: %w", err)
	}
	if len(payload) != 1 {
		return nil, fmt.Errorf(
			"expected one config in the snapshot list, got %d", len(payload))
	}

	var entries []snapshotPayload
	for _, value := range payload {
		entries = value
	}
	snapshots := make([]Snapshot, 0, len(entries))
	for _, entry := range entries {
		snapshots = append(snapshots, entry.snapshot())
	}
	SortSnapshots(snapshots)
	return snapshots, nil
}

// snapshot converts one decoded entry into the model.
func (p snapshotPayload) snapshot() Snapshot {
	s := Snapshot{
		Number:      p.Number,
		Type:        p.Type,
		RawDate:     p.Date,
		User:        p.User,
		Cleanup:     p.Cleanup,
		Description: p.Description,
		Userdata:    p.Userdata,
		UsedSpace:   UsedSpaceUnknown,
		Default:     p.Default,
		Active:      p.Active,
		Subvolume:   p.Subvolume,
	}
	if p.PreNumber != nil {
		s.PreNumber = *p.PreNumber
	}
	if p.UsedSpace != nil {
		s.UsedSpace = *p.UsedSpace
	}
	// A date snapper could not print, or one in a format this build does not
	// know, leaves Date zero and RawDate intact: the row still shows what
	// snapper said instead of an empty cell.
	if parsed, err := time.ParseInLocation(dateLayout, p.Date, time.Local); err == nil {
		s.Date = parsed
	}
	return s
}

// statusColumns is the width of a `snapper status` flag field, "c.....":
// contents, permissions, owner, group, extended attributes, ACL.
const statusColumns = 6

// ParseStatus reads `snapper -c <config> status <from>..<to>`.
//
// This output is plain text even under --jsonout: snapper 0.13.1 ignores the
// flag for status, which is why the tool parses lines here rather than JSON.
// Each line is a six-character flag field, a space, and the path — and the
// path can itself contain spaces, so it is everything after the first one.
func ParseStatus(out string) []Change {
	var changes []Change
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if len(line) < statusColumns+2 || line[statusColumns] != ' ' {
			continue
		}
		flags := line[:statusColumns]
		path := line[statusColumns+1:]
		kind, ok := changeKind(flags[0])
		if !ok || path == "" {
			continue
		}
		changes = append(changes, Change{
			Kind:     kind,
			Path:     path,
			Status:   flags,
			Metadata: strings.Trim(flags[1:], ".") != "",
		})
	}
	return changes
}

// changeKind maps the first status column to what happened to the path.
func changeKind(column byte) (ChangeKind, bool) {
	switch column {
	case '+':
		return Created, true
	case '-':
		return Deleted, true
	case 'c':
		return Modified, true
	case 't':
		return TypeChanged, true
	case '.':
		// Only metadata changed: the contents are the same, but the row is
		// still worth showing because a permission change is a change.
		return Modified, true
	default:
		return "", false
	}
}

// versionFlags reads the feature list `snapper --version` prints on its third
// line: "flags btrfs,lvm,ext4,xattrs,rollback,btrfs-quota,no-selinux".
func versionFlags(out string) []string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "flags ")
		if !ok {
			continue
		}
		var flags []string
		for _, flag := range strings.Split(rest, ",") {
			if flag = strings.TrimSpace(flag); flag != "" {
				flags = append(flags, flag)
			}
		}
		return flags
	}
	return nil
}

// ParseTimerState reads one unit's state from
// `systemctl show <unit> -p ActiveState -p UnitFileState`.
func ParseTimerState(unit, out string) TimerState {
	state := TimerState{Unit: unit}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "ActiveState":
			state.Active = value
		case "UnitFileState":
			state.Enabled = value
		}
	}
	// systemd answers for a unit it has never heard of with empty values
	// rather than an error, so an empty answer is reported as "not
	// installed" instead of as a blank row.
	if state.Active == "" && state.Enabled == "" {
		state.Err = "systemd does not know this unit"
	}
	return state
}

// FormatBytes renders a byte count in the largest unit that keeps it short,
// which is what fits in a header fact.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + " B"
	}
	div, exp := int64(unit), 0
	for value := n / unit; value >= unit && exp < 5; value /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
