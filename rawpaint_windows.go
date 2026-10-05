//go:build windows

package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// watchResize sends the initial size once. Windows has no SIGWINCH, so
// later resizes are not forwarded on this platform.
func (p *rawPainter) watchResize(prog *tea.Program) {
	if w, h, err := term.GetSize(0); err == nil && w > 0 && h > 0 {
		prog.Send(tea.WindowSizeMsg{Width: w, Height: h})
	}
}
