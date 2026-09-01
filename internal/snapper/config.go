package snapper

// Authoring a snapper configuration rather than only reading one. Creating a
// config, changing its retention limits and deleting it are the three things
// the tool used to tell the user to go and do in a shell; they are actions
// here now, built by BuildCommand like every other mutation and previewed
// before anything runs.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// The config-level actions. They are a separate table from Actions because
// they act on the configuration itself rather than on a snapshot, and because
// two of their keys ("a", "D") mean something else on the snapshot list.
const (
	// CreateConfig makes snapper start managing a subvolume.
	CreateConfig Action = "create-config"
	// SetConfig changes the retention limits of an existing config.
	SetConfig Action = "set-config"
	// DeleteConfig makes snapper forget a config.
	DeleteConfig Action = "delete-config"
)

// The scopes the config actions use.
const (
	// ScopeNewConfig needs nothing selected: it creates a config.
	ScopeNewConfig Scope = "new-config"
	// ScopeConfigRow applies to the config the cursor is on.
	ScopeConfigRow Scope = "config-row"
)

// The fields the config dialogs collect. The setting fields are named after
// the snapper key they carry, so the table below is the only place the two are
// tied together.
const (
	FieldSubvolume            FieldKind = "subvolume"
	FieldConfigName           FieldKind = "config-name"
	FieldNumberLimit          FieldKind = "number-limit"
	FieldNumberLimitImportant FieldKind = "number-limit-important"
	FieldTimelineCreate       FieldKind = "timeline-create"
	FieldTimelineHourly       FieldKind = "timeline-hourly"
	FieldTimelineDaily        FieldKind = "timeline-daily"
	FieldTimelineWeekly       FieldKind = "timeline-weekly"
	FieldTimelineMonthly      FieldKind = "timeline-monthly"
	FieldTimelineYearly       FieldKind = "timeline-yearly"
	FieldEmptyPrePostCleanup  FieldKind = "empty-pre-post-cleanup"
)

// YesNo are the two values snapper accepts for a boolean config key. They are
// spelled the way snapper spells them, so what the picker shows is what the
// command carries.
var YesNo = []string{"yes", "no"}

// BtrfsFSType is the only filesystem snapper's btrfs backend can manage, and
// the one this tool offers when creating a config.
const BtrfsFSType = "btrfs"

// Setting is one editable key of a snapper configuration: which snapper key it
// writes, which dialog collects it, and what a valid answer looks like.
type Setting struct {
	// Key is the snapper config key, written as KEY=VALUE by set-config.
	Key string
	// Kind is the field the dialog collects it under.
	Kind FieldKind
	// Title is the prompt's heading.
	Title string
	// Help is the line under the prompt.
	Help string
	// Options, when non-empty, makes this a picker and the only valid answers.
	Options []string
}

// EditableSettings is the closed set of keys this tool writes.
//
// snapper configs carry far more than these, including keys that decide who
// may run snapper at all. Everything here is retention: how many snapshots
// survive and whether the timeline takes any. A key that is not on this list
// is never touched, which is what makes `set-config` safe to offer at all.
var EditableSettings = []Setting{
	{Key: "NUMBER_LIMIT", Kind: FieldNumberLimit, Title: "NUMBER_LIMIT",
		Help: "How many number-algorithm snapshots survive a cleanup. A range like 10-50 works too."},
	{Key: "NUMBER_LIMIT_IMPORTANT", Kind: FieldNumberLimitImportant,
		Title: "NUMBER_LIMIT_IMPORTANT",
		Help:  "The same limit for the snapshots marked important, counted separately."},
	{Key: "TIMELINE_CREATE", Kind: FieldTimelineCreate, Title: "TIMELINE_CREATE",
		Options: YesNo,
		Help:    "Whether snapper-timeline.timer takes hourly snapshots of this config."},
	{Key: "TIMELINE_LIMIT_HOURLY", Kind: FieldTimelineHourly,
		Title: "TIMELINE_LIMIT_HOURLY", Help: "How many hourly timeline snapshots are kept."},
	{Key: "TIMELINE_LIMIT_DAILY", Kind: FieldTimelineDaily,
		Title: "TIMELINE_LIMIT_DAILY", Help: "How many daily timeline snapshots are kept."},
	{Key: "TIMELINE_LIMIT_WEEKLY", Kind: FieldTimelineWeekly,
		Title: "TIMELINE_LIMIT_WEEKLY", Help: "How many weekly timeline snapshots are kept."},
	{Key: "TIMELINE_LIMIT_MONTHLY", Kind: FieldTimelineMonthly,
		Title: "TIMELINE_LIMIT_MONTHLY", Help: "How many monthly timeline snapshots are kept."},
	{Key: "TIMELINE_LIMIT_YEARLY", Kind: FieldTimelineYearly,
		Title: "TIMELINE_LIMIT_YEARLY", Help: "How many yearly timeline snapshots are kept."},
	{Key: "EMPTY_PRE_POST_CLEANUP", Kind: FieldEmptyPrePostCleanup,
		Title: "EMPTY_PRE_POST_CLEANUP", Options: YesNo,
		Help: "Whether pre/post pairs that changed nothing are dropped by the cleanup."},
}

// SettingFor returns the editable setting a field carries.
func SettingFor(kind FieldKind) (Setting, bool) {
	for _, setting := range EditableSettings {
		if setting.Kind == kind {
			return setting, true
		}
	}
	return Setting{}, false
}

// settingFields renders the editable settings as dialog fields, so the form
// and the command can never disagree about which keys exist.
func settingFields() []Field {
	fields := make([]Field, 0, len(EditableSettings))
	for _, setting := range EditableSettings {
		fields = append(fields, Field{
			Kind: setting.Kind, Title: setting.Title,
			Help: setting.Help, Options: setting.Options,
			// An empty answer means "leave this key alone", which is what a
			// key snapper did not report needs: the form still walks past it
			// instead of trapping the user on a prompt with no valid answer.
			Optional: len(setting.Options) == 0,
		})
	}
	return fields
}

// ConfigActions is the config screen's action table, in help-screen order.
var ConfigActions = []ActionSpec{
	{Action: CreateConfig, Key: "n", Label: "Create a config", Scope: ScopeNewConfig,
		Body: "snapper starts managing this subvolume: it writes a configuration file and " +
			"creates the .snapshots subvolume under it. No snapshot is taken yet, and " +
			"nothing already on the filesystem is changed.",
		Fields: []Field{
			{Kind: FieldSubvolume, Title: "Subvolume",
				Help: "An absolute path on a mounted btrfs filesystem, such as /home."},
			{Kind: FieldConfigName, Title: "Config name",
				Help: "The name every later command uses with -c. Letters, digits, dot, dash, underscore."},
		}},
	{Action: SetConfig, Key: "a", Label: "Change the retention limits", Scope: ScopeConfigRow,
		Body: "Only the retention keys below are written, and only the ones you changed. " +
			"No snapshot is deleted now: the next cleanup run enforces the new limits.",
		Fields: settingFields()},
	{Action: DeleteConfig, Key: "D", Label: "Delete the config", Scope: ScopeConfigRow,
		Destructive: true,
		Body: "snapper forgets this configuration and deletes every snapshot it holds, " +
			"along with the .snapshots subvolume. The live subvolume itself stays. " +
			"This cannot be undone."},
}

// ConfigActionFor returns the config-screen spec bound to a key.
func ConfigActionFor(key string) (ActionSpec, bool) {
	for _, spec := range ConfigActions {
		if spec.Key == key {
			return spec, true
		}
	}
	return ActionSpec{}, false
}

// ConfigSpec returns the spec of a config action.
func ConfigSpec(action Action) (ActionSpec, bool) {
	for _, spec := range ConfigActions {
		if spec.Action == action {
			return spec, true
		}
	}
	return ActionSpec{}, false
}

// ProtectedConfig is the config this tool refuses to delete. Dropping it takes
// the root filesystem's whole snapshot history with it, including the entries
// a limine boot menu offers, and no confirmation dialog makes that a good idea
// from a snapshot browser.
const ProtectedConfig = "root"

// maxConfigNameLen bounds a config name. snapper stores it as a file name
// under /etc/snapper/configs, so a name long enough to be a problem there is
// refused here instead.
const maxConfigNameLen = 64

// ValidateConfigName rejects anything that is not a plain snapper config name.
// The name reaches snapper as an argv element, so nothing can be injected
// through it, but it also becomes a file name under /etc/snapper/configs and a
// key in the config list, and a name with a slash or a leading dash in it
// produces a confusing failure long after the user confirmed.
func ValidateConfigName(name string) error {
	switch {
	case name == "":
		return fmt.Errorf("a config needs a name")
	case len(name) > maxConfigNameLen:
		return fmt.Errorf("a config name is at most %d characters", maxConfigNameLen)
	case name == "." || name == "..":
		return fmt.Errorf("%q is not a config name", name)
	}
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			continue
		case (r == '_' || r == '-' || r == '.') && i > 0:
			continue
		default:
			return fmt.Errorf(
				"a config name is letters, digits, dot, dash and underscore, "+
					"starting with a letter or a digit — %q is not", name)
		}
	}
	return nil
}

// SuggestConfigName is the name a subvolume usually gets, so the new-config
// form's second prompt is a confirmation rather than an invention. The root
// subvolume is "root" by convention; anything else becomes its path with the
// separators turned into dashes, which keeps /var/log and /srv/log apart.
func SuggestConfigName(subvolume string) string {
	trimmed := strings.Trim(subvolume, "/")
	if trimmed == "" {
		return ProtectedConfig
	}
	name := strings.ReplaceAll(trimmed, "/", "-")
	if ValidateConfigName(name) != nil {
		return ""
	}
	return name
}

// ValidateSubvolumePath rejects a path snapper could not be given, without
// touching the filesystem: that part is CheckSubvolume's job, and this one runs
// in BuildCommand where there may be no filesystem to look at.
func ValidateSubvolumePath(path string) error {
	switch {
	case path == "":
		return fmt.Errorf("a subvolume path cannot be empty")
	case !strings.HasPrefix(path, "/"):
		return fmt.Errorf("a subvolume path is absolute, and %q is not", path)
	case filepath.Clean(path) != path:
		return fmt.Errorf(
			"%q is not a clean path — write it as %s", path, filepath.Clean(path))
	}
	for _, r := range path {
		if r == 0 || unicode.IsControl(r) {
			return fmt.Errorf("a subvolume path cannot contain control characters")
		}
	}
	return nil
}

// ValidateSettingValue reports whether an answer is one snapper accepts for a
// key. The numeric keys take a count or a "min-max" range; the boolean ones
// take exactly yes or no.
func ValidateSettingValue(setting Setting, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("%s cannot be empty", setting.Key)
	}
	if len(setting.Options) > 0 {
		if !allowed(value, setting.Options) {
			return fmt.Errorf("%s is %s, not %q",
				setting.Key, strings.Join(setting.Options, " or "), value)
		}
		return nil
	}
	low, high, isRange := strings.Cut(value, "-")
	if err := validateCount(setting.Key, low); err != nil {
		return err
	}
	if !isRange {
		return nil
	}
	if err := validateCount(setting.Key, high); err != nil {
		return err
	}
	return nil
}

// validateCount accepts a plain non-negative integer, which is one half of a
// limit.
func validateCount(key, value string) error {
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return fmt.Errorf("%s is a whole number of snapshots, or a min-max range", key)
	}
	return nil
}

// ParseConfigSettings reads the two-column table `snapper -c <config>
// get-config` prints:
//
//	Key                    | Value
//	-----------------------+------
//	NUMBER_LIMIT           | 50
//
// It is parsed as text rather than as JSON because --jsonout only grew a
// get-config payload well after the 0.8.6 this tool supports, and the table has
// been stable throughout.
func ParseConfigSettings(out string) map[string]string {
	settings := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		key, value, ok := strings.Cut(line, "|")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		// The heading and the rule under it are not settings.
		if key == "" || key == "Key" || strings.HasPrefix(key, "-") {
			continue
		}
		settings[key] = value
	}
	return settings
}

// Mount is one row of the kernel's mount table, reduced to what deciding
// "is this btrfs" needs.
type Mount struct {
	// Point is the mount point, unescaped.
	Point string
	// FSType is the filesystem type as the kernel names it.
	FSType string
}

// MountInfoPath is where the kernel publishes this process's mount table.
const MountInfoPath = "/proc/self/mountinfo"

// mountInfoFields is the number of fields before the optional ones start, and
// the index of the mount point among them.
const mountPointField = 4

// ParseMountInfo reads /proc/self/mountinfo. The line is
//
//	36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw
//
// with a variable number of optional fields before the " - " separator, so the
// filesystem type is found by splitting on that separator rather than by
// counting from the left.
func ParseMountInfo(out string) []Mount {
	var mounts []Mount
	for _, line := range strings.Split(out, "\n") {
		before, after, ok := strings.Cut(strings.TrimSpace(line), " - ")
		if !ok {
			continue
		}
		left := strings.Fields(before)
		right := strings.Fields(after)
		if len(left) <= mountPointField || len(right) == 0 {
			continue
		}
		point := unescapeMountPath(left[mountPointField])
		// A mount point the kernel escaped a newline or a tab into is dropped
		// rather than carried: no path this tool will accept can match it, and
		// a control character in a row would break the screen rather than one
		// cell. Found by FuzzParseMountInfo.
		if point == "" || strings.ContainsFunc(point, unicode.IsControl) {
			continue
		}
		mounts = append(mounts, Mount{Point: point, FSType: right[0]})
	}
	return mounts
}

// unescapeMountPath decodes the octal escapes the kernel writes for the four
// characters that would otherwise break the field split.
func unescapeMountPath(path string) string {
	replacer := strings.NewReplacer(
		`\040`, " ", `\011`, "\t", `\012`, "\n", `\134`, `\`)
	return replacer.Replace(path)
}

// ReadMounts reads and parses the kernel's mount table.
func ReadMounts(path string) ([]Mount, error) {
	// The path is a constant in production; a test points it at a fixture.
	out, err := os.ReadFile(path) //#nosec G304 -- the caller passes MountInfoPath or a test fixture
	if err != nil {
		return nil, fmt.Errorf("cannot read the mount table: %w", err)
	}
	return ParseMountInfo(string(out)), nil
}

// FilesystemOf returns the type of the filesystem a path lives on, by taking
// the longest mount point that is a prefix of it. A btrfs subvolume is usually
// not a mount point of its own, so asking "is this path itself mounted" would
// answer no for exactly the paths a user wants a config for.
func FilesystemOf(path string, mounts []Mount) (string, bool) {
	best, fstype := -1, ""
	for _, mount := range mounts {
		if !underMount(path, mount.Point) {
			continue
		}
		if len(mount.Point) > best {
			best, fstype = len(mount.Point), mount.FSType
		}
	}
	return fstype, best >= 0
}

// underMount reports whether a path is at or below a mount point.
func underMount(path, point string) bool {
	if point == "/" {
		return strings.HasPrefix(path, "/")
	}
	return path == point || strings.HasPrefix(path, point+"/")
}

// CheckSubvolume reports whether a path can hold a new snapper config: it has
// to exist, be a directory, and live on a mounted btrfs filesystem. Everything
// beyond that — whether the directory is a subvolume rather than an ordinary
// directory — is snapper's own check, and it makes it before it writes
// anything.
func CheckSubvolume(path string, mounts []Mount) error {
	if err := ValidateSubvolumePath(path); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s cannot be read: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	fstype, found := FilesystemOf(path, mounts)
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
