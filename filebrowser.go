package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"tuisheet/internal/xlsx"
)

func (a *app) enterOpenFileMode() {
	a.mode = modeOpenFile
	a.fileInput = ""
	a.fileInputCursor = 0
	a.fileDir = "."
	a.fileCursor = 0
	a.refreshFileList()
	a.message = "Retrieve .xlsx path: "
}

func (a *app) refreshFileList() {
	entries, err := os.ReadDir(a.fileDir)
	if err != nil {
		a.fileEntries = nil
		return
	}
	var out []fileEntry
	parent := filepath.Dir(a.fileDir)
	if a.fileDir == "." {
		if wd, err := os.Getwd(); err == nil {
			parent = filepath.Dir(wd)
		}
	}
	if parent == "" {
		parent = "."
	}
	out = append(out, fileEntry{name: "..", isDir: true, path: parent})
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			out = append(out, fileEntry{name: name, isDir: true, path: filepath.Join(a.fileDir, name)})
		} else if strings.HasSuffix(strings.ToLower(name), ".xlsx") {
			out = append(out, fileEntry{name: name, isDir: false, path: filepath.Join(a.fileDir, name)})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].isDir != out[j].isDir {
			return out[i].isDir
		}
		return strings.ToLower(out[i].name) < strings.ToLower(out[j].name)
	})
	a.fileEntries = out
	if a.fileCursor >= len(out) {
		a.fileCursor = max(0, len(out)-1)
	}
}

func (a *app) enterSaveMode() {
	if a.currentFile == "" {
		a.enterSaveAsMode()
		return
	}
	if err := a.saveXLSX(a.currentFile); err != nil {
		a.message = "Save failed: " + err.Error()
		return
	}
	a.mode = modeNormal
	a.message = "Saved " + a.currentFile
	wb, err := xlsx.Open(a.currentFile)
	if err != nil {
		a.message = "Saved but reopen failed: " + err.Error()
		return
	}
	if n := len(wb.Sheets); n != len(a.sheets) {
		a.message = fmt.Sprintf("Saved but sheet count %d != %d", n, len(a.sheets))
	}
}

func (a *app) enterSaveAsMode() {
	a.mode = modeSave
	a.saveInput = ""
	a.saveCursor = 0
	// reuse file browser for directory picking
	if a.currentFile != "" {
		a.fileDir = filepath.Dir(a.currentFile)
		if a.fileDir == "" || a.fileDir == "." {
			// keep as "." for relative
		}
	}
	if a.fileDir == "" {
		a.fileDir = "."
	}
	a.fileCursor = 0
	a.refreshFileList()
	a.message = "Save: choose dir/file, type new name below, Enter on xlsx to overwrite"
}

func (a *app) handleSaveKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		a.mode = modeNormal
		a.saveInput = ""
		a.saveCursor = 0
		a.message = "Save cancelled"
		a.pendingAction = actionNone
	case "enter":
		trimmed := strings.TrimSpace(a.saveInput)
		if trimmed != "" {
			// typed path handling (also handle typed dir navigation)
			// check if typed is existing dir (absolute or relative to fileDir)
			checkDir := func(p string) bool {
				if info, err := os.Stat(p); err == nil && info.IsDir() {
					return true
				}
				return false
			}
			if checkDir(trimmed) {
				a.fileDir = trimmed
				a.fileCursor = 0
				a.refreshFileList()
				return nil
			}
			if !filepath.IsAbs(trimmed) && !strings.Contains(trimmed, "/") && !strings.Contains(trimmed, "\\") {
				// check relative to fileDir
				cand := filepath.Join(a.fileDir, trimmed)
				if checkDir(cand) {
					a.fileDir = cand
					a.fileCursor = 0
					a.refreshFileList()
					return nil
				}
			}
			var filename string
			if filepath.IsAbs(trimmed) || strings.Contains(trimmed, "/") || strings.Contains(trimmed, "\\") {
				filename = ensureXlsxExt(trimmed)
			} else {
				filename = ensureXlsxExt(filepath.Join(a.fileDir, trimmed))
			}
			filename = filepath.Clean(filename)
			if fi, err := os.Stat(filename); err == nil && fi.IsDir() {
				a.message = "Path is a directory, enter a file name"
				return nil
			}
			dir := filepath.Dir(filename)
			if dir != "." && dir != "" {
				if _, err := os.Stat(dir); err != nil {
					if os.IsNotExist(err) {
						a.message = "Directory does not exist: " + dir
						return nil
					}
				}
			}
			if _, err := os.Stat(filename); err == nil {
				a.pendingSaveFile = filename
				a.mode = modeConfirmSaveOverwrite
				a.message = fmt.Sprintf("File %s exists, overwrite? (Y) or Esc to change name", filepath.Base(filename))
				return nil
			}
			if err := a.saveXLSX(filename); err != nil {
				a.message = "Save failed: " + err.Error()
				a.mode = modeNormal
				return nil
			}
			a.currentFile = filename
			a.saveInput = ""
			a.saveCursor = 0
			a.pendingSaveFile = ""
			a.message = "Saved " + filename
			act := a.pendingAction
			a.pendingAction = actionNone
			switch act {
			case actionQuit:
				return tea.Quit
			case actionRetrieve:
				a.enterOpenFileMode()
			default:
				a.mode = modeNormal
			}
		}
		// no typed input -> use browser selection
		if a.fileCursor < len(a.fileEntries) {
			entry := a.fileEntries[a.fileCursor]
			if entry.isDir {
				if entry.path == a.fileDir {
					a.message = "Already at top directory"
					return nil
				}
				a.fileDir = entry.path
				a.fileCursor = 0
				a.refreshFileList()
				return nil
			}
			a.pendingSaveFile = entry.path
			a.mode = modeConfirmSaveOverwrite
			a.message = fmt.Sprintf("File %s exists, overwrite? (Y) or Esc to change name", filepath.Base(entry.path))
			return nil
		}
		a.message = "Enter a file name or select a file from list"
		return nil
	case "up":
		if a.fileCursor > 0 {
			a.fileCursor--
		}
	case "down":
		if a.fileCursor < len(a.fileEntries)-1 {
			a.fileCursor++
		}
	case "pgup":
		page := max(1, (a.height*7/10)-4)
		a.fileCursor -= page
		if a.fileCursor < 0 {
			a.fileCursor = 0
		}
	case "pgdown":
		page := max(1, (a.height*7/10)-4)
		a.fileCursor += page
		if a.fileCursor >= len(a.fileEntries) {
			a.fileCursor = max(0, len(a.fileEntries)-1)
		}
	case "left":
		lineMoveLeft(&a.saveCursor)
	case "right":
		lineMoveRight(a.saveInput, &a.saveCursor)
	case "home", "ctrl+a":
		a.saveCursor = 0
	case "end", "ctrl+e":
		a.saveCursor = utf8.RuneCountInString(a.saveInput)
	case "backspace", "ctrl+h":
		lineBackspace(&a.saveInput, &a.saveCursor)
	default:
		if len(msg.Runes) > 0 {
			lineInsert(&a.saveInput, &a.saveCursor, string(msg.Runes))
		}
	}
	return nil
}

func currentDirLabel() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	base := filepath.Base(wd)
	if base == "" {
		return "/"
	}
	return base
}

func scrollbarGlyphs(total, windowSize, offset int) []string {
	glyphs := make([]string, max(0, windowSize))
	for i := range glyphs {
		glyphs[i] = " "
	}
	if total <= windowSize || total <= 0 || windowSize <= 0 {
		return glyphs
	}
	thumbLen := max(1, windowSize*windowSize/total)
	maxOff := total - windowSize
	pos := 0
	if offset < 0 {
		offset = 0
	}
	if maxOff > 0 {
		pos = offset * (windowSize - thumbLen) / maxOff
	}
	for i := 0; i < len(glyphs); i++ {
		glyphs[i] = "│"
	}
	for i := pos; i < min(windowSize, pos+thumbLen); i++ {
		glyphs[i] = "█"
	}
	return glyphs
}

func (a *app) withFileBrowserPopup(lines []string) []string {
	boxW := max(44, a.width*7/10)
	if boxW > a.width-2 {
		boxW = a.width - 2
	}
	boxX := (a.width - boxW) / 2
	innerW := boxW - 2

	// Build inner content (sans borders)
	var content []string
	dirLine := a.fileDir
	if dirLine == "." {
		dirLine = currentDirLabel()
	}
	dirText := fitDisplay(" Currect dir: "+dirLine+" ", innerW)
	content = append(content, style(sgrReverseVideo, dirText))

	entries := a.fileEntries
	scrollWindow := max(3, (a.height*7/10)-4)
	offset := 0
	if len(entries) > scrollWindow {
		offset = a.fileCursor - scrollWindow/2
		if offset < 0 {
			offset = 0
		}
		if maxOff := len(entries) - scrollWindow; offset > maxOff {
			offset = maxOff
		}
	}
	visible := entries[offset:]
	if len(visible) > scrollWindow {
		visible = visible[:scrollWindow]
	}
	// filter highlight for Open and SaveAs typing (reuse)
	filter := ""
	if a.mode == modeOpenFile {
		filter = strings.TrimSpace(a.fileInput)
	} else if a.mode == modeSave {
		filter = strings.TrimSpace(a.saveInput)
		// strip .xlsx extension for matching
		if strings.HasSuffix(strings.ToLower(filter), ".xlsx") {
			filter = filter[:len(filter)-5]
		}
	}
	filterLower := strings.ToLower(filter)
	for i, e := range visible {
		line := "  "
		if offset+i == a.fileCursor {
			line = "> "
		} else {
			line = "  "
		}
		if e.isDir {
			line += e.name + "/"
		} else {
			line += e.name
		}
		if offset+i == a.fileCursor {
			content = append(content, style(sgrTealBand, " "+line+" "))
		} else if filterLower != "" && !e.isDir && strings.Contains(strings.ToLower(e.name), filterLower) {
			content = append(content, style(sgrBold, " "+line+" "))
		} else {
			content = append(content, " "+line+" ")
		}
	}

	innerH := len(content)
	boxH := innerH + 2 // +2 for top/bottom borders
	if boxH > a.height {
		boxH = a.height
	}
	boxY := max(0, (a.height-boxH)/2)

	// Proportional scrollbar alongside the entry list (index 0 is the
	// directory header, which keeps the full inner width).
	bar := scrollbarGlyphs(len(entries), scrollWindow, offset)
	content[0] = fitDisplay(content[0], innerW)
	for j := 1; j < len(content); j++ {
		g := " "
		if j-1 < len(bar) {
			g = bar[j-1]
		}
		content[j] = fitDisplay(content[j], innerW-1) + g
	}

	for row := boxY; row < boxY+boxH && row < len(lines); row++ {
		i := row - boxY
		var boxLine string
		switch {
		case i == 0:
			boxLine = "┌" + strings.Repeat("─", innerW) + "┐"
		case i == boxH-1:
			boxLine = "└" + strings.Repeat("─", innerW) + "┘"
		default:
			ci := i - 1
			if ci >= 0 && ci < len(content) {
				boxLine = "│" + content[ci] + "│"
			} else {
				boxLine = "│" + strings.Repeat(" ", innerW) + "│"
			}
		}
		lines[row] = overlayPlain(lines[row], boxLine, boxX, boxW)
	}
	drawBoxShadow(lines, boxX, boxY, boxW, boxH)
	return lines
}
