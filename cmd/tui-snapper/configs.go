// The config screen: the list of snapper configurations, and the three
// actions that author one. Creating a config, changing its retention limits
// and deleting it all go through the same path every other mutation takes —
// collect the fields, build the command, show it, confirm it — so the screen
// that used to only pick a config now also changes what exists, and still
// never runs anything the user has not read.
package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-snapper/internal/snapper"
)

// handleConfigsKey handles the config screen.
func (a *app) handleConfigsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()

	// The action table is checked before navigation, so the keys the help
	// screen advertises are the keys this screen has.
	if spec, ok := snapper.ConfigActionFor(key); ok {
		return a, a.startConfigAction(spec)
	}

	switch key {
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
	case "r", "ctrl+r":
		a.loading = true
		return a, a.loadConfigs()
	case "enter":
		selected, ok := a.selectedConfig()
		if !ok {
			return a, nil
		}
		a.config = selected
		a.mode = modeSnapshots
		a.resetForConfig()
		a.loading = true
		return a, tea.Batch(a.loadSnapshots(), a.loadPlatform())
	}
	return a, nil
}

// selectedConfig returns the config row the cursor is on.
func (a *app) selectedConfig() (snapper.Config, bool) {
	if a.configCursor < 0 || a.configCursor >= len(a.configs) {
		return snapper.Config{}, false
	}
	return a.configs[a.configCursor], true
}

// startConfigAction begins one of the config-screen actions. Two of them read
// the machine first — the settings to seed the form with, the snapshot count
// to put in the deletion dialog — so they finish in the message handlers
// below rather than here.
func (a *app) startConfigAction(spec snapper.ActionSpec) tea.Cmd {
	if spec.Scope == snapper.ScopeNewConfig {
		a.pending = &pending{
			spec:     spec,
			req:      snapper.Request{Values: map[snapper.FieldKind]string{}},
			fields:   spec.Fields,
			returnTo: modeConfigs,
		}
		return a.advance()
	}

	selected, ok := a.selectedConfig()
	if !ok {
		a.setStatus(ui.StatusWarn, "no config selected")
		return nil
	}
	if spec.Action == snapper.DeleteConfig && selected.Name == snapper.ProtectedConfig {
		// Refused here as well as in BuildCommand, so the answer arrives on
		// the keystroke rather than after a dialog the user cannot use.
		a.setStatusf(ui.StatusWarn,
			"tui-snapper does not delete the %q config: it holds the root "+
				"filesystem's whole snapshot history, and the boot menu's entries with it",
			snapper.ProtectedConfig)
		return nil
	}

	a.pending = &pending{
		spec: spec,
		req: snapper.Request{
			Config:  selected.Name,
			Values:  map[snapper.FieldKind]string{},
			Current: map[snapper.FieldKind]string{},
		},
		fields:   spec.Fields,
		returnTo: modeConfigs,
	}
	a.busy = true
	if spec.Action == snapper.DeleteConfig {
		return a.loadConfigCount(selected.Name)
	}
	return a.loadSettings(selected.Name)
}

// onSettings seeds the retention form with what the machine reports, then asks
// the first question. Editing starts from the current values, so leaving a
// prompt untouched leaves that key alone.
func (a *app) onSettings(msg settingsMsg) (tea.Model, tea.Cmd) {
	a.busy = false
	if a.pending == nil || a.pending.req.Config != msg.config {
		return a, nil
	}
	if msg.err != nil {
		a.setStatus(ui.StatusError, msg.err.Error())
		a.mode = a.returnMode()
		a.pending = nil
		return a, nil
	}
	for _, setting := range snapper.EditableSettings {
		value := strings.TrimSpace(msg.settings[setting.Key])
		a.pending.req.Values[setting.Kind] = value
		a.pending.req.Current[setting.Kind] = value
	}
	return a, a.advance()
}

// onSubvolume resolves the check on a path typed into the new-config form. A
// path snapper could not manage sends the same prompt back with the reason,
// which is where a user can act on it.
func (a *app) onSubvolume(msg subvolumeMsg) (tea.Model, tea.Cmd) {
	a.busy = false
	if a.pending == nil {
		return a, nil
	}
	if msg.err != nil {
		a.setStatus(ui.StatusError, msg.err.Error())
		return a, a.reask()
	}
	return a, a.advance()
}

// onConfigCount finishes a config deletion: the count read is what the dialog
// says is about to go, and a count that could not be read is reported as
// unknown rather than as zero.
func (a *app) onConfigCount(msg configCountMsg) (tea.Model, tea.Cmd) {
	a.busy = false
	if a.pending == nil || a.pending.req.Config != msg.config {
		return a, nil
	}
	a.deleteCount = msg.count
	a.deleteCountKnown = msg.err == nil
	return a, a.previewPending()
}

// deleteConfigBody is the deletion dialog's explanation: the action's own
// words, plus what this particular config holds.
func (a *app) deleteConfigBody(p *pending) string {
	body := p.spec.Body
	subvolume := ""
	for _, config := range a.configs {
		if config.Name == p.req.Config {
			subvolume = config.Subvolume
		}
	}
	if subvolume != "" {
		body += fmt.Sprintf("\n\nSubvolume: %s", subvolume)
	}
	switch {
	case !a.deleteCountKnown:
		body += "\n\nIts snapshots could not be counted, so how much history goes " +
			"with it is unknown."
	case a.deleteCount == 0:
		body += "\n\nIt holds no snapshots."
	case a.deleteCount == 1:
		body += "\n\nIt holds 1 snapshot, which goes with it."
	default:
		body += fmt.Sprintf("\n\nIt holds %d snapshots, which go with it.", a.deleteCount)
	}
	return body
}

// handleTypedConfirm resolves the dialog that asks for a word rather than for
// a key. Like the yes/no one, it is a path to a change and nothing else.
func (a *app) handleTypedConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if a.typed == nil {
		a.mode = a.returnMode()
		return a, nil
	}
	cmd := a.typed.Update(msg)
	if !a.typed.done {
		return a, cmd
	}
	dialog := a.typed
	a.typed = nil
	a.mode = a.returnMode()
	a.pending = nil
	if !dialog.confirmed {
		if dialog.answered {
			a.setStatusf(ui.StatusWarn,
				"that is not %q, so nothing was deleted", dialog.word)
		} else {
			a.setStatus(ui.StatusInfo, "cancelled")
		}
		return a, nil
	}
	a.busy = true
	a.setStatusf(ui.StatusInfo, "running %s…", a.backend.Preview(dialog.cmd))
	return a, a.run(dialog.cmd)
}

// configSubcommand returns the config-level sub-command an argv carries and
// the config it names.
//
// The sub-command is the element right after `-c <name>`, which is where
// snapper's own parser expects it. Scanning the whole argv instead would read
// a snapshot whose description happens to be "set-config" as a config change.
func configSubcommand(argv []string) (kind, config string) {
	for i := 0; i+2 < len(argv); i++ {
		if argv[i] != "-c" {
			continue
		}
		switch argv[i+2] {
		case "create-config", "set-config", "delete-config":
			return argv[i+2], argv[i+1]
		default:
			return "", ""
		}
	}
	return "", ""
}
