package snapper

import (
	"os"
	"regexp"
	"strconv"
	"strings"
)

// This file reads the snapshot entries out of a limine configuration. It is
// read-only on purpose: on an Omarchy or limine-snapper-sync layout the boot
// menu is where a rollback actually happens, and limine-snapper-sync owns
// that file. tui-snapper shows what is there and never writes to it.
//
// The grammar is limine's own (CONFIG.md, "Structure of the config file"):
//
//   - a line starting with '#' is a comment;
//   - a line starting with one or more '/' opens a menu entry, and the number
//     of slashes is its depth in the tree;
//   - an optional '+' after the slashes marks a directory that is expanded by
//     default, and is not part of the title;
//   - every other line is an `option: value` belonging to the entry above it,
//     with the value running unquoted to the end of the line.
//
// Leading whitespace is not part of the grammar and carries no meaning: the
// two tools that write this file indent by different amounts (two spaces for
// the kernel entries, five for the generated snapshot subtree), so depth is
// taken from the slashes and never from the indentation.

// snapshotsNode is the title limine-snapper-sync gives the subtree it owns.
const snapshotsNode = "Snapshots"

// bootOptionKeys are the options that make an entry bootable. They are what
// separates a snapshot entry from the grouping node above it: a snapshot
// entry's children boot something, a grouping node's children do not.
var bootOptionKeys = map[string]bool{
	"protocol":       true,
	"path":           true,
	"kernel_path":    true,
	"module_path":    true,
	"image_path":     true,
	"cmdline":        true,
	"kernel_cmdline": true,
}

// idPattern matches the snapshot number in the titles that carry one, which
// is every format except the timestamp-only one Omarchy uses.
var idPattern = regexp.MustCompile(`\bID=(\d+)`)

// subvolPattern matches the snapshot number in a boot line's rootflags, which
// is where the number is recoverable when the title does not carry it:
// `rootflags=subvol=/@/.snapshots/112/snapshot`.
var subvolPattern = regexp.MustCompile(`\.snapshots/(\d+)/snapshot`)

// limineEntry is one parsed menu entry.
type limineEntry struct {
	depth   int
	title   string
	options map[string]string
	// order is the entry's position in the file, so children can be found
	// without building a tree.
	order int
}

// ReadBootEntries reads the snapshot entries out of a limine configuration.
// A missing file is not an error worth reporting up: a machine with no limine
// simply has no boot entries, and the caller reads that from the empty slice.
func ReadBootEntries(path string) ([]BootEntry, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path) //nolint:gosec // the path is configuration, not user input
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	return ParseBootEntries(string(data)), nil
}

// ParseBootEntries finds the Snapshots subtree of a limine configuration and
// returns the snapshot entries it offers.
func ParseBootEntries(text string) []BootEntry {
	entries := parseLimine(text)
	anchor, ok := findSnapshotsNode(entries)
	if !ok {
		return nil
	}

	var out []BootEntry
	for i := anchor + 1; i < len(entries); i++ {
		entry := entries[i]
		// The subtree ends at the first entry that is not below the anchor.
		if entry.depth <= entries[anchor].depth {
			break
		}
		// Only the entries whose own children boot something are snapshots.
		// The layout that puts Snapshots at the top level inserts a node per
		// operating system in between, and that node is not a snapshot.
		if !hasBootableChild(entries, i) {
			continue
		}
		out = append(out, BootEntry{
			Title:   entry.title,
			Number:  snapshotNumber(entries, i),
			Comment: entry.options["comment"],
		})
	}
	return out
}

// parseLimine turns the configuration into a flat list of entries, each
// carrying the options written under it.
func parseLimine(text string) []limineEntry {
	var entries []limineEntry
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(strings.TrimRight(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "/") {
			depth := 0
			for depth < len(line) && line[depth] == '/' {
				depth++
			}
			title := strings.TrimPrefix(line[depth:], "+")
			entries = append(entries, limineEntry{
				depth:   depth,
				title:   strings.TrimSpace(title),
				options: map[string]string{},
				order:   len(entries),
			})
			continue
		}
		// An option before the first entry is a global setting (timeout,
		// default_entry) and belongs to nothing this parser cares about.
		if len(entries) == 0 {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		// limine allows an option to repeat; the generated `comment:` lines
		// are the case that matters, and the first one is the description.
		if _, seen := entries[len(entries)-1].options[key]; !seen {
			entries[len(entries)-1].options[key] = value
		}
	}
	return entries
}

// findSnapshotsNode locates the subtree limine-snapper-sync owns. It sits
// either inside the operating system's entry or as a sibling of it, so both
// depths are accepted; anything deeper would be somebody else's entry that
// happens to share the name.
func findSnapshotsNode(entries []limineEntry) (int, bool) {
	for i, entry := range entries {
		if entry.depth <= 2 && strings.EqualFold(entry.title, snapshotsNode) {
			return i, true
		}
	}
	return 0, false
}

// hasBootableChild reports whether the entry at index has a direct child that
// carries boot options.
func hasBootableChild(entries []limineEntry, index int) bool {
	depth := entries[index].depth
	for i := index + 1; i < len(entries); i++ {
		if entries[i].depth <= depth {
			return false
		}
		if entries[i].depth != depth+1 {
			continue
		}
		for key := range entries[i].options {
			if bootOptionKeys[key] {
				return true
			}
		}
	}
	return false
}

// snapshotNumber recovers a snapshot's number. Most title formats carry it as
// "ID=112"; the timestamp-only format Omarchy uses does not, and then the
// number is read out of a child's rootflags, which always names the snapshot
// subvolume.
func snapshotNumber(entries []limineEntry, index int) int {
	if match := idPattern.FindStringSubmatch(entries[index].title); match != nil {
		if number, err := strconv.Atoi(match[1]); err == nil {
			return number
		}
	}
	depth := entries[index].depth
	for i := index; i < len(entries); i++ {
		if i > index && entries[i].depth <= depth {
			break
		}
		for _, value := range entries[i].options {
			if match := subvolPattern.FindStringSubmatch(value); match != nil {
				if number, err := strconv.Atoi(match[1]); err == nil {
					return number
				}
			}
		}
	}
	return 0
}
