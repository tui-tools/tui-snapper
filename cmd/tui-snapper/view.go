package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/tui-tools/tui-kit/ui"
	"github.com/tui-tools/tui-snapper/internal/snapper"
)

// Layout constants: the rows a list cannot use.
const (
	headerLines = 2
	footerLines = 2
	// minListHeight keeps at least one visible row on a very short terminal.
	minListHeight = 1
)

// listHeight is the number of rows that fit on screen.
func (a *app) listHeight() int {
	// header + table header + help bar + status line.
	return max(a.height-headerLines-footerLines-2, minListHeight)
}

// View renders the whole screen.
func (a *app) View() string {
	switch a.mode {
	case modeConfirm:
		return a.confirm.View(a.theme, a.width, a.height)
	case modeInput, modeFilter:
		return a.input.View(a.theme, a.width, a.height)
	case modePick:
		return a.picker.View(a.theme, a.width, a.height)
	case modeHelp:
		return placeCenter(
			ui.HelpScreen(a.theme, "tui-snapper — keys", helpKeys(), a.width),
			a.width, a.height)
	case modeConfigs:
		return a.configsView()
	case modeDiff:
		return a.diffView()
	case modeFile:
		return a.fileView()
	case modeRollback:
		return a.rollbackView()
	case modeTimers:
		return a.timersView()
	default:
		return a.snapshotsView()
	}
}

// placeCenter centers a rendered box in the terminal.
func placeCenter(box string, width, height int) string {
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// screen assembles the four bands every view shares.
func (a *app) screen(header, body string, hints []ui.KeyHint, fallback string) string {
	help := ui.HelpBar(a.theme, hints, a.width)
	status := ui.StatusLine(a.theme, a.statusKind, a.status, fallback, a.width)
	return strings.Join([]string{header, body, help, status}, "\n")
}

// snapshotsView renders the main screen.
func (a *app) snapshotsView() string {
	var body string
	switch {
	case a.loading && len(a.visible) == 0:
		body = ui.EmptyState(a.theme, "reading the snapshots…", a.width, a.listHeight()+1)
	case len(a.visible) == 0 && a.loadFailed:
		body = ui.EmptyState(a.theme,
			"could not read the snapshots — see the message below",
			a.width, a.listHeight()+1)
	case len(a.visible) == 0 && a.filter != "":
		body = ui.EmptyState(a.theme,
			"nothing matches "+strconv.Quote(a.filter), a.width, a.listHeight()+1)
	case len(a.visible) == 0:
		body = ui.EmptyState(a.theme, "this config has no snapshots yet",
			a.width, a.listHeight()+1)
	default:
		body = a.snapshotsTable()
	}
	return a.screen(a.snapshotsHeader(), body, shortHelpKeys(), a.defaultStatus())
}

// snapshotsHeader renders the facts at the top of the main screen: which
// config, which subvolume, how many snapshots, how far back they go, and how
// much space they hold when that figure is real.
func (a *app) snapshotsHeader() string {
	facts := []ui.Fact{{Label: "snapshots", Value: strconv.Itoa(a.realCount())}}

	if total, known := snapper.TotalUsedSpace(a.snapshots); known {
		// snapper only fills used-space in when btrfs quotas are enabled for
		// the config. Without them the figure is omitted rather than shown
		// as zero, which would read as "these snapshots cost nothing".
		facts = append(facts, ui.Fact{Label: "space", Value: snapper.FormatBytes(total)})
	}
	if oldest, ok := snapper.Oldest(a.snapshots); ok {
		facts = append(facts, ui.Fact{Label: "oldest", Value: since(oldest.Date)})
	}
	if newest, ok := snapper.Newest(a.snapshots); ok {
		facts = append(facts, ui.Fact{Label: "newest", Value: since(newest.Date)})
	}
	if timers := a.timerSummary(); timers != "" {
		facts = append(facts, ui.Fact{Label: "timers", Value: timers})
	}

	subtitle := a.config.Subvolume
	if subtitle == "" {
		subtitle = "no config"
	}
	subtitle += "  ·  " + a.backend.Describe()
	if a.filter != "" {
		subtitle += "  ·  filter: " + a.filter
	}
	if marked := len(a.marks); marked > 0 {
		subtitle += fmt.Sprintf("  ·  %d marked", marked)
	}

	title := "tui-snapper"
	if a.config.Name != "" {
		title += "  " + a.config.Name
	}
	return ui.Header{Title: title, Subtitle: subtitle, Facts: facts}.
		Render(a.theme, a.width)
}

// timerSummary is the one-word state of snapper's timers for the header, so
// "why did nothing get cleaned up" is answered without opening a screen.
func (a *app) timerSummary() string {
	if len(a.timers) == 0 {
		return ""
	}
	active := 0
	for _, timer := range a.timers {
		if timer.Active == "active" {
			active++
		}
	}
	switch active {
	case len(a.timers):
		return "on"
	case 0:
		return "off"
	default:
		return fmt.Sprintf("%d of %d", active, len(a.timers))
	}
}

// realCount is how many actual snapshots there are, which excludes snapper's
// row 0 for the live subvolume.
func (a *app) realCount() int {
	count := 0
	for _, s := range a.snapshots {
		if !s.Current() {
			count++
		}
	}
	return count
}

// defaultStatus is the hint shown when there is no message to report.
func (a *app) defaultStatus() string {
	count := strconv.Itoa(len(a.visible))
	if len(a.visible) != len(a.snapshots) {
		return count + " of " + strconv.Itoa(len(a.snapshots)) +
			" rows  ·  ? for help"
	}
	return count + " rows  ·  ? for help"
}

// snapshotsTable renders the snapshot list, dropping columns on narrow
// terminals.
func (a *app) snapshotsTable() string {
	columns := []ui.Column{
		{Title: "", Width: 1},
		{Title: "#", Width: 5},
		{Title: "TYPE", Width: 6},
		{Title: "DATE", Width: 12},
	}
	showCleanup := a.width >= 62
	showUser := a.width >= 78
	showSpace := a.width >= 94 && a.spaceKnown()
	if showCleanup {
		columns = append(columns, ui.Column{Title: "CLEANUP", Width: 9})
	}
	if showUser {
		columns = append(columns, ui.Column{Title: "USER", Width: 8})
	}
	if showSpace {
		columns = append(columns, ui.Column{Title: "SPACE", Width: 10})
	}
	columns = append(columns, ui.Column{Title: "DESCRIPTION", Width: 20, Flex: true})

	rows := make([][]string, 0, len(a.visible))
	styles := make([]*lipgloss.Style, 0, len(a.visible))
	for _, s := range a.visible {
		row := []string{a.markCell(s), numberCell(s), typeCell(s), dateCell(s)}
		if showCleanup {
			row = append(row, cleanupCell(s))
		}
		if showUser {
			row = append(row, s.User)
		}
		if showSpace {
			row = append(row, spaceCell(s))
		}
		row = append(row, descriptionCell(s))
		rows = append(rows, row)
		styles = append(styles, a.snapshotStyle(s))
	}

	return ui.Table{
		Columns: columns, Rows: rows, Styles: styles,
		Selected: a.cursor, Offset: a.offset, Height: a.listHeight(),
	}.Render(a.theme, a.width)
}

// spaceKnown reports whether snapper filled the used-space column in, which
// it only does when btrfs quotas are enabled for the config.
func (a *app) spaceKnown() bool {
	_, known := snapper.TotalUsedSpace(a.snapshots)
	return known
}

// markCell is the marker column, so a marked snapshot is visible without
// reading the header.
func (a *app) markCell(s snapper.Snapshot) string {
	if a.marks[s.Number] {
		return "●"
	}
	return " "
}

// numberCell renders a snapshot number, pairing a post snapshot with its pre.
func numberCell(s snapper.Snapshot) string {
	if s.Type == snapper.TypePost && s.PreNumber != 0 {
		return fmt.Sprintf("%d←%d", s.Number, s.PreNumber)
	}
	return strconv.Itoa(s.Number)
}

// typeCell renders the snapshot type, and names row 0 for what it is.
func typeCell(s snapper.Snapshot) string {
	if s.Current() {
		return "live"
	}
	return s.Type
}

// dateCell renders when a snapshot was taken, relative for anything recent
// because "3h ago" answers the question a timestamp only hints at.
func dateCell(s snapper.Snapshot) string {
	if s.Current() {
		return "now"
	}
	if s.Date.IsZero() {
		if s.RawDate != "" {
			return s.RawDate
		}
		return "-"
	}
	return since(s.Date)
}

// cleanupCell names the algorithm that may remove a snapshot, and says
// "keep" when none will.
func cleanupCell(s snapper.Snapshot) string {
	if s.Current() {
		return "-"
	}
	if s.Cleanup == "" {
		return "keep"
	}
	return s.Cleanup
}

// spaceCell renders a snapshot's exclusive space, and a dash when quotas are
// off for that row.
func spaceCell(s snapper.Snapshot) string {
	if s.UsedSpace == snapper.UsedSpaceUnknown {
		return "-"
	}
	return snapper.FormatBytes(s.UsedSpace)
}

// descriptionCell renders the label, with the userdata appended when there is
// room for it to matter.
func descriptionCell(s snapper.Snapshot) string {
	if userdata := s.UserdataString(); userdata != "" {
		return s.Description + "  (" + userdata + ")"
	}
	return s.Description
}

// snapshotStyle colors a row by what it is, so the eye finds the pinned
// snapshots and the upgrade pairs without reading every line.
func (a *app) snapshotStyle(s snapper.Snapshot) *lipgloss.Style {
	var style lipgloss.Style
	switch {
	case s.Current():
		style = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
	case s.Pinned():
		// Nothing will ever remove this one automatically, which is exactly
		// what a user pinning a known-good state wants to see at a glance.
		style = a.theme.Row.Foreground(a.theme.OK.GetForeground())
	case s.Paired():
		style = a.theme.Row.Foreground(a.theme.Info.GetForeground())
	default:
		style = a.theme.Row
	}
	return &style
}

// configsView renders the config picker.
func (a *app) configsView() string {
	header := ui.Header{
		Title:    "tui-snapper",
		Subtitle: "configs  ·  enter selects, esc goes back",
		Facts:    []ui.Fact{{Label: "configs", Value: strconv.Itoa(len(a.configs))}},
	}.Render(a.theme, a.width)

	rows := make([][]string, 0, len(a.configs))
	for _, config := range a.configs {
		marker := " "
		if config.Name == a.config.Name {
			marker = "▸"
		}
		rows = append(rows, []string{marker, config.Name, config.Subvolume})
	}
	body := ui.Table{
		Columns: []ui.Column{
			{Title: "", Width: 1},
			{Title: "CONFIG", Width: 16},
			{Title: "SUBVOLUME", Width: 24, Flex: true},
		},
		Rows: rows, Selected: a.configCursor, Height: a.listHeight(),
	}.Render(a.theme, a.width)

	hints := []ui.KeyHint{
		{Key: "enter", Desc: "open"},
		{Key: "esc", Desc: "back"},
		{Key: "?", Desc: "help"},
	}
	return a.screen(header, body, hints, "showing "+a.config.Name+"  ·  esc to go back")
}

// diffView renders what changed between two snapshots.
func (a *app) diffView() string {
	created, deleted, modified := 0, 0, 0
	for _, change := range a.changes {
		switch change.Kind {
		case snapper.Created:
			created++
		case snapper.Deleted:
			deleted++
		default:
			modified++
		}
	}
	facts := []ui.Fact{
		{Label: "modified", Value: strconv.Itoa(modified)},
		{Label: "created", Value: strconv.Itoa(created)},
		{Label: "deleted", Value: strconv.Itoa(deleted)},
	}
	if marked := len(a.fileMarks); marked > 0 {
		facts = append(facts, ui.Fact{Label: "marked", Value: strconv.Itoa(marked)})
	}
	header := ui.Header{
		Title:    fmt.Sprintf("%d → %d", a.diffFrom, a.diffTo),
		Subtitle: a.diffSubtitle(),
		Facts:    facts,
	}.Render(a.theme, a.width)

	var body string
	switch {
	case a.loading && len(a.visibleChanges) == 0:
		body = ui.EmptyState(a.theme, "comparing the snapshots…", a.width, a.listHeight()+1)
	case len(a.visibleChanges) == 0 && a.diffFilter != "":
		body = ui.EmptyState(a.theme, "nothing matches "+strconv.Quote(a.diffFilter),
			a.width, a.listHeight()+1)
	case len(a.visibleChanges) == 0:
		body = ui.EmptyState(a.theme, "nothing changed between these two snapshots",
			a.width, a.listHeight()+1)
	default:
		body = a.diffTable()
	}

	hints := []ui.KeyHint{
		{Key: "enter", Desc: "diff"},
		{Key: "space", Desc: "mark"},
		{Key: "u", Desc: "undo"},
		{Key: "/", Desc: "filter"},
		{Key: "esc", Desc: "back"},
		{Key: "?", Desc: "help"},
	}
	return a.screen(header, body, hints, a.diffStatus())
}

// diffSubtitle names the two snapshots being compared in words.
func (a *app) diffSubtitle() string {
	describe := func(number int) string {
		if number == 0 {
			return "the live subvolume"
		}
		if s, ok := a.snapshotNumbered(number); ok && s.Description != "" {
			return fmt.Sprintf("%d (%s)", number, s.Description)
		}
		return strconv.Itoa(number)
	}
	subtitle := describe(a.diffFrom) + " → " + describe(a.diffTo)
	if a.diffFilter != "" {
		subtitle += "  ·  filter: " + a.diffFilter
	}
	return subtitle
}

// diffStatus is the hint shown when there is no message to report.
func (a *app) diffStatus() string {
	return fmt.Sprintf("%d changed paths  ·  esc to go back", len(a.visibleChanges))
}

// diffTable renders the changed paths.
func (a *app) diffTable() string {
	columns := []ui.Column{
		{Title: "", Width: 1},
		{Title: "CHANGE", Width: 12},
	}
	showFlags := a.width >= 70
	if showFlags {
		columns = append(columns, ui.Column{Title: "FLAGS", Width: 7})
	}
	columns = append(columns, ui.Column{Title: "PATH", Width: 30, Flex: true})

	rows := make([][]string, 0, len(a.visibleChanges))
	styles := make([]*lipgloss.Style, 0, len(a.visibleChanges))
	for _, change := range a.visibleChanges {
		marker := " "
		if a.fileMarks[change.Path] {
			marker = "●"
		}
		row := []string{marker, string(change.Kind)}
		if showFlags {
			row = append(row, change.Status)
		}
		rows = append(rows, append(row, change.Path))
		styles = append(styles, a.changeStyle(change))
	}
	return ui.Table{
		Columns: columns, Rows: rows, Styles: styles,
		Selected: a.diffCursor, Offset: a.diffOffset, Height: a.listHeight(),
	}.Render(a.theme, a.width)
}

// changeStyle colors a row by what happened to the path.
func (a *app) changeStyle(change snapper.Change) *lipgloss.Style {
	var style lipgloss.Style
	switch change.Kind {
	case snapper.Created:
		style = a.theme.Row.Foreground(a.theme.OK.GetForeground())
	case snapper.Deleted:
		style = a.theme.Row.Foreground(a.theme.Danger.GetForeground())
	case snapper.TypeChanged:
		style = a.theme.Row.Foreground(a.theme.Warn.GetForeground())
	default:
		style = a.theme.Row
	}
	return &style
}

// fileView renders one path's unified diff, read-only and scrollable.
func (a *app) fileView() string {
	header := ui.Header{
		Title:    ui.Truncate(a.filePath, max(a.width-2, 8)),
		Subtitle: fmt.Sprintf("diff %d → %d  ·  read-only", a.diffFrom, a.diffTo),
		Facts:    []ui.Fact{{Label: "lines", Value: strconv.Itoa(len(a.fileLines()))}},
	}.Render(a.theme, a.width)

	height := a.listHeight() + 1
	var body string
	switch {
	case a.loading && a.fileText == "":
		body = ui.EmptyState(a.theme, "reading the diff…", a.width, height)
	case a.fileText == "":
		body = ui.EmptyState(a.theme, "snapper reported no difference for this path",
			a.width, height)
	default:
		body = a.diffLines(height)
	}

	hints := []ui.KeyHint{
		{Key: "↑/↓", Desc: "scroll"},
		{Key: "r", Desc: "re-read"},
		{Key: "esc", Desc: "back"},
		{Key: "?", Desc: "help"},
	}
	return a.screen(header, body, hints, a.fileScrollStatus())
}

// fileScrollStatus says where in the diff the reader is.
func (a *app) fileScrollStatus() string {
	total := len(a.fileLines())
	if total == 0 {
		return "esc to go back"
	}
	last := min(a.fileOffset+a.listHeight(), total)
	return fmt.Sprintf("lines %d-%d of %d  ·  esc to go back",
		a.fileOffset+1, last, total)
}

// diffLines renders the visible slice of a unified diff, coloring the added
// and removed lines the way every diff a reader has seen is colored.
func (a *app) diffLines(height int) string {
	lines := a.fileLines()
	if a.fileOffset < len(lines) {
		lines = lines[a.fileOffset:]
	} else {
		lines = nil
	}
	if len(lines) > height {
		lines = lines[:height]
	}

	rendered := make([]string, 0, height)
	for _, line := range lines {
		style := a.theme.Row
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			style = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
		case strings.HasPrefix(line, "@@"):
			style = a.theme.Row.Foreground(a.theme.Info.GetForeground())
		case strings.HasPrefix(line, "+"):
			style = a.theme.Row.Foreground(a.theme.OK.GetForeground())
		case strings.HasPrefix(line, "-"):
			style = a.theme.Row.Foreground(a.theme.Danger.GetForeground())
		}
		rendered = append(rendered,
			style.Width(a.width).Render(ui.Truncate(line, a.width-2)))
	}
	for len(rendered) < height {
		rendered = append(rendered, a.theme.Row.Width(a.width).Render(""))
	}
	return strings.Join(rendered, "\n")
}

// rollbackView explains what a rollback means on this machine, and only
// offers to perform one where snapper is genuinely the mechanism.
func (a *app) rollbackView() string {
	header := ui.Header{
		Title:    "tui-snapper  rollback",
		Subtitle: a.config.Name + "  ·  " + a.config.Subvolume,
		Facts:    []ui.Fact{{Label: "mechanism", Value: string(a.platform.Kind)}},
	}.Render(a.theme, a.width)

	height := a.listHeight() + 1
	var body string
	if a.loading && a.platform.Kind == "" {
		body = ui.EmptyState(a.theme, "looking at how this machine boots…", a.width, height)
	} else {
		body = a.rollbackBody(height)
	}

	hints := []ui.KeyHint{{Key: "r", Desc: "re-check"}}
	if a.platform.Kind == snapper.RollbackSnapper {
		hints = append([]ui.KeyHint{{Key: "enter", Desc: "roll back"}}, hints...)
	}
	hints = append(hints,
		ui.KeyHint{Key: "esc", Desc: "back"}, ui.KeyHint{Key: "?", Desc: "help"})
	return a.screen(header, body, hints, "esc to go back")
}

// rollbackBody is the explanation and, on a boot-menu layout, the entries the
// boot menu offers.
func (a *app) rollbackBody(height int) string {
	t := a.theme
	lines := []string{"", "  " + a.platform.Reason, ""}

	switch a.platform.Kind {
	case snapper.RollbackSnapper:
		selected := "a snapshot"
		if s, ok := a.selected(); ok && !s.Current() {
			selected = fmt.Sprintf("snapshot %d (%s)", s.Number, s.Description)
		}
		lines = append(lines,
			"  "+t.Danger.Render("This rewrites which subvolume the machine boots."),
			"",
			"  Pressing enter previews `snapper rollback` for "+selected+".",
			"  snapper snapshots the running system first, then makes the chosen",
			"  snapshot the new default subvolume. Nothing changes until you reboot,",
			"  and the command is shown in full before anything runs.",
		)
	case snapper.RollbackBootMenu:
		lines = append(lines,
			"  "+t.Warn.Render("Roll back from the boot menu, not from here."),
			"",
			"  Reboot, pick one of the entries below, and the machine starts from",
			"  that snapshot. tui-snapper reads "+a.platform.BootConfig+" and shows",
			"  what is there; it never edits it and never swaps subvolumes itself.",
			"",
			"  "+t.Subtitle.Render("Boot menu snapshot entries"),
		)
		for _, entry := range a.platform.Entries {
			line := "    " + entry.Title
			if entry.Comment != "" {
				line += "  ·  " + entry.Comment
			}
			lines = append(lines, "  "+ui.Truncate(line, max(a.width-4, 8)))
		}
	default:
		lines = append(lines,
			"  There is no rollback to offer for this config.",
			"",
			"  You can still compare snapshots with d and put individual files back",
			"  with u, which is what most of a rollback is for anyway.",
		)
	}

	rendered := make([]string, 0, height)
	for _, line := range lines {
		if len(rendered) == height {
			break
		}
		rendered = append(rendered,
			t.Row.Width(a.width).Render(ui.Truncate(line, a.width-1)))
	}
	for len(rendered) < height {
		rendered = append(rendered, t.Row.Width(a.width).Render(""))
	}
	return strings.Join(rendered, "\n")
}

// timersView renders the state of snapper's systemd timers, read-only.
// Turning them on or off is systemd's job, and tui-systemd already does it.
func (a *app) timersView() string {
	header := ui.Header{
		Title:    "tui-snapper  timers",
		Subtitle: "read-only  ·  systemctl enables and disables these",
		Facts:    []ui.Fact{{Label: "units", Value: strconv.Itoa(len(a.timers))}},
	}.Render(a.theme, a.width)

	var body string
	if len(a.timers) == 0 {
		message := "reading the timer states…"
		if !a.loading {
			message = "systemd could not be asked about these units"
		}
		body = ui.EmptyState(a.theme, message, a.width, a.listHeight()+1)
	} else {
		rows := make([][]string, 0, len(a.timers))
		styles := make([]*lipgloss.Style, 0, len(a.timers))
		for _, timer := range a.timers {
			active, enabled := timer.Active, timer.Enabled
			if timer.Err != "" {
				active, enabled = "unknown", "unknown"
			}
			rows = append(rows, []string{timer.Unit, active, enabled, timerNote(timer)})
			style := a.theme.Row
			switch {
			case timer.Err != "":
				style = a.theme.Row.Foreground(a.theme.Muted.GetForeground())
			case timer.Active == "active":
				style = a.theme.Row.Foreground(a.theme.OK.GetForeground())
			default:
				style = a.theme.Row.Foreground(a.theme.Warn.GetForeground())
			}
			styles = append(styles, &style)
		}
		body = ui.Table{
			Columns: []ui.Column{
				{Title: "UNIT", Width: 24},
				{Title: "STATE", Width: 10},
				{Title: "AT BOOT", Width: 10},
				{Title: "WHAT IT DOES", Width: 24, Flex: true},
			},
			Rows: rows, Styles: styles, Height: a.listHeight(), Selected: -1,
		}.Render(a.theme, a.width)
	}

	hints := []ui.KeyHint{
		{Key: "r", Desc: "re-read"},
		{Key: "esc", Desc: "back"},
		{Key: "?", Desc: "help"},
	}
	return a.screen(header, body, hints, "read-only  ·  esc to go back")
}

// timerNote says what each timer is for, because the unit name does not.
func timerNote(timer snapper.TimerState) string {
	if timer.Err != "" {
		return timer.Err
	}
	switch timer.Unit {
	case "snapper-timeline.timer":
		return "takes the hourly timeline snapshots"
	case "snapper-cleanup.timer":
		return "applies the cleanup algorithms"
	default:
		return ""
	}
}

// since renders how long ago a moment was, in one unit. A snapshot list is
// read as a history, and "3h ago" answers that better than a timestamp.
func since(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return "just now"
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dmin ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 60*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	default:
		return t.Format("2006-01-02")
	}
}

// shortHelpKeys is the single-line hint bar on the main screen.
func shortHelpKeys() []ui.KeyHint {
	return []ui.KeyHint{
		{Key: "d", Desc: "diff"},
		{Key: "c", Desc: "create"},
		{Key: "D", Desc: "delete"},
		{Key: "e", Desc: "describe"},
		{Key: "space", Desc: "mark"},
		{Key: "s", Desc: "configs"},
		{Key: "R", Desc: "rollback"},
		{Key: "/", Desc: "filter"},
		{Key: "?", Desc: "help"},
		{Key: "q", Desc: "quit"},
	}
}

// actionHints renders the action table, so a new action cannot go missing
// from the help screen.
func actionHints() []ui.KeyHint {
	hints := make([]ui.KeyHint, 0, len(snapper.Actions))
	for _, spec := range snapper.Actions {
		hints = append(hints, ui.KeyHint{
			Key: spec.Key, Desc: strings.ToLower(spec.Label)})
	}
	return hints
}

// helpKeys is the full key list shown on the help screen. It is kept short
// enough to fit a 24-row terminal, because a help screen that scrolls off the
// top is worse than no help at all.
func helpKeys() []ui.KeyHint {
	hints := []ui.KeyHint{
		{Key: "↑ / ↓", Desc: "move the selection (j / k also work)"},
		{Key: "g / G", Desc: "first / last row; pgup / pgdn scroll a page"},
		{Key: "space", Desc: "mark a snapshot: two marks are a range, many are a delete"},
		{Key: "/", Desc: "filter the list (esc clears)"},
		{Key: "", Desc: ""},
	}
	hints = append(hints, actionHints()...)
	return append(hints,
		ui.KeyHint{Key: "", Desc: ""},
		ui.KeyHint{Key: "d / enter", Desc: "compare two snapshots; a post pairs with its pre"},
		ui.KeyHint{Key: "enter", Desc: "in the diff: the file's changes, read-only"},
		ui.KeyHint{Key: "s / T", Desc: "switch config / snapper's timers, read-only"},
		ui.KeyHint{Key: "r", Desc: "re-read the current view"},
		ui.KeyHint{Key: "? / q", Desc: "this help / quit"},
		ui.KeyHint{Key: "", Desc: ""},
		ui.KeyHint{Key: "note", Desc: "every change is previewed and confirmed first"},
	)
}
