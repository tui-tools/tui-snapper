package main

import (
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/tui-tools/tui-kit/runner"
	"github.com/tui-tools/tui-kit/theme"
	"github.com/tui-tools/tui-kit/ui"
)

// typedConfirm is the heavier confirm dialog: it previews the exact command
// like the yes/no one, and then asks for a word to be typed out before it will
// run anything. A single keystroke is the right weight for deleting a
// snapshot; it is the wrong weight for deleting the config that holds them
// all, which is what this dialog stands in front of.
type typedConfirm struct {
	title   string
	body    string
	command string
	// word is what has to be typed, and is always the thing being destroyed.
	word  string
	input ui.Input
	// cmd is what runs once the word matches.
	cmd runner.Command
	// done reports that the dialog finished; answered tells a wrong word from
	// a cancel, so the status line can say which happened.
	done      bool
	answered  bool
	confirmed bool
}

// typedConfirmSpec is what a typed confirm needs to be built.
type typedConfirmSpec struct {
	title   string
	body    string
	command string
	word    string
	cmd     runner.Command
}

// newTypedConfirm builds the dialog for one command.
func newTypedConfirm(spec typedConfirmSpec) *typedConfirm {
	return &typedConfirm{
		title:   spec.title,
		body:    spec.body,
		command: spec.command,
		word:    spec.word,
		cmd:     spec.cmd,
		input:   ui.NewInput("", spec.word, ""),
	}
}

// Update forwards a key to the text input. The dialog confirms only when the
// submitted text is exactly the word it asked for.
func (c *typedConfirm) Update(msg tea.Msg) tea.Cmd {
	cmd, _ := c.input.Update(msg)
	if !c.input.Done {
		return cmd
	}
	c.done = true
	c.answered = c.input.Accepted
	c.confirmed = c.input.Accepted && c.input.Value() == c.word
	return cmd
}

// View renders the dialog: what is about to happen, the exact command, and the
// prompt.
func (c *typedConfirm) View(t theme.Theme, width, height int) string {
	lines := []string{t.Danger.Render(c.title)}
	if c.body != "" {
		lines = append(lines, "", t.Base.Render(c.body))
	}
	if c.command != "" {
		lines = append(lines, "",
			t.Muted.Render("Command to run:"),
			t.Command.Render("$ "+c.command))
	}
	lines = append(lines, "",
		t.Base.Render("Type "+strconv.Quote(c.word)+" and press enter to confirm:"),
		c.input.Model.View(),
		"",
		t.Key.Render("enter")+t.KeyDesc.Render(" confirm    ")+
			t.Key.Render("esc")+t.KeyDesc.Render(" cancel"))

	box := t.Dialog.MaxWidth(max(width-4, 20)).Render(strings.Join(lines, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
