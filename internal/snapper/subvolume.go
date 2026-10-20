package snapper

// Whether a path can hold a new snapper config, asked before the command that
// would fail on it is previewed.
//
// `snapper create-config -f btrfs <path>` refuses two kinds of path that look
// fine from the outside, and it refuses them only after the user has read the
// preview and confirmed it (tui-tools/tui-snapper#18):
//
//   - a directory that is not the root of a subvolume:
//     "IO error (subvolume is not a btrfs subvolume)."
//   - a subvolume that already has something called .snapshots in it, which
//     is the archinstall / Omarchy layout, where a separate @snapshots
//     subvolume is mounted there from /etc/fstab: "Creating config failed
//     (creating btrfs subvolume .snapshots failed since it already exists)."
//
// Both are facts on disk, so both are checked here, by reading the filesystem
// rather than by running anything, and refused with the reason and the way
// out. The way out is shown, never performed: it unmounts, removes and
// recreates things under a subvolume that holds snapshot history, and that is
// a job for a shell where each step can be checked, the same line this tool
// draws for a boot-menu rollback.

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// SubvolumeRootInode is the inode number of the root directory of every btrfs
// subvolume (BTRFS_FIRST_FREE_OBJECTID). An ordinary directory on btrfs has
// any other number, so this one number tells a subvolume from a directory
// without running btrfs(8) or needing root.
const SubvolumeRootInode = 256

// SnapshotsDir is the subvolume snapper creates inside the one it manages, and
// keeps the snapshots in.
const SnapshotsDir = ".snapshots"

// PathEntry is what the filesystem says about one path, as much of it as the
// check needs.
type PathEntry struct {
	IsDir     bool
	IsSymlink bool
	// Inode is the inode number; SubvolumeRootInode marks a subvolume root.
	Inode uint64
	// Empty reports a directory with nothing in it.
	Empty bool
}

// Inspector reads one path. follow says whether a symbolic link is followed
// (the path the user typed) or reported as itself (what sits at .snapshots).
// A path that does not exist is an error wrapping fs.ErrNotExist.
type Inspector func(path string, follow bool) (PathEntry, error)

// InspectPath is the Inspector the real backend uses: stat(2), and for a
// directory one entry of readdir(3) to tell an empty one from a full one.
func InspectPath(path string, follow bool) (PathEntry, error) {
	stat := os.Lstat
	if follow {
		stat = os.Stat
	}
	info, err := stat(path)
	if err != nil {
		return PathEntry{}, err
	}
	entry := PathEntry{
		IsDir:     info.IsDir(),
		IsSymlink: info.Mode()&fs.ModeSymlink != 0,
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		entry.Inode = st.Ino
	}
	if entry.IsDir {
		entry.Empty = dirEmpty(path)
	}
	return entry, nil
}

// dirEmpty reports a directory with no entries. A directory that cannot be
// listed counts as not empty: the advice for a full one is the careful one.
func dirEmpty(path string) bool {
	dir, err := os.Open(path) //#nosec G304 -- the path is the one the user typed, already validated
	if err != nil {
		return false
	}
	defer func() { _ = dir.Close() }()
	_, err = dir.Readdirnames(1)
	return errors.Is(err, io.EOF)
}

// SubvolumeRefusal is a path the new-config form sends back for a reason the
// user can act on. Reason is one line, for the status line; Advice is what the
// form shows under the prompt: what the path is, and the commands that make it
// a path snapper accepts, to run in a shell.
type SubvolumeRefusal struct {
	Reason string
	Advice string
}

// Error is the one-line reason.
func (r *SubvolumeRefusal) Error() string { return r.Reason }

// CheckSubvolume reports whether a path can hold a new snapper config: it has
// to exist, be a directory on a mounted btrfs filesystem, be the root of a
// subvolume, and have no .snapshots in it yet, because snapper creates that
// itself.
func CheckSubvolume(path string, mounts []Mount) error {
	return checkSubvolume(path, mounts, InspectPath)
}

// checkSubvolume is CheckSubvolume against any Inspector, so the demo backend
// and the tests walk the same checks with a filesystem of their own.
func checkSubvolume(path string, mounts []Mount, inspect Inspector) error {
	if err := ValidateSubvolumePath(path); err != nil {
		return err
	}
	entry, err := inspect(path, true)
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s does not exist", path)
	}
	if err != nil {
		return fmt.Errorf("%s cannot be read: %w", path, err)
	}
	if !entry.IsDir {
		return fmt.Errorf("%s is not a directory", path)
	}
	mount, found := mountOf(path, mounts)
	if !found {
		return fmt.Errorf("no mounted filesystem holds %s", path)
	}
	if mount.FSType != BtrfsFSType {
		return fmt.Errorf(
			"%s is on a %s filesystem, and snapper's snapshots need %s",
			path, mount.FSType, BtrfsFSType)
	}
	if entry.Inode != SubvolumeRootInode {
		return notASubvolume(path, mount.Point)
	}

	snapshots := filepath.Join(path, SnapshotsDir)
	existing, err := inspect(snapshots, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s cannot be read: %w", snapshots, err)
	}
	return snapshotsInTheWay(path, snapshots, existing, isMountPoint(snapshots, mounts))
}

// mountOf returns the mount that holds a path: the longest mount point that is
// a prefix of it (see FilesystemOf).
func mountOf(path string, mounts []Mount) (Mount, bool) {
	best, found := Mount{}, false
	for _, mount := range mounts {
		if !underMount(path, mount.Point) {
			continue
		}
		if !found || len(mount.Point) > len(best.Point) {
			best, found = mount, true
		}
	}
	return best, found
}

// isMountPoint reports that something is mounted exactly at path.
func isMountPoint(path string, mounts []Mount) bool {
	for _, mount := range mounts {
		if mount.Point == path {
			return true
		}
	}
	return false
}

// notASubvolume refuses a directory that is on btrfs but is not the top of a
// subvolume. snapper keeps its snapshots in a .snapshots subvolume next to the
// data it snapshots, so it manages subvolumes only.
func notASubvolume(path, mountPoint string) error {
	return &SubvolumeRefusal{
		Reason: path + " is a directory on btrfs, not a subvolume: " +
			"snapper can only manage the top of a subvolume",
		Advice: strings.Join([]string{
			"snapper snapshots whole subvolumes, and " + path +
				" is a directory inside one. Type a subvolume instead. " +
				"To see which ones there are, as root:",
			"",
			"  btrfs subvolume list " + mountPoint,
			"",
			"Or make the directory a subvolume of its own, while nothing writes to it:",
			"",
			"  btrfs subvolume create " + path + ".new",
			"  cp -a --reflink=always " + path + "/. " + path + ".new/",
			"  mv " + path + " " + path + ".old",
			"  mv " + path + ".new " + path,
		}, "\n"),
	}
}

// snapshotsInTheWay refuses a subvolume that already has a .snapshots, and
// says what it most likely is and how to get past it without losing anything.
func snapshotsInTheWay(path, snapshots string, existing PathEntry, mounted bool) error {
	config := SuggestConfigName(path)
	create := "snapper -c " + config + " create-config -f btrfs " + path

	if mounted {
		// The archinstall / Omarchy layout. snapper insists on creating
		// .snapshots itself, so the documented way through steps the mount
		// aside, lets snapper create the config, then puts the subvolume
		// from fstab back in place of the one snapper made. The snapshots on
		// the mounted subvolume are not touched by any of it.
		return &SubvolumeRefusal{
			Reason: snapshots + " is already a mounted subvolume, and snapper " +
				"creates .snapshots itself",
			Advice: strings.Join([]string{
				"A separate subvolume is mounted at " + snapshots +
					", as archinstall and Omarchy set it up (@snapshots in " +
					"/etc/fstab). snapper create-config refuses to start while it " +
					"is there. The usual way through, as root:",
				"",
				"  umount " + snapshots,
				"  rmdir " + snapshots,
				"  " + create,
				"  btrfs subvolume delete " + snapshots,
				"  mkdir " + snapshots,
				"  mount -a",
				"  chmod 750 " + snapshots,
				"",
				"The subvolume deleted is the empty one snapper just made; yours " +
					"is mounted back from fstab with what it held. Then press r here.",
			}, "\n"),
		}
	}

	var what, remove string
	switch {
	case existing.IsDir && existing.Inode == SubvolumeRootInode && existing.Empty:
		what = "an empty subvolume, probably left by a config that was deleted"
		remove = "btrfs subvolume delete " + snapshots
	case existing.IsDir && existing.Empty:
		what = "an empty directory"
		remove = "rmdir " + snapshots
	case existing.IsDir && existing.Inode == SubvolumeRootInode:
		what = "a subvolume with something in it, likely an earlier config's snapshots"
	case existing.IsDir:
		what = "a directory with something in it"
	default:
		what = "not a directory"
	}

	intro := snapshots + " is " + what + ". snapper create-config creates " +
		".snapshots itself and refuses while anything has that name. "
	var advice []string
	if remove != "" {
		advice = []string{intro + "Nothing is in it, so it can go, as root:", "",
			"  " + remove}
	} else {
		advice = []string{intro + "Move it aside rather than removing it, " +
			"and look at what it holds later, as root:", "",
			"  mv " + snapshots + " " + snapshots + ".old"}
	}
	advice = append(advice, "", "Then create the config here again.")
	return &SubvolumeRefusal{
		Reason: snapshots + " already exists, and snapper creates .snapshots itself",
		Advice: strings.Join(advice, "\n"),
	}
}
