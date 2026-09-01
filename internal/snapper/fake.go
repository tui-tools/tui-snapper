package snapper

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tui-tools/tui-kit/runner"
)

// Fake is the in-memory backend behind --demo and the tests. It builds and
// previews exactly the commands the real backend would, then applies them to
// its own state instead of to the machine.
//
// It is what makes --demo honest: the UI cannot tell it from the real thing,
// every key works, and a reviewer can try the tool without a btrfs filesystem
// to risk. It is also what the tests assert against — press a key, then check
// that exactly one command ran, with exactly the argv the preview showed.
type Fake struct {
	mu        sync.Mutex
	configs   []Config
	snapshots map[string][]Snapshot
	// settings is what get-config reports for each config.
	settings map[string]map[string]string
	// next is the number the next created snapshot gets.
	next int
	run  *runner.Fake
}

// DemoConfig is the config --demo opens on.
const DemoConfig = "root"

// NewFake returns a Fake preloaded with a plausible snapshot history: a
// timeline of automatic snapshots, two pre/post pairs from package upgrades,
// and one pinned snapshot with no cleanup algorithm.
func NewFake() *Fake {
	f := &Fake{
		configs: []Config{
			{Name: "root", Subvolume: "/"},
			{Name: "home", Subvolume: "/home"},
		},
		snapshots: map[string][]Snapshot{
			"root": demoRootSnapshots(),
			"home": demoHomeSnapshots(),
		},
		settings: map[string]map[string]string{
			"root": demoSettings("10", "yes"),
			"home": demoSettings("50", "no"),
		},
		next: 48,
	}
	f.run = &runner.Fake{Hook: f.apply}
	return f
}

// Name identifies the backend.
func (f *Fake) Name() string { return "demo" }

// Describe is the one-line summary shown in the header.
func (f *Fake) Describe() string { return "demo  ·  no snapshot is really created or deleted" }

// Preview renders the command the way the real backend would.
func (f *Fake) Preview(cmd runner.Command) string { return f.run.Preview(cmd) }

// Run applies a confirmed command to the in-memory state.
func (f *Fake) Run(ctx context.Context, cmd runner.Command) (string, error) {
	return f.run.Run(ctx, cmd)
}

// Commands returns every command the fake was asked to run, for the tests.
func (f *Fake) Commands() []runner.Command { return f.run.Ran }

// Build turns an action into a previewable command, exactly as Real does.
func (f *Fake) Build(spec ActionSpec, req Request) (runner.Command, error) {
	return BuildCommand(spec, req)
}

// Configs lists the sample configs.
func (f *Fake) Configs(_ context.Context) ([]Config, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Config(nil), f.configs...), nil
}

// Settings reports one sample config's settings, the way get-config would.
func (f *Fake) Settings(_ context.Context, config string) (map[string]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	settings, ok := f.settings[config]
	if !ok {
		//nolint:staticcheck // mirrors snapper's exact message
		return nil, fmt.Errorf("Config '%s' not found.", config)
	}
	out := map[string]string{}
	for key, value := range settings {
		out[key] = value
	}
	return out, nil
}

// CheckSubvolume answers the same question the real backend answers from the
// mount table, against the sample filesystem below. It refuses the same paths
// for the same reasons, so the demo shows the real refusals.
func (f *Fake) CheckSubvolume(_ context.Context, path string) error {
	if err := ValidateSubvolumePath(path); err != nil {
		return err
	}
	fstype, found := FilesystemOf(path, demoMounts())
	if !found {
		return fmt.Errorf("no mounted filesystem holds %s", path)
	}
	if fstype != BtrfsFSType {
		return fmt.Errorf(
			"%s is on a %s filesystem, and snapper's snapshots need %s",
			path, fstype, BtrfsFSType)
	}
	return nil
}

// Snapshots lists one sample config's snapshots.
func (f *Fake) Snapshots(_ context.Context, config string) ([]Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	snapshots, ok := f.snapshots[config]
	if !ok {
		// The real snapper's wording, so the demo shows the error the tool
		// would really surface. Capitalised and full-stopped on purpose.
		//nolint:staticcheck // mirrors snapper's exact message
		return nil, fmt.Errorf("Config '%s' not found.", config)
	}
	out := append([]Snapshot(nil), snapshots...)
	SortSnapshots(out)
	return out, nil
}

// Status returns the sample comparison between two snapshots.
func (f *Fake) Status(_ context.Context, config string, from, to int) ([]Change, error) {
	if _, err := f.Snapshots(context.Background(), config); err != nil {
		return nil, err
	}
	if from == to {
		return nil, fmt.Errorf("a comparison needs two different snapshots")
	}
	return demoChanges(), nil
}

// Diff returns a sample unified diff for a path.
func (f *Fake) Diff(_ context.Context, _ string, from, to int, path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("no file selected")
	}
	if body, ok := demoDiffs()[path]; ok {
		return fmt.Sprintf(
			"--- /.snapshots/%d/snapshot%s\n+++ /.snapshots/%d/snapshot%s\n%s",
			from, path, to, path, body), nil
	}
	return fmt.Sprintf(
		"--- /.snapshots/%d/snapshot%s\n+++ /.snapshots/%d/snapshot%s\n"+
			"Binary files differ.", from, path, to, path), nil
}

// Timers returns a plausible timer state: both units enabled and waiting,
// which is how a machine with snapper set up actually looks.
func (f *Fake) Timers(_ context.Context) []TimerState {
	return []TimerState{
		{Unit: "snapper-timeline.timer", Active: "active", Enabled: "enabled"},
		{Unit: "snapper-cleanup.timer", Active: "active", Enabled: "enabled"},
	}
}

// Platform returns the boot-menu layout, because that is the one a reader is
// least likely to have seen and the one the tool has the most to say about.
func (f *Fake) Platform(_ context.Context, config Config) Platform {
	if config.Subvolume != "/" {
		return Platform{
			Kind: RollbackUnsupported,
			Reason: fmt.Sprintf(
				"snapper rollback only applies to the root filesystem, and this config protects %s.",
				config.Subvolume),
			BootConfig: DefaultBootConfig,
		}
	}
	return Platform{
		Kind:         RollbackBootMenu,
		BootConfig:   DefaultBootConfig,
		SnapperFlags: []string{"btrfs", "lvm", "ext4", "xattrs", "rollback", "btrfs-quota"},
		Reason: DefaultBootConfig +
			" lists 4 snapshot entries, so this machine rolls back from the boot menu.",
		Entries: []BootEntry{
			{Title: "Snapshot 47 2026-08-29 09:12:04", Number: 47,
				Comment: "pacman -Syu (post)"},
			{Title: "Snapshot 45 2026-08-29 03:00:01", Number: 45,
				Comment: "timeline"},
			{Title: "Snapshot 42 2026-08-28 21:40:55", Number: 42,
				Comment: "before the nvidia downgrade"},
			{Title: "Snapshot 31 2026-08-26 03:00:02", Number: 31,
				Comment: "timeline"},
		},
	}
}

// apply mutates the sample state the way the real command would. It is the
// runner.Fake hook, so it runs only for a command that was previewed and
// confirmed — the same path the real backend takes.
func (f *Fake) apply(cmd runner.Command) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	config, rest, err := splitCommand(cmd.Argv)
	if err != nil {
		return "", err
	}
	// create-config is the one command whose config does not exist yet, so it
	// is applied before the "does this config exist" check below.
	if len(rest) > 0 && rest[0] == "create-config" {
		return f.applyCreateConfig(config, rest[1:])
	}
	if _, ok := f.snapshots[config]; !ok {
		//nolint:staticcheck // mirrors snapper's exact message
		return "", fmt.Errorf("Config '%s' not found.", config)
	}
	if len(rest) == 0 {
		return "", fmt.Errorf("malformed command %q", cmd)
	}

	switch rest[0] {
	case "create":
		return f.applyCreate(config, rest[1:])
	case "delete":
		return f.applyDelete(config, rest[1:])
	case "modify":
		return f.applyModify(config, rest[1:])
	case "cleanup":
		return f.applyCleanup(config, rest[1:])
	case "set-config":
		return f.applySetConfig(config, rest[1:])
	case "delete-config":
		return f.applyDeleteConfig(config)
	case "undochange":
		return "create:0 modify:1 delete:0", nil
	case "rollback":
		//nolint:staticcheck // mirrors snapper's exact message
		return "", fmt.Errorf(
			"Command 'rollback' cannot be used on a non-root subvolume /demo.")
	default:
		return "", fmt.Errorf("unknown command %q", rest[0])
	}
}

// applyCreate adds a snapshot the way `snapper create` would.
func (f *Fake) applyCreate(config string, args []string) (string, error) {
	snapshot := Snapshot{
		Number:    f.next,
		Type:      TypeSingle,
		Date:      time.Now(),
		RawDate:   time.Now().Format(dateLayout),
		User:      "root",
		UsedSpace: 24 * 1024 * 1024,
		Subvolume: f.subvolumeOf(config),
	}
	for i := 0; i+1 < len(args); i += 2 {
		switch args[i] {
		case "--type":
			snapshot.Type = args[i+1]
		case "--description":
			snapshot.Description = args[i+1]
		case "--cleanup-algorithm":
			snapshot.Cleanup = args[i+1]
		}
	}
	f.next++
	f.snapshots[config] = append(f.snapshots[config], snapshot)
	return "", nil
}

// applyCreateConfig registers a new config, the way `snapper create-config`
// would: the config exists afterwards and holds no snapshots yet.
func (f *Fake) applyCreateConfig(config string, args []string) (string, error) {
	if _, taken := f.settings[config]; taken {
		//nolint:staticcheck // mirrors snapper's exact message
		return "", fmt.Errorf("Config '%s' already exists.", config)
	}
	subvolume := ""
	if len(args) > 0 {
		subvolume = args[len(args)-1]
	}
	if subvolume == "" {
		return "", fmt.Errorf("create-config needs a subvolume")
	}
	f.configs = append(f.configs, Config{Name: config, Subvolume: subvolume})
	f.snapshots[config] = []Snapshot{{
		Number: 0, Type: TypeSingle, Description: "current", User: "root",
		UsedSpace: UsedSpaceUnknown, Subvolume: subvolume,
	}}
	f.settings[config] = demoSettings("50", "yes")
	return "", nil
}

// applySetConfig writes the KEY=VALUE pairs into the sample settings.
func (f *Fake) applySetConfig(config string, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("set-config needs at least one KEY=VALUE")
	}
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok || key == "" {
			//nolint:staticcheck // mirrors snapper's exact message
			return "", fmt.Errorf("Invalid configdata '%s'.", arg)
		}
		f.settings[config][key] = value
	}
	return "", nil
}

// applyDeleteConfig makes the sample state forget a config entirely.
func (f *Fake) applyDeleteConfig(config string) (string, error) {
	kept := make([]Config, 0, len(f.configs))
	for _, c := range f.configs {
		if c.Name != config {
			kept = append(kept, c)
		}
	}
	f.configs = kept
	delete(f.snapshots, config)
	delete(f.settings, config)
	return "", nil
}

// applyDelete removes the named snapshots.
func (f *Fake) applyDelete(config string, args []string) (string, error) {
	wanted := map[int]bool{}
	for _, arg := range args {
		number, err := strconv.Atoi(arg)
		if err != nil {
			//nolint:staticcheck // mirrors snapper's exact message
			return "", fmt.Errorf("Invalid snapshot '%s'.", arg)
		}
		wanted[number] = true
	}
	kept := make([]Snapshot, 0, len(f.snapshots[config]))
	removed := 0
	for _, s := range f.snapshots[config] {
		if wanted[s.Number] && !s.Current() {
			removed++
			continue
		}
		kept = append(kept, s)
	}
	if removed == 0 {
		//nolint:staticcheck // mirrors snapper's exact message
		return "", fmt.Errorf("Snapshot not found.")
	}
	f.snapshots[config] = kept
	return "", nil
}

// applyModify changes a snapshot's description or cleanup algorithm.
func (f *Fake) applyModify(config string, args []string) (string, error) {
	if len(args) < 3 {
		return "", fmt.Errorf("malformed modify command")
	}
	number, err := strconv.Atoi(args[len(args)-1])
	if err != nil {
		//nolint:staticcheck // mirrors snapper's exact message
		return "", fmt.Errorf("Invalid snapshot '%s'.", args[len(args)-1])
	}
	for i := range f.snapshots[config] {
		if f.snapshots[config][i].Number != number {
			continue
		}
		switch args[0] {
		case "--description":
			f.snapshots[config][i].Description = args[1]
		case "--cleanup-algorithm":
			f.snapshots[config][i].Cleanup = args[1]
		}
		return "", nil
	}
	//nolint:staticcheck // mirrors snapper's exact message
	return "", fmt.Errorf("Snapshot not found.")
}

// applyCleanup drops the snapshots an algorithm would remove. The demo keeps
// this simple and honest: it removes the oldest snapshots bound to that
// algorithm, which is the shape of what really happens without pretending to
// reimplement snapper's limits.
func (f *Fake) applyCleanup(config string, args []string) (string, error) {
	if len(args) != 1 {
		return "", fmt.Errorf("cleanup takes one algorithm")
	}
	algorithm := args[0]
	candidates := make([]Snapshot, 0, len(f.snapshots[config]))
	for _, s := range f.snapshots[config] {
		if s.Current() {
			continue
		}
		if algorithm == "empty-pre-post" {
			if s.Paired() {
				candidates = append(candidates, s)
			}
			continue
		}
		if s.Cleanup == algorithm {
			candidates = append(candidates, s)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].Number < candidates[j].Number
	})
	// Half of what the algorithm governs is a plausible sweep and leaves the
	// demo list worth looking at afterwards.
	drop := map[int]bool{}
	for i := 0; i < len(candidates)/2; i++ {
		drop[candidates[i].Number] = true
	}
	if len(drop) == 0 {
		return "nothing to do", nil
	}
	kept := make([]Snapshot, 0, len(f.snapshots[config]))
	for _, s := range f.snapshots[config] {
		if drop[s.Number] {
			continue
		}
		kept = append(kept, s)
	}
	f.snapshots[config] = kept
	return fmt.Sprintf("deleted %d snapshots", len(drop)), nil
}

// subvolumeOf looks up a config's subvolume.
func (f *Fake) subvolumeOf(config string) string {
	for _, c := range f.configs {
		if c.Name == config {
			return c.Subvolume
		}
	}
	return ""
}

// splitCommand pulls the config out of a `snapper -c <config> …` argv, so the
// fake reads the command the same way snapper does.
func splitCommand(argv []string) (config string, rest []string, err error) {
	if len(argv) < 4 || argv[0] != "snapper" || argv[1] != "-c" {
		return "", nil, fmt.Errorf("malformed command %q", strings.Join(argv, " "))
	}
	return argv[2], argv[3:], nil
}

// demoRootSnapshots is the sample history --demo opens on: automatic timeline
// snapshots, two pre/post pairs from package upgrades, and one pinned
// snapshot a user took by hand before a risky change.
//
// The times are relative to now, so the view reads sensibly however long
// after this was written it runs.
func demoRootSnapshots() []Snapshot {
	now := time.Now()
	at := func(d time.Duration) time.Time { return now.Add(-d) }

	rows := []Snapshot{
		{Number: 0, Type: TypeSingle, Description: "current", User: "root"},
		{Number: 31, Type: TypeSingle, Cleanup: "timeline", User: "root",
			Description: "timeline", Date: at(78 * time.Hour), UsedSpace: 412 * 1024 * 1024},
		{Number: 36, Type: TypePre, Cleanup: "number", User: "root",
			Description: "pacman -Syu", Date: at(54 * time.Hour), UsedSpace: 96 * 1024 * 1024},
		{Number: 37, Type: TypePost, PreNumber: 36, Cleanup: "number", User: "root",
			Description: "pacman -Syu", Date: at(54*time.Hour - 3*time.Minute),
			UsedSpace: 1_284 * 1024 * 1024},
		{Number: 40, Type: TypeSingle, Cleanup: "timeline", User: "root",
			Description: "timeline", Date: at(30 * time.Hour), UsedSpace: 88 * 1024 * 1024},
		{Number: 42, Type: TypeSingle, User: "edimar",
			Description: "before the nvidia downgrade", Date: at(26 * time.Hour),
			UsedSpace: 640 * 1024 * 1024,
			Userdata:  map[string]string{"important": "yes", "reason": "manual"}},
		{Number: 45, Type: TypeSingle, Cleanup: "timeline", User: "root",
			Description: "timeline", Date: at(6 * time.Hour), UsedSpace: 74 * 1024 * 1024},
		{Number: 46, Type: TypePre, Cleanup: "number", User: "root",
			Description: "pacman -S linux-firmware", Date: at(95 * time.Minute),
			UsedSpace: 12 * 1024 * 1024},
		{Number: 47, Type: TypePost, PreNumber: 46, Cleanup: "number", User: "root",
			Description: "pacman -S linux-firmware", Date: at(94 * time.Minute),
			UsedSpace: 318 * 1024 * 1024},
	}
	for i := range rows {
		rows[i].Subvolume = "/"
		if rows[i].Current() {
			rows[i].UsedSpace = UsedSpaceUnknown
			continue
		}
		rows[i].RawDate = rows[i].Date.Format(dateLayout)
	}
	return rows
}

// demoHomeSnapshots is a second config, so switching configs does something
// visible in the demo.
func demoHomeSnapshots() []Snapshot {
	now := time.Now()
	rows := []Snapshot{
		{Number: 0, Type: TypeSingle, Description: "current", User: "root",
			UsedSpace: UsedSpaceUnknown},
		{Number: 12, Type: TypeSingle, Cleanup: "timeline", User: "root",
			Description: "timeline", Date: now.Add(-50 * time.Hour),
			UsedSpace: UsedSpaceUnknown},
		{Number: 13, Type: TypeSingle, Cleanup: "timeline", User: "root",
			Description: "timeline", Date: now.Add(-26 * time.Hour),
			UsedSpace: UsedSpaceUnknown},
		{Number: 14, Type: TypeSingle, User: "edimar",
			Description: "before wiping ~/.config", Date: now.Add(-2 * time.Hour),
			UsedSpace: UsedSpaceUnknown},
	}
	for i := range rows {
		rows[i].Subvolume = "/home"
		if !rows[i].Current() {
			rows[i].RawDate = rows[i].Date.Format(dateLayout)
		}
	}
	return rows
}

// demoSettings is the settings table `snapper get-config` reports for a sample
// config. The two arguments are the values that differ between the demo
// configs, so switching config on the settings form shows different numbers.
func demoSettings(numberLimit, timelineCreate string) map[string]string {
	return map[string]string{
		"ALLOW_GROUPS":           "wheel",
		"ALLOW_USERS":            "",
		"BACKGROUND_COMPARISON":  "yes",
		"EMPTY_PRE_POST_CLEANUP": "yes",
		"FSTYPE":                 BtrfsFSType,
		"NUMBER_CLEANUP":         "yes",
		"NUMBER_LIMIT":           numberLimit,
		"NUMBER_LIMIT_IMPORTANT": "10",
		"NUMBER_MIN_AGE":         "1800",
		"SUBVOLUME":              "/",
		"SYNC_ACL":               "no",
		"TIMELINE_CLEANUP":       "yes",
		"TIMELINE_CREATE":        timelineCreate,
		"TIMELINE_LIMIT_DAILY":   "7",
		"TIMELINE_LIMIT_HOURLY":  "10",
		"TIMELINE_LIMIT_MONTHLY": "6",
		"TIMELINE_LIMIT_WEEKLY":  "4",
		"TIMELINE_LIMIT_YEARLY":  "2",
	}
}

// demoMounts is the sample mount table CheckSubvolume answers from: two btrfs
// subvolumes that already have a config, two that do not, and two paths on
// another filesystem, so the demo can show both a config being created and the
// refusal a wrong path gets.
func demoMounts() []Mount {
	return []Mount{
		{Point: "/", FSType: BtrfsFSType},
		{Point: "/home", FSType: BtrfsFSType},
		{Point: "/srv", FSType: BtrfsFSType},
		{Point: "/var/log", FSType: BtrfsFSType},
		{Point: "/boot", FSType: "vfat"},
		{Point: "/tmp", FSType: "tmpfs"},
	}
}

// demoChanges is the sample comparison the diff view shows: a package upgrade
// as it really looks, with a configuration file modified, a new unit dropped
// in, an old library removed, and one path whose permissions moved but whose
// contents did not.
func demoChanges() []Change {
	return []Change{
		{Kind: Modified, Path: "/etc/pacman.d/mirrorlist", Status: "c....."},
		{Kind: Modified, Path: "/etc/mkinitcpio.conf", Status: "c....."},
		{Kind: Created, Path: "/usr/lib/systemd/system/nvidia-suspend.service", Status: "+....."},
		{Kind: Created, Path: "/usr/lib/modules/6.19.4-arch1-1/vmlinuz", Status: "+....."},
		{Kind: Deleted, Path: "/usr/lib/modules/6.19.2-arch1-1/vmlinuz", Status: "-....."},
		{Kind: Deleted, Path: "/usr/lib/libcurl.so.4.8.0", Status: "-....."},
		{Kind: Modified, Path: "/etc/ssh/sshd_config", Status: ".p...."},
		{Kind: Modified, Path: "/boot/initramfs-linux.img", Status: "c....."},
		{Kind: TypeChanged, Path: "/usr/bin/vi", Status: "t....."},
	}
}

// demoDiffs is the body of the unified diff shown for a path the reader is
// likely to open first.
func demoDiffs() map[string]string {
	return map[string]string{
		"/etc/pacman.d/mirrorlist": `@@ -1,6 +1,7 @@
 ##
 ## Arch Linux repository mirrorlist
 ##
-Server = https://mirror.example.net/archlinux/$repo/os/$arch
+Server = https://mirror.fastest.example/archlinux/$repo/os/$arch
+Server = https://mirror.example.net/archlinux/$repo/os/$arch
 Server = https://mirror.backup.example/archlinux/$repo/os/$arch`,
		"/etc/mkinitcpio.conf": `@@ -50,7 +50,7 @@
 #    This setup will autodetect all modules for your system and should
 #    work as a sane default
 #
-HOOKS=(base udev autodetect modconf kms keyboard block filesystems fsck)
+HOOKS=(base udev autodetect modconf kms keyboard block encrypt filesystems fsck)

 # COMPRESSION`,
		"/etc/ssh/sshd_config": `Only the metadata changed: the contents of this file are identical in both
snapshots. snapper reported ".p....", which is a permission change.`,
	}
}
