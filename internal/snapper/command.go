package snapper

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/tui-tools/tui-kit/runner"
)

// Action is something the user can do to a config, a snapshot or a range.
type Action string

// The actions v0.1 supports. Every one of them is a mutation: the reads have
// no action table because nothing needs confirming.
const (
	Create      Action = "create"
	Delete      Action = "delete"
	Describe    Action = "modify-description"
	Retention   Action = "modify-cleanup"
	CleanupNow  Action = "cleanup"
	UndoChange  Action = "undochange"
	RollbackNow Action = "rollback"
)

// Scope is what an action applies to, which decides what the app must have
// selected before the key does anything.
type Scope string

// The scopes an action can have.
const (
	// ScopeConfig applies to the whole config: create and cleanup need no
	// snapshot selected.
	ScopeConfig Scope = "config"
	// ScopeSnapshot applies to the selected snapshot, or to the marked
	// range when the user marked one.
	ScopeSnapshot Scope = "snapshot"
	// ScopeRange applies to a pair of snapshots.
	ScopeRange Scope = "range"
	// ScopeFiles applies to a pair of snapshots and a set of paths.
	ScopeFiles Scope = "files"
)

// FieldKind names an extra value an action needs before its command line can
// be assembled.
type FieldKind string

// The values the dialogs collect.
const (
	// FieldDescription is the free-text label of a snapshot.
	FieldDescription FieldKind = "description"
	// FieldCleanup is the cleanup algorithm bound to a snapshot.
	FieldCleanup FieldKind = "cleanup"
	// FieldType is single or pre, for a new snapshot.
	FieldType FieldKind = "type"
	// FieldAlgorithm is the algorithm the standalone cleanup command runs.
	FieldAlgorithm FieldKind = "algorithm"
)

// Field is one value the user is asked for before an action is previewed. A
// Field with options is a picker; one without is a text prompt.
type Field struct {
	Kind FieldKind
	// Title is the prompt's heading.
	Title string
	// Help is the line under the prompt.
	Help string
	// Options, when non-empty, makes this a picker instead of a text input.
	Options []string
	// Optional allows an empty answer, which is how "no cleanup algorithm"
	// and "no description" are expressed.
	Optional bool
}

// CleanupAlgorithms are the algorithms a snapshot can be bound to. The empty
// entry is deliberate and first: a snapshot with no algorithm is never
// removed automatically, which is what a user pinning a known-good state
// wants.
var CleanupAlgorithms = []string{"(none, keep forever)", "number", "timeline"}

// SnapshotTypes are the types `snapper create` can produce on its own. A post
// snapshot needs a pre to belong to, so it is created by pairing rather than
// chosen from this list.
var SnapshotTypes = []string{"single", "pre"}

// CleanupTargets are the algorithms the standalone cleanup command runs.
var CleanupTargets = []string{"number", "timeline", "empty-pre-post"}

// noneOption is the picker label that means "leave this empty".
const noneOption = "(none, keep forever)"

// ActionSpec describes one action for the key map, the help screen and the
// confirm dialog, so the three can never drift apart.
type ActionSpec struct {
	Action Action
	// Key is the key that triggers it.
	Key string
	// Label is the confirm dialog's title.
	Label string
	// Body explains what will happen, shown above the command preview.
	Body string
	// Destructive marks an action that drops state a user may want back, so
	// the dialog is painted in the danger color.
	Destructive bool
	// Scope is what the action applies to.
	Scope Scope
	// Fields are the values collected before the preview, in order.
	Fields []Field
}

// Actions is the full action table, in help-screen order.
var Actions = []ActionSpec{
	{Action: Create, Key: "c", Label: "Create a snapshot", Scope: ScopeConfig,
		Body: "A new snapshot of this config's subvolume is taken now. Nothing that exists is changed.",
		Fields: []Field{
			{Kind: FieldDescription, Title: "Description",
				Help:     "What this snapshot is for. Empty is allowed but rarely useful.",
				Optional: true},
			{Kind: FieldType, Title: "Type", Options: SnapshotTypes,
				Help: "single stands alone; pre is the first half of a pre/post pair."},
			{Kind: FieldCleanup, Title: "Cleanup algorithm", Options: CleanupAlgorithms,
				Help:     "Which algorithm may remove it later. None means it is kept until you delete it.",
				Optional: true},
		}},
	{Action: Delete, Key: "D", Label: "Delete", Scope: ScopeSnapshot, Destructive: true,
		Body: "The snapshot is removed and the space it holds alone is freed. This cannot be undone."},
	{Action: Describe, Key: "e", Label: "Change the description", Scope: ScopeSnapshot,
		Body: "Only the label changes. The snapshot's contents are untouched.",
		Fields: []Field{
			{Kind: FieldDescription, Title: "Description",
				Help: "The new label for this snapshot.", Optional: true},
		}},
	{Action: Retention, Key: "a", Label: "Change the cleanup algorithm", Scope: ScopeSnapshot,
		Body: "This decides which algorithm may remove the snapshot later. It does not delete anything now.",
		Fields: []Field{
			{Kind: FieldCleanup, Title: "Cleanup algorithm", Options: CleanupAlgorithms,
				Help:     "None pins the snapshot: no timer will ever remove it.",
				Optional: true},
		}},
	{Action: CleanupNow, Key: "C", Label: "Run a cleanup", Scope: ScopeConfig, Destructive: true,
		Body: "snapper applies one cleanup algorithm now and deletes every snapshot it decides is expendable.",
		Fields: []Field{
			{Kind: FieldAlgorithm, Title: "Cleanup algorithm", Options: CleanupTargets,
				Help: "number and timeline enforce the config's limits; empty-pre-post drops pairs that changed nothing."},
		}},
	{Action: UndoChange, Key: "u", Label: "Undo the changes to", Scope: ScopeFiles, Destructive: true,
		Body: "The selected paths are written back to the state they had in the first snapshot of the range. Files created since are deleted, deleted files come back."},
	{Action: RollbackNow, Key: "R", Label: "Roll back to", Scope: ScopeSnapshot, Destructive: true,
		Body: "snapper makes this snapshot the default subvolume. The change only takes effect after a reboot, and the running system is snapshotted first."},
}

// ActionFor returns the spec bound to a key.
func ActionFor(key string) (ActionSpec, bool) {
	for _, spec := range Actions {
		if spec.Key == key {
			return spec, true
		}
	}
	return ActionSpec{}, false
}

// Spec returns the spec of an action, for the callers that know which action
// they want rather than which key was pressed.
func Spec(action Action) (ActionSpec, bool) {
	for _, spec := range Actions {
		if spec.Action == action {
			return spec, true
		}
	}
	return ActionSpec{}, false
}

// Request is everything BuildCommand needs besides the spec: which config,
// which snapshots, which paths, and the values the dialogs collected.
type Request struct {
	// Config is the snapper config the command is scoped to. Required.
	Config string
	// Globals are the snapper-wide flags every invocation carries, such as
	// --no-dbus. They belong before the sub-command, which is why they are
	// part of the request rather than something a caller can bolt on later.
	Globals []string
	// Numbers are the snapshots the action applies to, in the order the
	// action reads them. Delete takes one or more; a range action takes
	// exactly two, from and to.
	Numbers []int
	// Files are the paths undochange applies to, exactly as `snapper
	// status` printed them.
	Files []string
	// Values holds the answers to the spec's fields.
	Values map[FieldKind]string
	// Current holds the values a form was seeded with, so a settings command
	// writes only the keys the user actually changed. It is nil for every
	// action that does not read the machine before asking.
	Current map[FieldKind]string
}

// Value reads one collected field.
func (r Request) Value(kind FieldKind) string { return strings.TrimSpace(r.Values[kind]) }

// CurrentValue reads what a field was seeded with.
func (r Request) CurrentValue(kind FieldKind) string {
	return strings.TrimSpace(r.Current[kind])
}

// BuildCommand assembles the snapper invocation for an action. It is the only
// place in the tool where a command line is built, and it is shared by the
// real and the fake backend, so --demo previews exactly the command the real
// thing would run.
func BuildCommand(spec ActionSpec, req Request) (runner.Command, error) {
	if spec.Action == "" {
		return runner.Command{}, fmt.Errorf("no action given")
	}
	if req.Config == "" {
		return runner.Command{}, fmt.Errorf("no config selected")
	}
	argv := WithGlobals(req.Globals, "-c", req.Config)

	switch spec.Action {
	case Create:
		return createCommand(spec, req, argv)
	case Delete:
		return deleteCommand(spec, req, argv)
	case Describe, Retention:
		return modifyCommand(spec, req, argv)
	case CleanupNow:
		return cleanupCommand(spec, req, argv)
	case UndoChange:
		return undoCommand(spec, req, argv)
	case RollbackNow:
		return rollbackCommand(spec, req, argv)
	case CreateConfig:
		return createConfigCommand(spec, req, argv)
	case SetConfig:
		return setConfigCommand(spec, req, argv)
	case DeleteConfig:
		return deleteConfigCommand(spec, req, argv)
	default:
		return runner.Command{}, fmt.Errorf("unknown action %q", spec.Action)
	}
}

// createCommand builds `snapper -c X create [-t pre] [-d …] [-c …]`.
func createCommand(spec ActionSpec, req Request, argv []string) (runner.Command, error) {
	argv = append(argv, "create")
	kind := req.Value(FieldType)
	if kind != "" && kind != TypeSingle {
		if kind != TypePre {
			return runner.Command{}, fmt.Errorf(
				"a snapshot created on its own is single or pre, not %q", kind)
		}
		argv = append(argv, "--type", kind)
	}
	description := req.Value(FieldDescription)
	if description != "" {
		argv = append(argv, "--description", description)
	}
	if algorithm := cleanupValue(req.Value(FieldCleanup)); algorithm != "" {
		argv = append(argv, "--cleanup-algorithm", algorithm)
	}
	label := spec.Label
	if description != "" {
		label += ": " + description
	}
	return runner.Command{Argv: argv, Description: label, Destructive: spec.Destructive}, nil
}

// deleteCommand builds `snapper -c X delete N` or, for several snapshots,
// `snapper -c X delete N M …`. snapper also accepts an `N-M` range, but the
// explicit list is what the confirm dialog can be read against: the user sees
// every number that will go.
func deleteCommand(spec ActionSpec, req Request, argv []string) (runner.Command, error) {
	numbers, err := realNumbers(req.Numbers)
	if err != nil {
		return runner.Command{}, err
	}
	argv = append(argv, "delete")
	for _, n := range numbers {
		argv = append(argv, strconv.Itoa(n))
	}
	return runner.Command{
		Argv:        argv,
		Description: spec.Label + " " + describeNumbers(numbers),
		Destructive: true,
	}, nil
}

// modifyCommand builds `snapper -c X modify -d … N` or `… -c … N`.
func modifyCommand(spec ActionSpec, req Request, argv []string) (runner.Command, error) {
	numbers, err := realNumbers(req.Numbers)
	if err != nil {
		return runner.Command{}, err
	}
	if len(numbers) != 1 {
		return runner.Command{}, fmt.Errorf(
			"modify changes one snapshot at a time, got %d", len(numbers))
	}
	argv = append(argv, "modify")
	switch spec.Action {
	case Describe:
		// An empty description is a deliberate answer: it clears the label.
		argv = append(argv, "--description", req.Value(FieldDescription))
	case Retention:
		argv = append(argv, "--cleanup-algorithm", cleanupValue(req.Value(FieldCleanup)))
	default:
		return runner.Command{}, fmt.Errorf("%q is not a modify action", spec.Action)
	}
	argv = append(argv, strconv.Itoa(numbers[0]))
	return runner.Command{
		Argv:        argv,
		Description: fmt.Sprintf("%s of snapshot %d", spec.Label, numbers[0]),
		Destructive: spec.Destructive,
	}, nil
}

// cleanupCommand builds `snapper -c X cleanup <algorithm>`.
func cleanupCommand(spec ActionSpec, req Request, argv []string) (runner.Command, error) {
	algorithm := req.Value(FieldAlgorithm)
	if !allowed(algorithm, CleanupTargets) {
		return runner.Command{}, fmt.Errorf(
			"unknown cleanup algorithm %q (want %s)",
			algorithm, strings.Join(CleanupTargets, ", "))
	}
	argv = append(argv, "cleanup", algorithm)
	return runner.Command{
		Argv:        argv,
		Description: spec.Label + ": " + algorithm,
		Destructive: true,
	}, nil
}

// undoCommand builds `snapper -c X undochange <from>..<to> <paths…>`.
//
// snapper takes the paths exactly as `snapper status` printed them, so they
// are passed through untouched. Rewriting them relative to the subvolume is
// what makes snapper answer "File '…' not found".
func undoCommand(spec ActionSpec, req Request, argv []string) (runner.Command, error) {
	from, to, err := rangeOf(req.Numbers)
	if err != nil {
		return runner.Command{}, err
	}
	if len(req.Files) == 0 {
		return runner.Command{}, fmt.Errorf("no file selected")
	}
	for _, file := range req.Files {
		if strings.TrimSpace(file) == "" {
			return runner.Command{}, fmt.Errorf("a path cannot be empty")
		}
	}
	argv = append(argv, "undochange", fmt.Sprintf("%d..%d", from, to))
	argv = append(argv, req.Files...)
	return runner.Command{
		Argv: argv,
		Description: fmt.Sprintf("%s %s, back to snapshot %d",
			spec.Label, describeFiles(req.Files), from),
		Destructive: true,
	}, nil
}

// rollbackCommand builds `snapper -c X rollback N`.
func rollbackCommand(spec ActionSpec, req Request, argv []string) (runner.Command, error) {
	numbers, err := realNumbers(req.Numbers)
	if err != nil {
		return runner.Command{}, err
	}
	if len(numbers) != 1 {
		return runner.Command{}, fmt.Errorf(
			"rollback takes one snapshot, got %d", len(numbers))
	}
	argv = append(argv, "rollback", strconv.Itoa(numbers[0]))
	return runner.Command{
		Argv:        argv,
		Description: fmt.Sprintf("%s snapshot %d", spec.Label, numbers[0]),
		Destructive: true,
	}, nil
}

// createConfigCommand builds `snapper -c X create-config -f btrfs <subvolume>`.
//
// The filesystem type is passed explicitly rather than left to snapper's
// default, so the preview says which backend will manage the config instead of
// leaving the reader to know what snapper picks.
func createConfigCommand(spec ActionSpec, req Request, argv []string) (runner.Command, error) {
	if err := ValidateConfigName(req.Config); err != nil {
		return runner.Command{}, err
	}
	subvolume := req.Value(FieldSubvolume)
	if err := ValidateSubvolumePath(subvolume); err != nil {
		return runner.Command{}, err
	}
	argv = append(argv, "create-config", "-f", BtrfsFSType, subvolume)
	return runner.Command{
		Argv: argv,
		Description: fmt.Sprintf("%s %s for %s",
			spec.Label, req.Config, subvolume),
		Destructive: spec.Destructive,
	}, nil
}

// setConfigCommand builds `snapper -c X set-config KEY=VALUE …`.
//
// Only the keys whose answer differs from what get-config reported are
// written. A set-config that repeats every value would work, but the preview
// is what the user reads before saying yes, and a preview naming eight keys
// when one changed is a preview nobody reads.
func setConfigCommand(spec ActionSpec, req Request, argv []string) (runner.Command, error) {
	pairs := make([]string, 0, len(EditableSettings))
	changed := make([]string, 0, len(EditableSettings))
	for _, setting := range EditableSettings {
		value := req.Value(setting.Kind)
		if value == "" {
			// A key the form did not collect is a key this command leaves
			// alone, which is how a partially answered form stays harmless.
			continue
		}
		if err := ValidateSettingValue(setting, value); err != nil {
			return runner.Command{}, err
		}
		if value == req.CurrentValue(setting.Kind) {
			continue
		}
		pairs = append(pairs, setting.Key+"="+value)
		changed = append(changed, setting.Key)
	}
	if len(pairs) == 0 {
		return runner.Command{}, fmt.Errorf("nothing changed, so there is nothing to write")
	}
	argv = append(argv, "set-config")
	argv = append(argv, pairs...)
	return runner.Command{
		Argv: argv,
		Description: fmt.Sprintf("%s of %s: %s",
			spec.Label, req.Config, strings.Join(changed, ", ")),
		Destructive: spec.Destructive,
	}, nil
}

// deleteConfigCommand builds `snapper -c X delete-config`.
//
// The root config is refused outright: deleting it takes the whole history of
// the root filesystem with it, including the entries a limine boot menu offers
// for a rollback, and a snapshot browser is the wrong place to make that
// possible however many dialogs stand in front of it.
func deleteConfigCommand(spec ActionSpec, req Request, argv []string) (runner.Command, error) {
	if err := ValidateConfigName(req.Config); err != nil {
		return runner.Command{}, err
	}
	if req.Config == ProtectedConfig {
		return runner.Command{}, fmt.Errorf(
			"tui-snapper does not delete the %q config: it holds the root "+
				"filesystem's whole history, and the boot menu's snapshot "+
				"entries with it — use `snapper -c %s delete-config` deliberately, "+
				"in a shell, if that is really what you want",
			ProtectedConfig, ProtectedConfig)
	}
	argv = append(argv, "delete-config")
	return runner.Command{
		Argv:        argv,
		Description: spec.Label + " " + req.Config,
		Destructive: true,
	}, nil
}

// GetConfigArgs is the read that returns one config's settings.
func GetConfigArgs(globals []string, config string) []string {
	return WithGlobals(globals, "-c", config, "get-config")
}

// WithGlobals starts an argv: the binary, then the snapper-wide flags, then
// the rest. snapper wants its global options before the sub-command, so this
// is the one place that ordering is decided.
func WithGlobals(globals []string, rest ...string) []string {
	argv := make([]string, 0, 1+len(globals)+len(rest))
	argv = append(argv, "snapper")
	argv = append(argv, globals...)
	return append(argv, rest...)
}

// StatusArgs is the read that lists what changed between two snapshots.
func StatusArgs(globals []string, config string, from, to int) []string {
	return WithGlobals(globals, "-c", config, "status", fmt.Sprintf("%d..%d", from, to))
}

// DiffArgs is the read that shows one path's unified diff between two
// snapshots. snapper's own usage is `snapper diff <n1>..<n2> [files]`: there
// is no `--` separator, and passing one makes snapper look for a file called
// "--".
func DiffArgs(globals []string, config string, from, to int, path string) []string {
	return WithGlobals(globals,
		"-c", config, "diff", fmt.Sprintf("%d..%d", from, to), path)
}

// ListArgs is the read that returns the snapshots of a config as JSON.
func ListArgs(globals []string, config string) []string {
	return WithGlobals(globals, "--jsonout", "-c", config, "list")
}

// ListConfigsArgs is the read that returns every config as JSON.
func ListConfigsArgs(globals []string) []string {
	return WithGlobals(globals, "--jsonout", "list-configs")
}

// realNumbers rejects an empty selection and snapshot 0, which is the live
// subvolume rather than a snapshot: nothing can delete or modify it, and
// letting the command through would only produce a confusing snapper error
// after the user had already confirmed.
func realNumbers(numbers []int) ([]int, error) {
	if len(numbers) == 0 {
		return nil, fmt.Errorf("no snapshot selected")
	}
	out := make([]int, 0, len(numbers))
	seen := map[int]bool{}
	for _, n := range numbers {
		if n == 0 {
			return nil, fmt.Errorf(
				"snapshot 0 is the live subvolume, not a snapshot")
		}
		if n < 0 {
			return nil, fmt.Errorf("%d is not a snapshot number", n)
		}
		if seen[n] {
			continue
		}
		seen[n] = true
		out = append(out, n)
	}
	sort.Ints(out)
	return out, nil
}

// rangeOf reads a from/to pair. Snapshot 0 is allowed here: comparing against
// the live subvolume is exactly what `status 5..0` is for.
func rangeOf(numbers []int) (from, to int, err error) {
	if len(numbers) != 2 {
		return 0, 0, fmt.Errorf("a range needs two snapshots, got %d", len(numbers))
	}
	if numbers[0] < 0 || numbers[1] < 0 {
		return 0, 0, fmt.Errorf("a snapshot number cannot be negative")
	}
	if numbers[0] == numbers[1] {
		return 0, 0, fmt.Errorf("a range needs two different snapshots")
	}
	return numbers[0], numbers[1], nil
}

// cleanupValue maps the picker's "none" label to the empty string snapper
// expects, and passes every real algorithm through.
func cleanupValue(value string) string {
	if value == noneOption {
		return ""
	}
	return value
}

// allowed reports whether a value is one of a fixed set.
func allowed(value string, set []string) bool {
	for _, candidate := range set {
		if value == candidate {
			return true
		}
	}
	return false
}

// describeNumbers renders a snapshot list for a dialog title, so the user
// reads every number that is about to go.
func describeNumbers(numbers []int) string {
	if len(numbers) == 1 {
		return "snapshot " + strconv.Itoa(numbers[0])
	}
	parts := make([]string, 0, len(numbers))
	for _, n := range numbers {
		parts = append(parts, strconv.Itoa(n))
	}
	return "snapshots " + strings.Join(parts, ", ")
}

// describeFiles renders a path list for a dialog title without letting a long
// selection push the command preview off the screen.
func describeFiles(files []string) string {
	if len(files) == 1 {
		return files[0]
	}
	return fmt.Sprintf("%d files", len(files))
}
