//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || aix || zos

package main

import (
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// watchResize sends the initial size and forwards SIGWINCH, since bubbletea
// size reporting is tied to tty output init (skipped without a renderer).
func (p *rawPainter) watchResize(prog *tea.Program) {
	sendSize := func() {
		if w, h, err := term.GetSize(0); err == nil && w > 0 && h > 0 {
			prog.Send(tea.WindowSizeMsg{Width: w, Height: h})
		}
	}
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		defer signal.Stop(ch)
		sendSize()
		for {
			select {
			case <-p.done:
				return
			case <-ch:
				sendSize()
			}
		}
	}()
}
