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
//
// Everything is wrapped to the dialog's inner width with the kit's own
// wrappers, the same ones ui.Confirm uses, so nothing in it is cut. It used to
// render the body as one long line and cap the box with MaxWidth, which
// truncates rather than wraps: on a 160-column terminal the delete-config
// warning lost its last sentence, "This cannot be undone.", together with the
// right-hand border.
func (c *typedConfirm) View(t theme.Theme, width, height int) string {
	content := typedConfirmContent(t, width)

	lines := wrapStyled(ui.Wrap(c.title, content), t.Danger)
	if c.body != "" {
		lines = append(lines, "")
		lines = append(lines, wrapStyled(ui.WrapBody(c.body, content), t.Base)...)
	}
	if c.command != "" {
		lines = append(lines, "", t.Muted.Render("Command to run:"))
		lines = append(lines, wrapStyled(ui.WrapBody("$ "+c.command, content), t.Command)...)
	}
	lines = append(lines, "")
	lines = append(lines, wrapStyled(ui.Wrap("Type "+strconv.Quote(c.word)+
		" and press enter to confirm:", content), t.Base)...)
	// The input scrolls its own text inside the width it is given; without
	// one a long answer would widen the box past the screen.
	c.input.Model.Width = max(content-lipgloss.Width(c.input.Model.Prompt)-1, 1)
	lines = append(lines, c.input.Model.View(),
		"",
		t.Key.Render("enter")+t.KeyDesc.Render(" confirm    ")+
			t.Key.Render("esc")+t.KeyDesc.Render(" cancel"))

	box := t.Dialog.Render(strings.Join(lines, "\n"))
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// typedConfirmMinContent is the narrowest text column the dialog lays out to,
// matching the kit's dialogs: below it the terminal clips the box instead.
const typedConfirmMinContent = 20

// typedConfirmContent is the width the dialog's text has to work with: the
// screen less the two cells of margin on each side the kit's dialogs keep,
// less the dialog style's own border and padding.
func typedConfirmContent(t theme.Theme, width int) int {
	return max(width-4-t.Dialog.GetHorizontalFrameSize(), typedConfirmMinContent)
}

// wrapStyled applies one style to every line of a wrapped block.
func wrapStyled(lines []string, style lipgloss.Style) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = style.Render(line)
	}
	return out
}
