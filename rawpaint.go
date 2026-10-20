package main

// Message-driven painting (glow-style): main runs the program with
// tea.WithoutRenderer and this painter owns the tty output side: every
// processed message paints the fresh View, with no frame ticker, so idle
// CPU stays near zero. This is the only rendering path.
//
// v1 WithoutRenderer semantics (verified in bubbletea@v1.3.10 source):
//   - initTerminal early-returns for nilRenderer: NO raw mode, NO tty init,
//     NO initial WindowSizeMsg (handleResize needs ttyOutput), NO mouse/
//     bracketed-paste/alt-screen sequences.
//   - The input byte pump (initCancelReader/readLoop on p.input) DOES run,
//     so parsed KeyMsg/MouseMsg keep flowing once stdin is raw.
// Hence this file owns: raw mode (+restore), output sequences, initial size
// + SIGWINCH forwarding. Not for merge as-is: full clear+rewrite per message
// (no cell diffing).

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"golang.org/x/term"
)

// rawPainter paints full frames directly to the terminal from a single
// dedicated worker goroutine. paintModel.Update submits each fresh View
// synchronously into the latest-frame slot; the worker paints only the
// newest available frame, so scroll bursts coalesce instead of queueing
// one goroutine + full frame string per message.
type rawPainter struct {
	w io.Writer

	// inFd is the resolved console input fd. A literal 0 works on Unix
	// (fd 0 is stdin) but is a NULL handle on Windows, where
	// GetConsoleMode would fail with ERROR_INVALID_HANDLE.
	inFd       int
	console    *os.File // owned console handle (CONIN$), closed on stop
	stdinState *term.State
	// restoreOutput undoes setupConsoleOutput (Windows VT processing).
	restoreOutput func()
	done          chan struct{}
	wake          chan struct{}
	wg            sync.WaitGroup

	mu          sync.Mutex
	latest      string
	pending     bool
	stopped     bool
	lastWritten string
}

// resolveConsoleFd reports the fd to use for raw console input, or false
// when f is not a terminal (redirected stdin, pipe, non-console host).
func resolveConsoleFd(f *os.File) (int, bool) {
	if f == nil {
		return 0, false
	}
	fd := int(f.Fd())
	if !term.IsTerminal(fd) {
		return 0, false
	}
	return fd, true
}

func newRawPainter(w io.Writer) (*rawPainter, error) {
	// Raw mode: bubbletea skips tty init without a renderer, so own it.
	// stdin is what the input pump reads.
	fd, owned, err := openConsole()
	if err != nil {
		return nil, err
	}
	st, err := term.MakeRaw(fd)
	if err != nil {
		if owned != nil {
			_ = owned.Close()
		}
		src := "stdin"
		if owned != nil {
			src = "CONIN$"
		}
		return nil, fmt.Errorf("raw mode setup failed on %s (fd %d): %w", src, fd, err)
	}
	rp := newRawPainterWithState(w, st, fd, owned)
	if f, ok := w.(*os.File); ok {
		restore, err := setupConsoleOutput(f)
		if err != nil {
			if owned != nil {
				_ = owned.Close()
			}
			return nil, err
		}
		rp.restoreOutput = restore
	}
	return rp, nil
}

// newRawPainterWithState builds a painter without touching the tty, so
// tests can inject a buffer writer and no stdin state.
func newRawPainterWithState(w io.Writer, st *term.State, fd int, owned *os.File) *rawPainter {
	return &rawPainter{
		w:          w,
		inFd:       fd,
		console:    owned,
		stdinState: st,
		done:       make(chan struct{}),
		wake:       make(chan struct{}, 1),
	}
}

func (p *rawPainter) start() {
	// alt screen enter, cursor hide, bracketed paste, mouse cell-motion+SGR.
	// Mirrors what bubbletea enables for WithAltScreen+WithMouseCellMotion.
	_, _ = io.WriteString(p.w, "\x1b[?1049h\x1b[?25l\x1b[?2004h\x1b[?1002h\x1b[?1006h")
	p.wg.Add(1)
	go p.run()
}

// run is the sole terminal writer. It exits on done; stop() joins it
// before emitting teardown sequences, so no frame can land after them.
func (p *rawPainter) run() {
	defer p.wg.Done()
	for {
		select {
		case <-p.done:
			return
		case <-p.wake:
			p.mu.Lock()
			frame := p.latest
			p.pending = false
			p.mu.Unlock()
			p.writeFrame(frame)
		}
	}
}

func (p *rawPainter) stop() {
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	p.stopped = true
	p.mu.Unlock()
	close(p.done)
	p.wg.Wait()
	// Drain one final pending frame (suppression-aware) so shutdown never
	// loses the last visible state, then tear down in order.
	p.mu.Lock()
	frame, pending := p.latest, p.pending
	p.pending = false
	p.mu.Unlock()
	if pending {
		p.writeFrame(frame)
	}
	_, _ = io.WriteString(p.w, "\x1b[?1002l\x1b[?1006l\x1b[?2004l\x1b[?25h\x1b[?1049l")
	if p.restoreOutput != nil {
		p.restoreOutput()
		p.restoreOutput = nil
	}
	if p.stdinState != nil {
		_ = term.Restore(p.inFd, p.stdinState)
		p.stdinState = nil
	}
	if p.console != nil {
		_ = p.console.Close()
		p.console = nil
	}
}

// submit stores frame as the newest pending one and wakes the worker.
// Non-blocking: bursts coalesce in the slot instead of queueing work.
// Submissions after stop are dropped.
func (p *rawPainter) submit(frame string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopped {
		return
	}
	p.latest = frame
	p.pending = true
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// writeFrame emits one frame, skipping it when identical to the last
// written one. Must only be called from the worker or stop (post-join),
// the two places that can prove no other write is in flight.
func (p *rawPainter) writeFrame(frame string) {
	p.mu.Lock()
	if frame == p.lastWritten {
		p.mu.Unlock()
		return
	}
	p.lastWritten = frame
	p.mu.Unlock()

	// In-place rewrite from home: View always covers the full screen
	// (trimOrPad to height x width), so no erase is needed. Erasing
	// first (\x1b[2J) flashes, especially visible on popup scroll.
	// Single pass: pre-size once, convert newlines inline, write once.
	var b strings.Builder
	b.Grow(len(frame) + 64)
	b.WriteString("\x1b[?2026h\x1b[H")
	for i := 0; i < len(frame); i++ {
		if frame[i] == '\n' {
			b.WriteString("\r\n")
		} else {
			b.WriteByte(frame[i])
		}
	}
	b.WriteString("\x1b[?2026l")
	_, _ = io.WriteString(p.w, b.String())
}

// paintModel wraps the real model: after every Update it paints the fresh
// View. Bubbletea never calls View for output when the renderer is disabled,
// so the wrapper drives painting itself — render-on-message, no ticker.
type paintModel struct {
	inner   *app
	painter *rawPainter
}

func (m paintModel) Init() tea.Cmd {
	m.painter.submit(m.inner.View())
	return m.inner.Init()
}

// View satisfies tea.Model; output goes through the worker, never this.
func (m paintModel) View() string {
	return m.inner.View()
}

func (m paintModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	nm, cmd := m.inner.Update(msg)
	if na, ok := nm.(*app); ok {
		m.inner = na
	}
	m.painter.submit(m.inner.View())
	return m, cmd
}

// maybeRawPainter returns a painter + model wrapper for message-driven
// rendering. program is needed for resize forwarding; call watchResize
// after NewProgram. There is no fallback path: a setup failure is loud.
func maybeRawPainter(a *app) (*rawPainter, tea.Model, error) {
	rp, err := newRawPainter(os.Stdout)
	if err != nil {
		return nil, a, err
	}
	// Synchronous initial size: watchResize only forwards later changes,
	// so size the app before the first frame instead of assuming 80x24.
	if w, h, gerr := term.GetSize(rp.inFd); gerr == nil && w > 0 && h > 0 {
		a.width = max(1, w)
		a.height = max(1, h)
		a.ensureVisible()
	}
	return rp, paintModel{inner: a, painter: rp}, nil
}
