package main

import (
	"os"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-snapper/internal/snapper"
)

// These tests drive the model the way a user drives the app: press a key,
// read the screen, answer the dialog. What they are really asserting is the
// family contract — a change happens only after the exact command line has
// been shown and confirmed.

// newTestApp builds an app on the demo backend and plays through the initial
// reads, so the tests start from the screen a user would see.
func newTestApp(t *testing.T) (*app, *snapper.Fake) {
	t.Helper()
	fake := snapper.NewFake()
	a := newApp(fake, theme.Theme{}, snapper.DemoConfig)
	a.width, a.height = 120, 30
	drain(t, a, a.Init())
	if a.config.Name != snapper.DemoConfig {
		t.Fatalf("opened on config %q, want %q", a.config.Name, snapper.DemoConfig)
	}
	if len(a.visible) == 0 {
		t.Fatal("the demo backend should have produced snapshots")
	}
	return a, fake
}

// drain runs a command and feeds every message it produces back into the
// model, which is what the Bubble Tea runtime does.
//
// Each command is run with a deadline, because not every command answers: a
// text input's cursor blink sleeps until its next tick, and the real runtime
// simply leaves it running on its own goroutine. Here it is abandoned, which
// is the same thing from the model's point of view.
func drain(t *testing.T, a *app, cmd tea.Cmd) {
	t.Helper()
	for i := 0; cmd != nil && i < 20; i++ {
		msg, answered := runCmd(cmd)
		if !answered || msg == nil {
			return
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			// A batch is a set of independent commands; each is drained on
			// its own.
			cmd = nil
			for _, one := range batch {
				drain(t, a, one)
			}
			continue
		}
		_, cmd = a.Update(msg)
	}
}

// runCmd evaluates one command, giving up on the ones that never answer.
func runCmd(cmd tea.Cmd) (tea.Msg, bool) {
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		return msg, true
	case <-time.After(200 * time.Millisecond):
		return nil, false
	}
}

// press sends one key and drains whatever it produced.
func press(t *testing.T, a *app, key string) {
	t.Helper()
	var msg tea.KeyMsg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	case " ":
		msg = tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	_, cmd := a.Update(msg)
	drain(t, a, cmd)
}

// selectNumber moves the cursor onto a given snapshot.
func selectNumber(t *testing.T, a *app, number int) {
	t.Helper()
	for i, s := range a.visible {
		if s.Number == number {
			a.cursor = i
			a.clampCursor()
			return
		}
	}
	t.Fatalf("snapshot %d is not in the demo data", number)
}

func TestOpensOnARealSnapshot(t *testing.T) {
	// Row 0 is the live subvolume and nothing can be done to it, so a tool
	// that opened with it selected would answer "pick a real snapshot" to
	// every action key.
	a, _ := newTestApp(t)
	selected, ok := a.selected()
	if !ok {
		t.Fatal("nothing is selected")
	}
	if selected.Current() {
		t.Error("the cursor should start on a real snapshot, not the live subvolume")
	}
}

func TestDeleteNeedsConfirmation(t *testing.T) {
	a, fake := newTestApp(t)
	selectNumber(t, a, 42)

	press(t, a, "D")
	if a.mode != modeConfirm {
		t.Fatalf("D opened mode %v, want the confirm dialog", a.mode)
	}
	// The dialog must show the command that will run, not a paraphrase.
	if want := "snapper -c root delete 42"; a.confirm.Command != want {
		t.Errorf("preview = %q, want %q", a.confirm.Command, want)
	}
	if !a.confirm.Danger {
		t.Error("a delete dialog should be painted as dangerous")
	}
	if len(fake.Commands()) != 0 {
		t.Fatalf("%d commands ran before the dialog was answered", len(fake.Commands()))
	}

	// Cancelling runs nothing at all.
	press(t, a, "n")
	if len(fake.Commands()) != 0 {
		t.Fatalf("cancelling ran %d commands, want none", len(fake.Commands()))
	}
	if a.mode != modeSnapshots {
		t.Errorf("after cancelling the app is in mode %v, want the list", a.mode)
	}

	// Confirming runs exactly the previewed command, and nothing else.
	press(t, a, "D")
	preview := a.confirm.Command
	press(t, a, "y")
	ran := fake.Commands()
	if len(ran) != 1 {
		t.Fatalf("ran %d commands, want 1", len(ran))
	}
	if got := a.backend.Preview(ran[0]); got != preview {
		t.Errorf("ran %q, but the preview promised %q", got, preview)
	}
	// The system is re-read after a change, so the row is really gone.
	for _, s := range a.snapshots {
		if s.Number == 42 {
			t.Error("snapshot 42 is still listed after a confirmed delete")
		}
	}
}

func TestEveryActionKeyStopsAtTheDialog(t *testing.T) {
	// No action key may reach the backend on its own, whatever it is.
	for _, spec := range snapper.Actions {
		t.Run(string(spec.Action), func(t *testing.T) {
			a, fake := newTestApp(t)
			selectNumber(t, a, 42)
			press(t, a, spec.Key)
			if len(fake.Commands()) != 0 {
				t.Errorf("%q ran %d commands before any confirmation",
					spec.Key, len(fake.Commands()))
			}
			if a.mode == modeSnapshots && a.status == "" {
				t.Errorf("%q did nothing and said nothing", spec.Key)
			}
		})
	}
}

func TestCreateCollectsItsFieldsThenPreviews(t *testing.T) {
	a, fake := newTestApp(t)

	press(t, a, "c")
	if a.mode != modeInput {
		t.Fatalf("create opened mode %v, want the description prompt", a.mode)
	}
	for _, r := range "known good" {
		press(t, a, string(r))
	}
	press(t, a, "enter")

	if a.mode != modePick {
		t.Fatalf("after the description the app is in mode %v, want the type picker", a.mode)
	}
	press(t, a, "enter") // single, the first option

	if a.mode != modePick {
		t.Fatalf("after the type the app is in mode %v, want the cleanup picker", a.mode)
	}
	press(t, a, "down") // move off "(none)" onto "number"
	press(t, a, "enter")

	if a.mode != modeConfirm {
		t.Fatalf("after the last field the app is in mode %v, want the dialog", a.mode)
	}
	want := "snapper -c root create --description known good --cleanup-algorithm number"
	if a.confirm.Command != want {
		t.Errorf("preview = %q, want %q", a.confirm.Command, want)
	}
	if len(fake.Commands()) != 0 {
		t.Fatal("collecting the fields must not run anything")
	}

	press(t, a, "y")
	if len(fake.Commands()) != 1 {
		t.Fatalf("ran %d commands, want 1", len(fake.Commands()))
	}
	newest, ok := snapper.Newest(a.snapshots)
	if !ok || newest.Description != "known good" {
		t.Errorf("newest snapshot = %+v, want the one just created", newest)
	}
}

func TestCancellingAPromptRunsNothing(t *testing.T) {
	a, fake := newTestApp(t)
	press(t, a, "c")
	press(t, a, "esc")
	if a.mode != modeSnapshots {
		t.Errorf("after esc the app is in mode %v, want the list", a.mode)
	}
	if len(fake.Commands()) != 0 {
		t.Errorf("abandoning a prompt ran %d commands", len(fake.Commands()))
	}
	if a.pending != nil {
		t.Error("the abandoned action is still pending")
	}
}

func TestMarkingSeveralSnapshotsDeletesThemTogether(t *testing.T) {
	a, _ := newTestApp(t)
	selectNumber(t, a, 45)
	press(t, a, " ")
	selectNumber(t, a, 40)
	press(t, a, " ")
	selectNumber(t, a, 31)
	press(t, a, " ")

	press(t, a, "D")
	if a.mode != modeConfirm {
		t.Fatalf("D opened mode %v, want the confirm dialog", a.mode)
	}
	if want := "snapper -c root delete 31 40 45"; a.confirm.Command != want {
		t.Errorf("preview = %q, want %q", a.confirm.Command, want)
	}
	// The title has to name every number, because that is what the user
	// reads before saying yes.
	for _, want := range []string{"31", "40", "45"} {
		if !strings.Contains(a.confirm.Title, want) {
			t.Errorf("the dialog title %q does not name snapshot %s",
				a.confirm.Title, want)
		}
	}
}

func TestPostSnapshotDiffsAgainstItsOwnPre(t *testing.T) {
	a, _ := newTestApp(t)
	selectNumber(t, a, 47) // a post snapshot paired with 46

	press(t, a, "d")
	if a.mode != modeDiff {
		t.Fatalf("d opened mode %v, want the diff view", a.mode)
	}
	if a.diffFrom != 46 || a.diffTo != 47 {
		t.Errorf("comparing %d..%d, want 46..47", a.diffFrom, a.diffTo)
	}
	if len(a.visibleChanges) == 0 {
		t.Fatal("the comparison produced no changes")
	}
}

func TestTwoMarksDefineTheComparison(t *testing.T) {
	a, _ := newTestApp(t)
	selectNumber(t, a, 31)
	press(t, a, " ")
	selectNumber(t, a, 45)
	press(t, a, " ")

	press(t, a, "d")
	if a.diffFrom != 31 || a.diffTo != 45 {
		t.Errorf("comparing %d..%d, want 31..45", a.diffFrom, a.diffTo)
	}
}

func TestUndoChangeInTheDiffView(t *testing.T) {
	a, fake := newTestApp(t)
	selectNumber(t, a, 47)
	press(t, a, "d")

	first := a.visibleChanges[0].Path
	press(t, a, "u")
	if a.mode != modeConfirm {
		t.Fatalf("u opened mode %v, want the confirm dialog", a.mode)
	}
	want := "snapper -c root undochange 46..47 " + first
	if a.confirm.Command != want {
		t.Errorf("preview = %q, want %q", a.confirm.Command, want)
	}
	if len(fake.Commands()) != 0 {
		t.Fatal("u must not run anything before the dialog is answered")
	}

	press(t, a, "y")
	if len(fake.Commands()) != 1 {
		t.Fatalf("ran %d commands, want 1", len(fake.Commands()))
	}
	// The diff view is where the action was started, so it is where the app
	// goes back to.
	if a.mode != modeDiff {
		t.Errorf("after the undo the app is in mode %v, want the diff view", a.mode)
	}
}

func TestUndoChangeUsesEveryMarkedFile(t *testing.T) {
	a, _ := newTestApp(t)
	selectNumber(t, a, 47)
	press(t, a, "d")

	first := a.visibleChanges[0].Path
	second := a.visibleChanges[1].Path
	press(t, a, " ") // marks the first and moves down
	press(t, a, " ") // marks the second

	press(t, a, "u")
	want := "snapper -c root undochange 46..47 " + first + " " + second
	if a.confirm.Command != want {
		t.Errorf("preview = %q, want %q", a.confirm.Command, want)
	}
}

func TestFileDiffIsReadOnly(t *testing.T) {
	a, fake := newTestApp(t)
	selectNumber(t, a, 47)
	press(t, a, "d")
	press(t, a, "enter")

	if a.mode != modeFile {
		t.Fatalf("enter opened mode %v, want the file panel", a.mode)
	}
	if a.fileText == "" {
		t.Fatal("the file panel is empty")
	}
	// Nothing on this screen may change anything, so the keys that mutate
	// elsewhere have to be inert here.
	for _, key := range []string{"D", "c", "C", "u", "a", "e", "R"} {
		press(t, a, key)
	}
	if len(fake.Commands()) != 0 {
		t.Errorf("the read-only panel ran %d commands", len(fake.Commands()))
	}
	press(t, a, "esc")
	if a.mode != modeDiff {
		t.Errorf("esc left the panel into mode %v, want the diff view", a.mode)
	}
}

func TestRollbackExplainsBeforeItOffers(t *testing.T) {
	a, fake := newTestApp(t)
	selectNumber(t, a, 42)

	press(t, a, "R")
	if a.mode != modeRollback {
		t.Fatalf("R opened mode %v, want the rollback screen", a.mode)
	}
	if a.platform.Kind != snapper.RollbackBootMenu {
		t.Fatalf("the demo platform is %q, want the boot-menu layout", a.platform.Kind)
	}
	// On a boot-menu layout there is nothing for snapper to roll back, so
	// enter must refuse rather than build a command that would fail.
	press(t, a, "enter")
	if len(fake.Commands()) != 0 {
		t.Errorf("the rollback screen ran %d commands on a boot-menu layout",
			len(fake.Commands()))
	}
	if a.mode != modeRollback {
		t.Errorf("enter left the rollback screen into mode %v", a.mode)
	}
	if a.status == "" {
		t.Error("refusing a rollback should say why")
	}
}

func TestSwitchingConfigReloads(t *testing.T) {
	a, _ := newTestApp(t)
	press(t, a, "s")
	if a.mode != modeConfigs {
		t.Fatalf("s opened mode %v, want the config picker", a.mode)
	}
	press(t, a, "down")
	press(t, a, "enter")
	if a.config.Name != "home" {
		t.Fatalf("opened config %q, want home", a.config.Name)
	}
	if a.config.Subvolume != "/home" {
		t.Errorf("subvolume = %q, want /home", a.config.Subvolume)
	}
	for _, s := range a.snapshots {
		if s.Subvolume != "/home" {
			t.Errorf("snapshot %d belongs to %q, not to the new config",
				s.Number, s.Subvolume)
		}
	}
	// Nothing from the previous config may survive the switch.
	if len(a.marks) != 0 || a.filter != "" {
		t.Error("marks and filters should be cleared when the config changes")
	}
}

func TestFilterMatchesDescriptionsAndTypes(t *testing.T) {
	a, _ := newTestApp(t)
	total := len(a.visible)

	a.filter = "pacman"
	a.applyFilter()
	if len(a.visible) == 0 || len(a.visible) >= total {
		t.Fatalf("filtering on \"pacman\" left %d of %d rows", len(a.visible), total)
	}
	for _, s := range a.visible {
		if !strings.Contains(strings.ToLower(s.Haystack()), "pacman") {
			t.Errorf("snapshot %d does not match the filter", s.Number)
		}
	}

	a.filter = "timeline"
	a.applyFilter()
	for _, s := range a.visible {
		if s.Cleanup != "timeline" && s.Description != "timeline" {
			t.Errorf("snapshot %d does not match \"timeline\"", s.Number)
		}
	}

	a.filter = ""
	a.applyFilter()
	if len(a.visible) != total {
		t.Errorf("clearing the filter left %d of %d rows", len(a.visible), total)
	}
}

func TestViewRendersAtEveryWidth(t *testing.T) {
	// A layout that panics or overflows on a narrow pane is a bug the
	// screenshots would never catch.
	a, _ := newTestApp(t)
	modes := []struct {
		name string
		open func()
	}{
		{"snapshots", func() { a.mode = modeSnapshots }},
		{"configs", func() { a.mode = modeConfigs }},
		{"timers", func() { a.mode = modeTimers }},
		{"rollback", func() { a.mode = modeRollback }},
		// The help screen is deliberately not in this list: the kit's
		// HelpScreen sizes its panel to its longest line and ignores the
		// width it is given, so asserting on it here would be asserting on
		// the kit rather than on this tool.
	}
	for _, width := range []int{40, 60, 80, 120, 200} {
		for _, m := range modes {
			a.width, a.height = width, 24
			m.open()
			a.clampCursor()
			out := a.View()
			if out == "" {
				t.Errorf("%s at %d columns rendered nothing", m.name, width)
			}
			for _, line := range strings.Split(out, "\n") {
				if len([]rune(stripANSI(line))) > width {
					t.Errorf("%s at %d columns has a line of %d cells",
						m.name, width, len([]rune(stripANSI(line))))
					break
				}
			}
		}
	}
}

// stripANSI removes the escape sequences a styled line carries, so a width
// check counts cells rather than bytes.
func stripANSI(s string) string {
	var out strings.Builder
	inEscape := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			inEscape = true
		case inEscape && r == 'm':
			inEscape = false
		case !inEscape:
			out.WriteRune(r)
		}
	}
	return out.String()
}

func TestParseFlags(t *testing.T) {
	opts, err := parseFlags([]string{"--demo", "--config", "home"}, os.Stdout)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !opts.demo || opts.config != "home" {
		t.Errorf("opts = %+v", opts)
	}
	// An explicitly empty -sudo must disable escalation rather than read as
	// "not given".
	opts, err = parseFlags([]string{"--sudo", ""}, os.Stdout)
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if !opts.sudoSet || opts.sudo != "" {
		t.Errorf("--sudo \"\" gave %+v, want an explicit empty prefix", opts)
	}
}
