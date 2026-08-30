package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/runner"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-snapper/internal/snapper"
)

// mode is the screen the app currently shows. Only one is open at a time,
// which keeps the update loop flat.
type mode int

const (
	modeSnapshots mode = iota
	modeConfigs
	modeDiff
	modeFile
	modeRollback
	modeTimers
	modeConfirm
	modeInput
	modePick
	modeFilter
	modeHelp
)

// readTimeout bounds a background read so a stuck command cannot wedge the UI.
const readTimeout = 45 * time.Second

// actionTimeout bounds a mutation. Deleting several snapshots at once is the
// slow case, and it is worth waiting for rather than reporting a timeout on a
// command that is still working.
const actionTimeout = 6 * time.Minute

// pending is an action that has been asked for but not yet previewed: the
// fields still to collect, and the request built so far. When the last field
// is answered the command is built and the confirm dialog opens, which is the
// only path to a change.
type pending struct {
	spec snapper.ActionSpec
	req  snapper.Request
	// fields are the values still to collect, in order.
	fields []snapper.Field
	// returnTo is the screen to go back to once the action resolves.
	returnTo mode
}

// app is the tui-snapper Bubble Tea model.
type app struct {
	backend snapper.Backend
	theme   theme.Theme

	configs   []snapper.Config
	config    snapper.Config
	snapshots []snapper.Snapshot
	visible   []snapper.Snapshot
	timers    []snapper.TimerState
	platform  snapper.Platform

	// backendCompat is what the version probe found, rendered in the header
	// and consulted for the capabilities the running snapper has.
	backendCompat compat.Result

	width, height int
	// cursor and offset drive the snapshot list; the diff view and the file
	// panel keep their own, so leaving and re-entering a screen does not
	// lose the reader's place.
	cursor, offset         int
	diffCursor, diffOffset int
	fileOffset             int
	configCursor           int
	filter                 string

	// marks are the snapshot numbers the user selected with space. Two marks
	// define a range; any number of them can be deleted at once.
	marks map[int]bool

	// The diff view's state: which range it compares, what changed, and
	// which paths are marked for undochange.
	diffFrom, diffTo int
	changes          []snapper.Change
	visibleChanges   []snapper.Change
	diffFilter       string
	fileMarks        map[string]bool
	// The file panel's state.
	filePath string
	fileText string

	mode     mode
	prevMode mode
	confirm  ui.Confirm
	input    ui.Input
	picker   ui.Picker
	pending  *pending
	// field is the field the open input or picker is collecting.
	field snapper.Field

	status     string
	statusKind ui.StatusKind
	loading    bool
	// loadFailed reports that the last read failed, so the empty state does
	// not claim the machine simply has no snapshots.
	loadFailed bool
	// busy blocks input while a command runs.
	busy bool
	// settled records that the first read has placed the cursor, so a later
	// re-read does not move the user's selection back.
	settled bool
}

// configsMsg carries the result of a config list read.
type configsMsg struct {
	configs []snapper.Config
	err     error
}

// snapshotsMsg carries the result of a snapshot list read.
type snapshotsMsg struct {
	config    string
	snapshots []snapper.Snapshot
	err       error
}

// statusMsg carries the result of a comparison read.
type statusMsg struct {
	from, to int
	changes  []snapper.Change
	err      error
}

// diffMsg carries the result of one path's diff read.
type diffMsg struct {
	path string
	text string
	err  error
}

// timersMsg carries the state of snapper's systemd timers.
type timersMsg struct{ timers []snapper.TimerState }

// platformMsg carries how this machine rolls back.
type platformMsg struct{ platform snapper.Platform }

// ranMsg carries the result of a Run.
type ranMsg struct {
	cmd    runner.Command
	output string
	err    error
}

// newApp builds the model around a backend. wanted is the config named on the
// command line, and is empty when the tool should pick one itself.
// backendCompat is what the startup version probe found; the zero value is a
// tool that could not read a version, which renders no badge and gates
// nothing.
func newApp(backend snapper.Backend, th theme.Theme, wanted string,
	backendCompat compat.Result) *app {
	a := &app{
		backend:       backend,
		theme:         th,
		width:         80,
		height:        24,
		loading:       true,
		marks:         map[int]bool{},
		fileMarks:     map[string]bool{},
		config:        snapper.Config{Name: wanted},
		backendCompat: backendCompat,
	}
	if th.Warning != "" {
		a.setStatus(ui.StatusWarn, th.Warning)
	}
	return a
}

// Init starts the first read.
func (a *app) Init() tea.Cmd { return a.loadConfigs() }

// loadConfigs reads the config list in the background.
func (a *app) loadConfigs() tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		configs, err := backend.Configs(ctx)
		return configsMsg{configs: configs, err: err}
	}
}

// loadSnapshots reads one config's snapshots in the background.
func (a *app) loadSnapshots() tea.Cmd {
	backend, name := a.backend, a.config.Name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		snapshots, err := backend.Snapshots(ctx, name)
		return snapshotsMsg{config: name, snapshots: snapshots, err: err}
	}
}

// loadStatus reads what changed between two snapshots.
func (a *app) loadStatus(from, to int) tea.Cmd {
	backend, name := a.backend, a.config.Name
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		changes, err := backend.Status(ctx, name, from, to)
		return statusMsg{from: from, to: to, changes: changes, err: err}
	}
}

// loadDiff reads one path's unified diff.
func (a *app) loadDiff(path string) tea.Cmd {
	backend, name := a.backend, a.config.Name
	from, to := a.diffFrom, a.diffTo
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		text, err := backend.Diff(ctx, name, from, to, path)
		return diffMsg{path: path, text: text, err: err}
	}
}

// loadTimers reads the timer states.
func (a *app) loadTimers() tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		return timersMsg{timers: backend.Timers(ctx)}
	}
}

// loadPlatform detects how this machine rolls back.
func (a *app) loadPlatform() tea.Cmd {
	backend, config := a.backend, a.config
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), readTimeout)
		defer cancel()
		return platformMsg{platform: backend.Platform(ctx, config)}
	}
}

// run executes a confirmed command in the background.
func (a *app) run(cmd runner.Command) tea.Cmd {
	backend := a.backend
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		out, err := backend.Run(ctx, cmd)
		return ranMsg{cmd: cmd, output: out, err: err}
	}
}

// setStatus records a message for the status line.
func (a *app) setStatus(kind ui.StatusKind, message string) {
	a.status = message
	a.statusKind = kind
}

// setStatusf records a formatted message for the status line.
func (a *app) setStatusf(kind ui.StatusKind, format string, args ...any) {
	a.setStatus(kind, fmt.Sprintf(format, args...))
}

// Update is the main event loop.
func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width, a.height = msg.Width, msg.Height
		a.clampCursor()
		return a, nil

	case configsMsg:
		return a.onConfigs(msg)

	case snapshotsMsg:
		a.loading = false
		// A late answer for a config the user has already left must not
		// overwrite the list now on screen.
		if msg.config != a.config.Name {
			return a, nil
		}
		if msg.err != nil {
			a.loadFailed = true
			a.snapshots = nil
			a.applyFilter()
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, nil
		}
		a.loadFailed = false
		a.snapshots = msg.snapshots
		a.applyFilter()
		a.settleCursor()
		return a, nil

	case statusMsg:
		a.loading = false
		if msg.from != a.diffFrom || msg.to != a.diffTo {
			return a, nil
		}
		if msg.err != nil {
			a.changes = nil
			a.applyDiffFilter()
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, nil
		}
		a.changes = msg.changes
		a.applyDiffFilter()
		return a, nil

	case diffMsg:
		a.loading = false
		if msg.path != a.filePath {
			return a, nil
		}
		if msg.err != nil {
			a.fileText = ""
			a.setStatus(ui.StatusError, msg.err.Error())
			return a, nil
		}
		a.fileText = msg.text
		a.fileOffset = 0
		return a, nil

	case timersMsg:
		a.loading = false
		a.timers = msg.timers
		return a, nil

	case platformMsg:
		a.loading = false
		a.platform = msg.platform
		return a, nil

	case ranMsg:
		return a.onRan(msg)

	case tea.KeyMsg:
		return a.handleKey(msg)
	}

	// Anything else (cursor blink, …) only concerns an open text input.
	if a.mode == modeInput || a.mode == modeFilter {
		cmd, _ := a.input.Update(msg)
		return a, cmd
	}
	return a, nil
}

// onConfigs settles which config the tool opens on, then reads it.
func (a *app) onConfigs(msg configsMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		a.loading = false
		a.loadFailed = true
		a.setStatus(ui.StatusError, msg.err.Error())
		return a, nil
	}
	a.configs = msg.configs
	if len(a.configs) == 0 {
		a.loading = false
		a.loadFailed = true
		a.setStatus(ui.StatusWarn,
			"snapper has no configs on this machine — `snapper create-config <subvolume>` makes one")
		return a, nil
	}
	if picked, ok := a.pickConfig(a.config.Name); ok {
		a.config = picked
	} else {
		if a.config.Name != "" {
			a.setStatusf(ui.StatusWarn,
				"no config called %q; showing %s instead", a.config.Name, a.configs[0].Name)
		}
		a.config = a.configs[0]
	}
	a.loading = true
	return a, tea.Batch(a.loadSnapshots(), a.loadTimers(), a.loadPlatform())
}

// pickConfig finds a config by name.
func (a *app) pickConfig(name string) (snapper.Config, bool) {
	for _, config := range a.configs {
		if config.Name == name {
			return config, true
		}
	}
	return snapper.Config{}, false
}

// onRan reports a finished command and re-reads, because the filesystem is
// the source of truth rather than what the tool assumed would happen.
func (a *app) onRan(msg ranMsg) (tea.Model, tea.Cmd) {
	a.busy = false
	a.marks = map[int]bool{}
	a.fileMarks = map[string]bool{}
	if msg.err != nil {
		a.setStatus(ui.StatusError, msg.err.Error())
		return a, a.loadSnapshots()
	}
	summary := strings.TrimSpace(msg.output)
	if summary == "" {
		summary = "done"
	}
	a.setStatusf(ui.StatusOK, "%s: %s", msg.cmd.Description, runner.FirstLine(summary))
	a.loading = true
	// A change to a snapshot can change what a comparison shows, so the open
	// diff view is re-read alongside the list.
	if a.mode == modeDiff && a.diffFrom != a.diffTo {
		return a, tea.Batch(a.loadSnapshots(), a.loadStatus(a.diffFrom, a.diffTo))
	}
	return a, a.loadSnapshots()
}

// handleKey routes a key press to the open screen.
func (a *app) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ctrl+c always quits, even mid-dialog.
	if msg.Type == tea.KeyCtrlC {
		return a, tea.Quit
	}
	if a.busy {
		// A command is running: swallow input rather than queueing surprises.
		return a, nil
	}

	switch a.mode {
	case modeConfirm:
		return a.handleConfirm(msg)
	case modeInput:
		return a.handleFieldInput(msg)
	case modePick:
		return a.handlePicker(msg)
	case modeFilter:
		return a.handleFilter(msg)
	case modeHelp:
		a.mode = a.prevMode
		return a, nil
	case modeConfigs:
		return a.handleConfigsKey(msg)
	case modeDiff:
		return a.handleDiffKey(msg)
	case modeFile:
		return a.handleFileKey(msg)
	case modeRollback:
		return a.handleRollbackKey(msg)
	case modeTimers:
		return a.handleTimersKey(msg)
	default:
		return a.handleSnapshotsKey(msg)
	}
}

// handleConfirm resolves the confirm dialog. This is the only path to a
// change.
func (a *app) handleConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a.confirm.Update(msg)
	if !a.confirm.Done {
		return a, nil
	}
	a.mode = a.returnMode()
	confirmed := a.confirm.Confirmed
	cmd, ok := a.confirm.Payload.(runner.Command)
	a.confirm = ui.Confirm{}
	a.pending = nil
	if !confirmed || !ok {
		a.setStatus(ui.StatusInfo, "cancelled")
		return a, nil
	}
	a.busy = true
	a.setStatusf(ui.StatusInfo, "running %s…", a.backend.Preview(cmd))
	return a, a.run(cmd)
}

// handleFieldInput collects a free-text field of a pending action.
func (a *app) handleFieldInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd, _ := a.input.Update(msg)
	if !a.input.Done {
		return a, cmd
	}
	if !a.input.Accepted {
		return a, a.cancelPending()
	}
	value := a.input.Value()
	if value == "" && !a.field.Optional {
		a.setStatusf(ui.StatusWarn, "%s cannot be empty", strings.ToLower(a.field.Title))
		return a, a.cancelPending()
	}
	a.collect(a.field.Kind, value)
	return a, a.advance()
}

// handlePicker collects a fixed-choice field of a pending action.
func (a *app) handlePicker(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	a.picker.Update(msg)
	if !a.picker.Done {
		return a, nil
	}
	if !a.picker.Accepted {
		return a, a.cancelPending()
	}
	a.collect(a.field.Kind, a.picker.Selected())
	return a, a.advance()
}

// handleFilter resolves the filter prompt of whichever list is open.
func (a *app) handleFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	cmd, _ := a.input.Update(msg)
	target := a.returnMode()
	set := func(value string) {
		if target == modeDiff {
			a.diffFilter = value
			a.applyDiffFilter()
			return
		}
		a.filter = value
		a.applyFilter()
	}
	if !a.input.Done {
		// Filter as the user types.
		set(a.input.Value())
		return a, cmd
	}
	if a.input.Accepted {
		set(a.input.Value())
	} else {
		set("")
	}
	a.mode = target
	return a, nil
}

// handleSnapshotsKey handles the main screen.
func (a *app) handleSnapshotsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// An action key is checked before navigation, so the action table is the
	// single source of truth for what each key does.
	if spec, ok := snapper.ActionFor(key); ok {
		// Rollback is the one action that opens a screen first. What it
		// means differs between an openSUSE-style layout and an Omarchy one,
		// and nobody should confirm it without reading which one they are on.
		if spec.Action == snapper.RollbackNow {
			return a, a.openRollback()
		}
		return a, a.startAction(spec, modeSnapshots)
	}

	switch key {
	case "q", "esc":
		return a, tea.Quit
	case "?":
		a.prevMode = modeSnapshots
		a.mode = modeHelp
	case "down", "j", "ctrl+n":
		a.moveCursor(1)
	case "up", "k", "ctrl+p":
		a.moveCursor(-1)
	case "g", "home":
		a.cursor, a.offset = 0, 0
	case "G", "end":
		a.cursor = max(len(a.visible)-1, 0)
		a.clampCursor()
	case "pgdown", "ctrl+f":
		a.moveCursor(a.listHeight())
	case "pgup", "ctrl+b":
		a.moveCursor(-a.listHeight())
	case " ":
		a.toggleMark()
	case "/":
		a.openFilter("Filter snapshots",
			"number, type, description, user, cleanup…", a.filter, modeSnapshots)
	case "r", "ctrl+r":
		a.loading = true
		return a, tea.Batch(a.loadSnapshots(), a.loadTimers())
	case "s":
		return a, a.openConfigs()
	case "d", "enter":
		return a, a.openDiff()
	case "T":
		a.prevMode = modeSnapshots
		a.mode = modeTimers
		a.loading = true
		return a, a.loadTimers()
	}
	return a, nil
}

// handleConfigsKey handles the config picker.
func (a *app) handleConfigsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "s":
		a.mode = modeSnapshots
		return a, nil
	case "?":
		a.prevMode = modeConfigs
		a.mode = modeHelp
		return a, nil
	case "down", "j", "ctrl+n":
		a.configCursor = min(a.configCursor+1, max(len(a.configs)-1, 0))
	case "up", "k", "ctrl+p":
		a.configCursor = max(a.configCursor-1, 0)
	case "g", "home":
		a.configCursor = 0
	case "G", "end":
		a.configCursor = max(len(a.configs)-1, 0)
	case "enter":
		if a.configCursor < 0 || a.configCursor >= len(a.configs) {
			return a, nil
		}
		a.config = a.configs[a.configCursor]
		a.mode = modeSnapshots
		a.resetForConfig()
		a.loading = true
		return a, tea.Batch(a.loadSnapshots(), a.loadPlatform())
	}
	return a, nil
}

// handleDiffKey handles the list of what changed between two snapshots.
func (a *app) handleDiffKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == snapper.Actions[undoIndex()].Key {
		return a, a.startUndo()
	}

	switch key {
	case "esc", "q", "d":
		a.mode = modeSnapshots
		return a, nil
	case "?":
		a.prevMode = modeDiff
		a.mode = modeHelp
		return a, nil
	case "down", "j", "ctrl+n":
		a.moveDiffCursor(1)
	case "up", "k", "ctrl+p":
		a.moveDiffCursor(-1)
	case "g", "home":
		a.diffCursor, a.diffOffset = 0, 0
	case "G", "end":
		a.diffCursor = max(len(a.visibleChanges)-1, 0)
		a.clampCursor()
	case "pgdown", "ctrl+f":
		a.moveDiffCursor(a.listHeight())
	case "pgup", "ctrl+b":
		a.moveDiffCursor(-a.listHeight())
	case " ":
		a.toggleFileMark()
	case "/":
		a.openFilter("Filter changes", "path or kind…", a.diffFilter, modeDiff)
	case "r", "ctrl+r":
		a.loading = true
		return a, a.loadStatus(a.diffFrom, a.diffTo)
	case "enter":
		return a, a.openFile()
	}
	return a, nil
}

// handleFileKey handles the read-only diff panel for one path.
func (a *app) handleFileKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	height := a.listHeight()
	switch msg.String() {
	case "esc", "q", "enter":
		a.mode = modeDiff
		return a, nil
	case "?":
		a.prevMode = modeFile
		a.mode = modeHelp
		return a, nil
	case "down", "j", "ctrl+n":
		a.scrollFile(1)
	case "up", "k", "ctrl+p":
		a.scrollFile(-1)
	case "pgdown", "ctrl+f", " ":
		a.scrollFile(height)
	case "pgup", "ctrl+b":
		a.scrollFile(-height)
	case "g", "home":
		a.fileOffset = 0
	case "G", "end":
		a.fileOffset = max(len(a.fileLines())-height, 0)
	case "r", "ctrl+r":
		a.loading = true
		return a, a.loadDiff(a.filePath)
	}
	return a, nil
}

// handleRollbackKey handles the rollback screen. The confirm dialog is only
// reachable on a layout where `snapper rollback` is the real mechanism.
func (a *app) handleRollbackKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "R":
		a.mode = modeSnapshots
		return a, nil
	case "?":
		a.prevMode = modeRollback
		a.mode = modeHelp
		return a, nil
	case "r", "ctrl+r":
		a.loading = true
		return a, a.loadPlatform()
	case "enter":
		if a.platform.Kind != snapper.RollbackSnapper {
			a.setStatus(ui.StatusWarn,
				"this machine does not roll back with snapper — see the note above")
			return a, nil
		}
		spec, ok := snapper.Spec(snapper.RollbackNow)
		if !ok {
			return a, nil
		}
		return a, a.startAction(spec, modeRollback)
	}
	return a, nil
}

// handleTimersKey handles the read-only timer screen.
func (a *app) handleTimersKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "q", "T":
		a.mode = modeSnapshots
		return a, nil
	case "?":
		a.prevMode = modeTimers
		a.mode = modeHelp
		return a, nil
	case "r", "ctrl+r":
		a.loading = true
		return a, a.loadTimers()
	}
	return a, nil
}

// undoIndex is the position of the undochange action in the table, so the
// diff view binds the same key the help screen advertises.
func undoIndex() int {
	for i, spec := range snapper.Actions {
		if spec.Action == snapper.UndoChange {
			return i
		}
	}
	return 0
}

// startAction begins an action: it fills in what the selection implies, then
// collects the fields the spec asks for. The command is built and previewed
// only once every field has an answer.
func (a *app) startAction(spec snapper.ActionSpec, returnTo mode) tea.Cmd {
	if a.config.Name == "" {
		a.setStatus(ui.StatusWarn, "no config selected")
		return nil
	}
	req := snapper.Request{Config: a.config.Name, Values: map[snapper.FieldKind]string{}}

	switch spec.Scope {
	case snapper.ScopeSnapshot:
		numbers, ok := a.targetNumbers(spec)
		if !ok {
			return nil
		}
		req.Numbers = numbers
	case snapper.ScopeFiles:
		a.setStatus(ui.StatusWarn, "undo works on a file in the diff view")
		return nil
	}

	a.pending = &pending{spec: spec, req: req, fields: spec.Fields, returnTo: returnTo}
	// A prompt starts from what the snapshot already says, so answering it is
	// editing rather than retyping.
	a.seedFields()
	return a.advance()
}

// startUndo begins an undochange for the marked paths, or for the selected
// one when nothing is marked.
func (a *app) startUndo() tea.Cmd {
	spec, ok := snapper.Spec(snapper.UndoChange)
	if !ok {
		return nil
	}
	files := a.markedFiles()
	if len(files) == 0 {
		change, ok := a.selectedChange()
		if !ok {
			a.setStatus(ui.StatusWarn, "no file selected")
			return nil
		}
		files = []string{change.Path}
	}
	a.pending = &pending{
		spec: spec,
		req: snapper.Request{
			Config:  a.config.Name,
			Numbers: []int{a.diffFrom, a.diffTo},
			Files:   files,
			Values:  map[snapper.FieldKind]string{},
		},
		fields:   spec.Fields,
		returnTo: modeDiff,
	}
	return a.advance()
}

// targetNumbers is which snapshots a snapshot-scoped action applies to: the
// marked ones when the user marked any, otherwise the selected row.
func (a *app) targetNumbers(spec snapper.ActionSpec) ([]int, bool) {
	// Only delete acts on several snapshots at once; modifying and rolling
	// back are single-snapshot operations, and silently picking one of
	// several marks would be a surprise.
	if marked := a.markedNumbers(); len(marked) > 0 && spec.Action == snapper.Delete {
		return marked, true
	}
	snapshot, ok := a.selected()
	if !ok {
		a.setStatus(ui.StatusWarn, "no snapshot selected")
		return nil, false
	}
	if snapshot.Current() {
		a.setStatus(ui.StatusWarn,
			"snapshot 0 is the live subvolume — pick a real snapshot")
		return nil, false
	}
	return []int{snapshot.Number}, true
}

// seedFields fills a pending action's prompts with the values the selected
// snapshot already carries.
func (a *app) seedFields() {
	if a.pending == nil || len(a.pending.req.Numbers) != 1 {
		return
	}
	snapshot, ok := a.snapshotNumbered(a.pending.req.Numbers[0])
	if !ok {
		return
	}
	switch a.pending.spec.Action {
	case snapper.Describe:
		a.pending.req.Values[snapper.FieldDescription] = snapshot.Description
	case snapper.Retention:
		a.pending.req.Values[snapper.FieldCleanup] = snapshot.Cleanup
	}
}

// advance collects the next field, or builds and previews the command when
// there are none left.
func (a *app) advance() tea.Cmd {
	if a.pending == nil {
		return nil
	}
	if len(a.pending.fields) == 0 {
		return a.previewPending()
	}
	a.field = a.pending.fields[0]
	a.pending.fields = a.pending.fields[1:]
	current := a.pending.req.Values[a.field.Kind]

	if len(a.field.Options) == 0 {
		a.input = ui.NewInput(a.field.Title, "…", current)
		a.input.Help = a.field.Help
		a.mode = modeInput
		return a.input.Model.Focus()
	}
	a.picker = ui.NewPicker(a.field.Title, a.field.Options, pickerValue(a.field, current))
	a.mode = modePick
	return nil
}

// pickerValue maps a stored value onto the option that represents it, so a
// snapshot with no cleanup algorithm opens on the "none" entry rather than on
// the first one.
func pickerValue(field snapper.Field, current string) string {
	if current != "" {
		return current
	}
	if field.Kind == snapper.FieldCleanup {
		return snapper.CleanupAlgorithms[0]
	}
	return ""
}

// collect records one answer.
func (a *app) collect(kind snapper.FieldKind, value string) {
	if a.pending == nil {
		return
	}
	a.pending.req.Values[kind] = value
}

// previewPending builds the command and opens the confirm dialog.
func (a *app) previewPending() tea.Cmd {
	p := a.pending
	cmd, err := a.backend.Build(p.spec, p.req)
	if err != nil {
		a.mode = p.returnTo
		a.pending = nil
		a.setStatus(ui.StatusError, err.Error())
		return nil
	}
	a.mode = modeConfirm
	a.confirm = ui.Confirm{
		Title:   cmd.Description,
		Body:    a.confirmBody(p),
		Command: a.backend.Preview(cmd),
		Danger:  cmd.Destructive,
		Payload: cmd,
	}
	return nil
}

// confirmBody is the action's own explanation, plus the caveat that applies to
// the snapper this machine is running.
//
// The only one today is gh#openSUSE/snapper#168, fixed in 0.10.6: undochange
// cannot recreate a path whose parent is the subvolume root, and it fails
// after the user confirmed rather than before. The version that has it is
// named in the manifest, not here, so a capability the probe could not read is
// assumed present and the dialog stays quiet.
func (a *app) confirmBody(p *pending) string {
	body := p.spec.Body
	if p.spec.Action != snapper.UndoChange {
		return body
	}
	if a.backendCompat.Caps().Has("undochange-root-paths") {
		return body
	}
	if !anyRootPath(p.req.Files) {
		return body
	}
	return body + "\n\nThis snapper is older than 0.10.6, where `undochange` " +
		"could not recreate a path directly under the subvolume root: a " +
		"selected top-level path may come back as \"failed to create\"."
}

// anyRootPath reports whether a path sits directly under the subvolume root,
// which is the shape the pre-0.10.6 undochange could not recreate.
func anyRootPath(files []string) bool {
	for _, file := range files {
		trimmed := strings.TrimPrefix(file, "/")
		if trimmed != "" && !strings.Contains(trimmed, "/") {
			return true
		}
	}
	return false
}

// cancelPending abandons a half-collected action.
func (a *app) cancelPending() tea.Cmd {
	a.mode = a.returnMode()
	a.pending = nil
	a.setStatus(ui.StatusInfo, "cancelled")
	return nil
}

// returnMode is the screen a dialog goes back to.
func (a *app) returnMode() mode {
	if a.pending != nil {
		return a.pending.returnTo
	}
	if a.prevMode == modeDiff || a.prevMode == modeSnapshots {
		return a.prevMode
	}
	return modeSnapshots
}

// openFilter opens the filter prompt for one of the lists.
func (a *app) openFilter(title, placeholder, value string, target mode) {
	a.input = ui.NewInput(title, placeholder, value)
	a.input.Help = "Empty clears the filter."
	a.prevMode = target
	a.mode = modeFilter
}

// openConfigs shows the config picker.
func (a *app) openConfigs() tea.Cmd {
	if len(a.configs) == 0 {
		a.setStatus(ui.StatusWarn, "no configs to choose from")
		return nil
	}
	a.configCursor = 0
	for i, config := range a.configs {
		if config.Name == a.config.Name {
			a.configCursor = i
		}
	}
	a.mode = modeConfigs
	return nil
}

// openDiff compares the selected snapshot with the one the user marked, or
// with the most sensible neighbour when nothing is marked.
func (a *app) openDiff() tea.Cmd {
	from, to, ok := a.diffRange()
	if !ok {
		return nil
	}
	a.diffFrom, a.diffTo = from, to
	a.changes, a.visibleChanges = nil, nil
	a.diffCursor, a.diffOffset = 0, 0
	a.diffFilter = ""
	a.fileMarks = map[string]bool{}
	a.mode = modeDiff
	a.loading = true
	return a.loadStatus(from, to)
}

// diffRange decides which two snapshots to compare.
//
// Two marks are an explicit answer. Otherwise a post snapshot is compared
// with its own pre, which is the comparison anyone opening a package upgrade
// wants; any other snapshot is compared with the one before it, and the
// oldest with the live subvolume.
func (a *app) diffRange() (from, to int, ok bool) {
	marked := a.markedNumbers()
	if len(marked) == 2 {
		return marked[0], marked[1], true
	}
	if len(marked) > 2 {
		a.setStatus(ui.StatusWarn,
			"a comparison takes two snapshots — unmark the rest with space")
		return 0, 0, false
	}
	snapshot, found := a.selected()
	if !found {
		a.setStatus(ui.StatusWarn, "no snapshot selected")
		return 0, 0, false
	}
	if snapshot.Type == snapper.TypePost && snapshot.PreNumber != 0 {
		return snapshot.PreNumber, snapshot.Number, true
	}
	if previous, ok := a.previousNumber(snapshot.Number); ok {
		return previous, snapshot.Number, true
	}
	if snapshot.Current() {
		a.setStatus(ui.StatusWarn,
			"there is nothing older than the live subvolume to compare it with")
		return 0, 0, false
	}
	// The oldest snapshot has no predecessor, so it is compared with the
	// live subvolume: "what changed since then".
	return snapshot.Number, 0, true
}

// previousNumber is the highest snapshot number below n, and whether there is
// one.
func (a *app) previousNumber(n int) (int, bool) {
	best, found := 0, false
	for _, s := range a.snapshots {
		if s.Current() || s.Number >= n {
			continue
		}
		if !found || s.Number > best {
			best, found = s.Number, true
		}
	}
	return best, found
}

// openFile shows one path's diff.
func (a *app) openFile() tea.Cmd {
	change, ok := a.selectedChange()
	if !ok {
		a.setStatus(ui.StatusWarn, "no file selected")
		return nil
	}
	a.filePath = change.Path
	a.fileText = ""
	a.fileOffset = 0
	a.mode = modeFile
	a.loading = true
	return a.loadDiff(change.Path)
}

// openRollback shows what a rollback means on this machine.
func (a *app) openRollback() tea.Cmd {
	a.mode = modeRollback
	if a.platform.Kind == "" {
		a.loading = true
		return a.loadPlatform()
	}
	return nil
}

// resetForConfig clears everything that belonged to the previous config.
func (a *app) resetForConfig() {
	a.snapshots, a.visible = nil, nil
	a.changes, a.visibleChanges = nil, nil
	a.cursor, a.offset = 0, 0
	a.diffCursor, a.diffOffset = 0, 0
	a.filter, a.diffFilter = "", ""
	a.marks = map[int]bool{}
	a.fileMarks = map[string]bool{}
	a.platform = snapper.Platform{}
	a.loadFailed = false
	a.settled = false
}

// settleCursor puts the selection on the newest real snapshot the first time
// a config is read. Row 0 is the live subvolume, and nothing can be done to
// it: opening with it selected would make every action key answer "pick a
// real snapshot" until the user pressed down.
func (a *app) settleCursor() {
	if a.settled || a.cursor != 0 || len(a.visible) == 0 {
		return
	}
	a.settled = true
	if a.visible[0].Current() && len(a.visible) > 1 {
		a.cursor = 1
		a.clampCursor()
	}
}

// toggleMark marks or unmarks the selected snapshot.
func (a *app) toggleMark() {
	snapshot, ok := a.selected()
	if !ok {
		return
	}
	if a.marks[snapshot.Number] {
		delete(a.marks, snapshot.Number)
	} else {
		a.marks[snapshot.Number] = true
	}
	a.moveCursor(1)
}

// toggleFileMark marks or unmarks the selected path in the diff view.
func (a *app) toggleFileMark() {
	change, ok := a.selectedChange()
	if !ok {
		return
	}
	if a.fileMarks[change.Path] {
		delete(a.fileMarks, change.Path)
	} else {
		a.fileMarks[change.Path] = true
	}
	a.moveDiffCursor(1)
}

// markedNumbers is the marked snapshots, in ascending order.
func (a *app) markedNumbers() []int {
	numbers := make([]int, 0, len(a.marks))
	for number := range a.marks {
		numbers = append(numbers, number)
	}
	sort.Ints(numbers)
	return numbers
}

// markedFiles is the marked paths, in the order the diff view lists them, so
// the confirm dialog reads the way the screen does.
func (a *app) markedFiles() []string {
	files := make([]string, 0, len(a.fileMarks))
	for _, change := range a.changes {
		if a.fileMarks[change.Path] {
			files = append(files, change.Path)
		}
	}
	return files
}

// snapshotNumbered finds a snapshot by number in the last read.
func (a *app) snapshotNumbered(number int) (snapper.Snapshot, bool) {
	for _, s := range a.snapshots {
		if s.Number == number {
			return s, true
		}
	}
	return snapper.Snapshot{}, false
}

// applyFilter recomputes the visible snapshots.
func (a *app) applyFilter() {
	if a.filter == "" {
		a.visible = a.snapshots
		a.clampCursor()
		return
	}
	needle := strings.ToLower(a.filter)
	kept := make([]snapper.Snapshot, 0, len(a.snapshots))
	for _, s := range a.snapshots {
		if strings.Contains(strings.ToLower(s.Haystack()), needle) {
			kept = append(kept, s)
		}
	}
	a.visible = kept
	a.clampCursor()
}

// applyDiffFilter recomputes the visible changes.
func (a *app) applyDiffFilter() {
	if a.diffFilter == "" {
		a.visibleChanges = a.changes
		a.clampCursor()
		return
	}
	needle := strings.ToLower(a.diffFilter)
	kept := make([]snapper.Change, 0, len(a.changes))
	for _, change := range a.changes {
		if strings.Contains(strings.ToLower(change.Haystack()), needle) {
			kept = append(kept, change)
		}
	}
	a.visibleChanges = kept
	a.clampCursor()
}

// selected returns the highlighted snapshot.
func (a *app) selected() (snapper.Snapshot, bool) {
	if a.cursor < 0 || a.cursor >= len(a.visible) {
		return snapper.Snapshot{}, false
	}
	return a.visible[a.cursor], true
}

// selectedChange returns the highlighted path in the diff view.
func (a *app) selectedChange() (snapper.Change, bool) {
	if a.diffCursor < 0 || a.diffCursor >= len(a.visibleChanges) {
		return snapper.Change{}, false
	}
	return a.visibleChanges[a.diffCursor], true
}

// fileLines is the diff panel's text, split for scrolling.
func (a *app) fileLines() []string {
	if a.fileText == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(a.fileText, "\n"), "\n")
}

// scrollFile moves the diff panel's viewport.
func (a *app) scrollFile(delta int) {
	limit := max(len(a.fileLines())-a.listHeight(), 0)
	a.fileOffset = max(min(a.fileOffset+delta, limit), 0)
}

// moveCursor moves the snapshot selection and keeps the viewport in sync.
func (a *app) moveCursor(delta int) {
	a.cursor += delta
	a.clampCursor()
}

// moveDiffCursor moves the diff selection and keeps the viewport in sync.
func (a *app) moveDiffCursor(delta int) {
	a.diffCursor += delta
	a.clampCursor()
}

// clampCursor keeps every cursor and scroll offset within range. Both lists
// are clamped on every call, because a resize changes what fits on either.
func (a *app) clampCursor() {
	a.cursor, a.offset = clamp(a.cursor, a.offset, len(a.visible), a.listHeight())
	a.diffCursor, a.diffOffset = clamp(
		a.diffCursor, a.diffOffset, len(a.visibleChanges), a.listHeight())
	a.configCursor = min(max(a.configCursor, 0), max(len(a.configs)-1, 0))
	a.fileOffset = max(min(a.fileOffset, max(len(a.fileLines())-a.listHeight(), 0)), 0)
}

// clamp keeps one cursor and its offset inside a list of count rows shown
// height at a time.
func clamp(cursor, offset, count, height int) (int, int) {
	if count == 0 {
		return 0, 0
	}
	cursor = min(max(cursor, 0), count-1)
	if cursor < offset {
		offset = cursor
	}
	if cursor >= offset+height {
		offset = cursor - height + 1
	}
	offset = max(min(offset, max(count-height, 0)), 0)
	return cursor, offset
}
