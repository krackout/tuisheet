//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris || aix || zos

package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// setupConsoleOutput is a no-op on Unix: ANSI sequences need no console
// mode switch there.
func setupConsoleOutput(_ *os.File) (func(), error) {
	return func() {}, nil
}

// openConsole resolves console input for raw mode. Unix: stdin only —
// fd 0 is stdin here, so no CONIN$ equivalent is needed. Redirected
// stdin fails loud (same as before).
func openConsole() (fd int, owned *os.File, err error) {
	if fd, ok := resolveConsoleFd(os.Stdin); ok {
		return fd, nil, nil
	}
	return 0, nil, fmt.Errorf("no console available for raw input (stdin is not a terminal); tuisheet needs an interactive terminal")
}

// watchResize sends the initial size and forwards SIGWINCH, since bubbletea
// size reporting is tied to tty output init (skipped without a renderer).
func (p *rawPainter) watchResize(prog *tea.Program) {
	sendSize := func() {
		if w, h, err := term.GetSize(p.inFd); err == nil && w > 0 && h > 0 {
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
