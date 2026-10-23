package main

import (
	"strings"
	"testing"

	"github.com/tui-tools/tui-kit/theme"
)

// boxText is what a reader can read in a rendered dialog: every line with its
// escapes and the box drawing removed, joined, and its runs of spaces folded,
// so a sentence that was wrapped across lines reads as one again.
func boxText(frame string) string {
	var words []string
	for _, line := range strings.Split(frame, "\n") {
		line = strings.Map(func(r rune) rune {
			if strings.ContainsRune("│╭╮╰╯─", r) {
				return ' '
			}
			return r
		}, stripANSI(line))
		words = append(words, strings.Fields(line)...)
	}
	return strings.Join(words, " ")
}

// TestDeleteConfigDialogShowsItsWholeTextAtEveryWidth: the typed confirm used
// to render its body as one line under a MaxWidth, which truncates. At 160
// columns the warning lost its last sentence, "This cannot be undone.", and
// the box lost its right border. Rendered with the real theme, so the border
// and padding are counted as they are on a terminal.
func TestDeleteConfigDialogShowsItsWholeTextAtEveryWidth(t *testing.T) {
	for _, width := range []int{160, 80, 60} {
		a, _ := newTestApp(t)
		a.theme = theme.New()
		a.width, a.height = width, 45
		openConfigScreen(t, a, "home")
		press(t, a, "D")
		if a.mode != modeTypedConfirm {
			t.Fatalf("D opened mode %v, want the typed confirm", a.mode)
		}

		frame := a.View()
		text := boxText(frame)
		for _, want := range []string{
			strings.Join(strings.Fields(a.typed.body), " "),
			"This cannot be undone.",
			"$ " + a.typed.command,
			`Type "home" and press enter to confirm:`,
			"enter confirm esc cancel",
		} {
			if !strings.Contains(text, want) {
				t.Errorf("at %d columns the dialog does not show %q:\n%s",
					width, want, frame)
			}
		}

		// Nothing is wider than the terminal, and every row of the box is
		// closed on the right: a truncated row loses its border first.
		rows := 0
		for _, line := range strings.Split(frame, "\n") {
			plain := strings.TrimRight(stripANSI(line), " ")
			if cells := len([]rune(plain)); cells > width {
				t.Errorf("at %d columns a line is %d cells wide", width, cells)
			}
			trimmed := strings.TrimLeft(plain, " ")
			if strings.HasPrefix(trimmed, "│") {
				rows++
				if !strings.HasSuffix(trimmed, "│") {
					t.Errorf("at %d columns a dialog row is cut: %q", width, trimmed)
				}
			}
		}
		if rows == 0 {
			t.Fatalf("at %d columns no dialog border was drawn:\n%s", width, frame)
		}
	}
}
