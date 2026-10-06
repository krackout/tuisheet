//go:build windows

package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/sys/windows"
	"golang.org/x/term"
)

// setupConsoleOutput enables virtual-terminal processing on console output
// so ANSI escape sequences are interpreted instead of printed as glyphs
// (a bare ESC shows as ← without it). Non-console output is left alone so
// piping keeps working; a console that refuses the mode is a hard error,
// mirroring bubbletea's own startup behavior.
func setupConsoleOutput(f *os.File) (func(), error) {
	h := windows.Handle(f.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(h, &mode); err != nil {
		return func() {}, nil
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		return func() {}, nil
	}
	if err := windows.SetConsoleMode(h, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err != nil {
		return nil, fmt.Errorf("cannot enable virtual-terminal processing on console output: %w; tuisheet needs a terminal with ANSI support (Windows Terminal or Windows 10+ conhost)", err)
	}
	return func() {
		windows.SetConsoleMode(h, mode)
	}, nil
}

// openConsole resolves console input for raw mode. A literal fd 0 is a
// NULL handle on Windows, so prefer stdin's real handle when it is a
// console; otherwise fall back to CONIN$, which exists whenever the
// process has a console even with stdin redirected. Without any console,
// fail with an actionable message instead of a raw syscall error.
func openConsole() (fd int, owned *os.File, err error) {
	if fd, ok := resolveConsoleFd(os.Stdin); ok {
		return fd, nil, nil
	}
	f, ferr := os.OpenFile("CONIN$", os.O_RDWR, 0)
	if ferr != nil {
		return 0, nil, fmt.Errorf("no console available for raw input (stdin is not a terminal and CONIN$ unavailable): %v; run tuisheet in Windows Terminal or conhost", ferr)
	}
	return int(f.Fd()), f, nil
}

// watchResize sends the initial size once. Windows has no SIGWINCH, so
// later resizes are not forwarded on this platform. The send runs in a
// goroutine: Program.Send blocks until the event loop receives, and this
// is called before p.Run() starts it — a synchronous send would deadlock
// startup on a real terminal.
func (p *rawPainter) watchResize(prog *tea.Program) {
	go func() {
		if w, h, err := term.GetSize(p.inFd); err == nil && w > 0 && h > 0 {
			prog.Send(tea.WindowSizeMsg{Width: w, Height: h})
		}
	}()
}
