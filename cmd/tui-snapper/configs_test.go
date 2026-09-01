package main

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-snapper/internal/snapper"
)

// The config screen drives the same contract the snapshot one does: a key
// starts a form, the form ends in a preview, and nothing runs until the
// preview has been answered. These tests press the keys a user presses.

// typeText sends a string one key at a time.
func typeText(t *testing.T, a *app, text string) {
	t.Helper()
	for _, r := range text {
		press(t, a, string(r))
	}
}

// clearInput empties the open text prompt, so a seeded value can be replaced
// rather than appended to.
func clearInput(t *testing.T, a *app) {
	t.Helper()
	a.input.Model.SetValue("")
}

// openConfigScreen moves the app onto the config list, on a named config.
func openConfigScreen(t *testing.T, a *app, name string) {
	t.Helper()
	press(t, a, "s")
	if a.mode != modeConfigs {
		t.Fatalf("s opened mode %v, want the config screen", a.mode)
	}
	for i, config := range a.configs {
		if config.Name == name {
			a.configCursor = i
			return
		}
	}
	t.Fatalf("config %q is not in the demo data", name)
}

func TestCreateConfigFromTheConfigScreen(t *testing.T) {
	a, fake := newTestApp(t)
	openConfigScreen(t, a, "root")

	press(t, a, "n")
	if a.mode != modeInput {
		t.Fatalf("n opened mode %v, want the subvolume prompt", a.mode)
	}
	typeText(t, a, "/srv")
	press(t, a, "enter")

	// The name prompt opens seeded with the name that subvolume usually gets.
	if a.mode != modeInput {
		t.Fatalf("after the subvolume the app is in mode %v, want the name prompt", a.mode)
	}
	if got := a.input.Value(); got != "srv" {
		t.Errorf("the name prompt opened on %q, want the suggestion \"srv\"", got)
	}
	press(t, a, "enter")

	if a.mode != modeConfirm {
		t.Fatalf("after the last field the app is in mode %v, want the dialog", a.mode)
	}
	want := "snapper -c srv create-config -f btrfs /srv"
	if a.confirm.Command != want {
		t.Errorf("preview = %q, want %q", a.confirm.Command, want)
	}
	if len(fake.Commands()) != 0 {
		t.Fatal("collecting the fields must not run anything")
	}

	press(t, a, "y")
	ran := fake.Commands()
	if len(ran) != 1 {
		t.Fatalf("ran %d commands, want 1", len(ran))
	}
	if got := a.backend.Preview(ran[0]); got != want {
		t.Errorf("ran %q, but the preview promised %q", got, want)
	}
	// The config list is re-read afterwards, because the machine is the source
	// of truth for what exists.
	found := false
	for _, config := range a.configs {
		if config.Name == "srv" && config.Subvolume == "/srv" {
			found = true
		}
	}
	if !found {
		t.Errorf("the new config is not in the reloaded list: %+v", a.configs)
	}
}

func TestCreateConfigRefusesAPathSnapperCannotManage(t *testing.T) {
	a, fake := newTestApp(t)
	openConfigScreen(t, a, "root")

	press(t, a, "n")
	typeText(t, a, "/boot") // vfat in the demo mount table
	press(t, a, "enter")

	// The path is checked against the machine before the form goes on, and the
	// same prompt comes back with the reason.
	if a.mode != modeInput {
		t.Fatalf("a refused path left the app in mode %v, want the prompt again", a.mode)
	}
	if !strings.Contains(a.status, "vfat") {
		t.Errorf("the refusal does not name the filesystem: %q", a.status)
	}
	if len(fake.Commands()) != 0 {
		t.Errorf("a refused path ran %d commands", len(fake.Commands()))
	}

	// A path with a traversal in it never reaches the machine at all.
	clearInput(t, a)
	typeText(t, a, "/srv/../etc")
	press(t, a, "enter")
	if a.mode != modeInput {
		t.Fatalf("an unclean path left the app in mode %v, want the prompt again", a.mode)
	}
	if len(fake.Commands()) != 0 {
		t.Errorf("an unclean path ran %d commands", len(fake.Commands()))
	}
}

func TestEditConfigLimitsWritesOnlyWhatChanged(t *testing.T) {
	a, fake := newTestApp(t)
	openConfigScreen(t, a, "home")

	press(t, a, "a")
	// The form opens on NUMBER_LIMIT, seeded with what get-config reports.
	if a.mode != modeInput {
		t.Fatalf("a opened mode %v, want the first settings prompt", a.mode)
	}
	if got := a.input.Value(); got != "50" {
		t.Errorf("NUMBER_LIMIT opened on %q, want the current value 50", got)
	}
	clearInput(t, a)
	typeText(t, a, "20")
	press(t, a, "enter")

	// Every other prompt is accepted as it stands, so only the first key
	// differs from what the machine reported.
	for a.mode == modeInput || a.mode == modePick {
		press(t, a, "enter")
	}

	if a.mode != modeConfirm {
		t.Fatalf("after the form the app is in mode %v, want the dialog", a.mode)
	}
	want := "snapper -c home set-config NUMBER_LIMIT=20"
	if a.confirm.Command != want {
		t.Errorf("preview = %q, want %q", a.confirm.Command, want)
	}
	if len(fake.Commands()) != 0 {
		t.Fatal("the form must not run anything")
	}

	press(t, a, "y")
	if len(fake.Commands()) != 1 {
		t.Fatalf("ran %d commands, want 1", len(fake.Commands()))
	}
	settings, err := fake.Settings(t.Context(), "home")
	if err != nil {
		t.Fatalf("Settings: %v", err)
	}
	if settings["NUMBER_LIMIT"] != "20" {
		t.Errorf("NUMBER_LIMIT = %q after the change, want 20", settings["NUMBER_LIMIT"])
	}
	if settings["ALLOW_GROUPS"] != "wheel" {
		t.Errorf("a key outside the form was rewritten: ALLOW_GROUPS = %q",
			settings["ALLOW_GROUPS"])
	}
}

func TestEditConfigRefusesAValueSnapperWouldNotTake(t *testing.T) {
	a, fake := newTestApp(t)
	openConfigScreen(t, a, "home")

	press(t, a, "a")
	clearInput(t, a)
	typeText(t, a, "lots")
	press(t, a, "enter")

	// The prompt comes back rather than the form failing at the end.
	if a.mode != modeInput {
		t.Fatalf("a rejected value left the app in mode %v, want the prompt again", a.mode)
	}
	if a.status == "" {
		t.Error("a rejected value should say why")
	}
	if len(fake.Commands()) != 0 {
		t.Errorf("a rejected value ran %d commands", len(fake.Commands()))
	}
}

func TestEditConfigWithNothingChangedBuildsNothing(t *testing.T) {
	a, fake := newTestApp(t)
	openConfigScreen(t, a, "home")

	press(t, a, "a")
	for a.mode == modeInput || a.mode == modePick {
		press(t, a, "enter")
	}
	if a.mode == modeConfirm {
		t.Fatalf("an unchanged form opened a dialog for %q", a.confirm.Command)
	}
	if len(fake.Commands()) != 0 {
		t.Errorf("an unchanged form ran %d commands", len(fake.Commands()))
	}
	if a.status == "" {
		t.Error("an unchanged form should say that there is nothing to write")
	}
}

func TestDeleteConfigNeedsItsNameTyped(t *testing.T) {
	a, fake := newTestApp(t)
	openConfigScreen(t, a, "home")

	press(t, a, "D")
	if a.mode != modeTypedConfirm {
		t.Fatalf("D opened mode %v, want the typed confirm", a.mode)
	}
	if want := "snapper -c home delete-config"; a.typed.command != want {
		t.Errorf("preview = %q, want %q", a.typed.command, want)
	}
	// The dialog says what goes with the config: the demo home config holds
	// three real snapshots.
	if !strings.Contains(a.typed.body, "3 snapshots") {
		t.Errorf("the dialog does not say what is lost: %q", a.typed.body)
	}

	// The wrong word runs nothing.
	typeText(t, a, "yes")
	press(t, a, "enter")
	if len(fake.Commands()) != 0 {
		t.Fatalf("a wrong word ran %d commands", len(fake.Commands()))
	}
	if a.mode != modeConfigs {
		t.Errorf("after a wrong word the app is in mode %v, want the config screen", a.mode)
	}

	// The config's own name runs exactly the previewed command.
	press(t, a, "D")
	preview := a.typed.command
	typeText(t, a, "home")
	press(t, a, "enter")
	ran := fake.Commands()
	if len(ran) != 1 {
		t.Fatalf("ran %d commands, want 1", len(ran))
	}
	if got := a.backend.Preview(ran[0]); got != preview {
		t.Errorf("ran %q, but the preview promised %q", got, preview)
	}
	for _, config := range a.configs {
		if config.Name == "home" {
			t.Error("the config is still listed after a confirmed delete")
		}
	}
}

func TestDeleteConfigRefusesTheRootConfig(t *testing.T) {
	a, fake := newTestApp(t)
	openConfigScreen(t, a, snapper.ProtectedConfig)

	press(t, a, "D")
	if a.mode != modeConfigs {
		t.Errorf("D on the root config opened mode %v, want no dialog at all", a.mode)
	}
	if a.typed != nil {
		t.Error("the root config got a deletion dialog")
	}
	if len(fake.Commands()) != 0 {
		t.Errorf("the root config ran %d commands", len(fake.Commands()))
	}
	if !strings.Contains(a.status, snapper.ProtectedConfig) {
		t.Errorf("the refusal does not name the config: %q", a.status)
	}
}

func TestEveryConfigActionKeyStopsAtADialog(t *testing.T) {
	// The same guard the snapshot table has: no key on this screen may reach
	// the backend on its own.
	for _, spec := range snapper.ConfigActions {
		t.Run(string(spec.Action), func(t *testing.T) {
			a, fake := newTestApp(t)
			openConfigScreen(t, a, "home")
			press(t, a, spec.Key)
			if len(fake.Commands()) != 0 {
				t.Errorf("%q ran %d commands before any confirmation",
					spec.Key, len(fake.Commands()))
			}
			if a.mode == modeConfigs && a.status == "" {
				t.Errorf("%q did nothing and said nothing", spec.Key)
			}
		})
	}
}

func TestConfigScreenOpensWithNoConfigsAtAll(t *testing.T) {
	// A machine snapper has never been set up on is the one that most needs
	// the key that sets it up, so the screen has to be reachable there.
	a, _ := newTestApp(t)
	a.configs = nil
	a.config = snapper.Config{}
	press(t, a, "s")
	if a.mode != modeConfigs {
		t.Fatalf("s opened mode %v on a machine with no configs", a.mode)
	}
	press(t, a, "n")
	if a.mode != modeInput {
		t.Errorf("n opened mode %v, want the subvolume prompt", a.mode)
	}
}

func TestConfigSubcommand(t *testing.T) {
	for _, tc := range []struct {
		argv         []string
		kind, config string
	}{
		{[]string{"snapper", "-c", "srv", "create-config", "-f", "btrfs", "/srv"},
			"create-config", "srv"},
		{[]string{"snapper", "--no-dbus", "-c", "home", "delete-config"},
			"delete-config", "home"},
		{[]string{"snapper", "-c", "home", "set-config", "NUMBER_LIMIT=20"},
			"set-config", "home"},
		// A snapshot whose description happens to name a config command is a
		// snapshot command, and must not send the app off to re-read configs.
		{[]string{"snapper", "-c", "root", "create", "--description", "set-config"},
			"", ""},
		{[]string{"snapper", "-c", "root", "delete", "42"}, "", ""},
	} {
		kind, config := configSubcommand(tc.argv)
		if kind != tc.kind || config != tc.config {
			t.Errorf("configSubcommand(%v) = %q %q, want %q %q",
				tc.argv, kind, config, tc.kind, tc.config)
		}
	}
}
