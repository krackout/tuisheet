package main

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"tuisheet/internal/sheet"
)

func (a *app) handleSearchInputKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		a.mode = modeNormal
		a.searchInput = ""
		a.searchCursor = 0
		a.message = "Search cancelled"
		return nil
	case "enter":
		q := strings.TrimSpace(a.searchInput)
		if q == "" {
			a.message = "Search cancelled (empty query)"
			a.mode = modeNormal
			return nil
		}
		a.doSearch(q)
		return nil
	case "left":
		lineMoveLeft(&a.searchCursor)
	case "right":
		lineMoveRight(a.searchInput, &a.searchCursor)
	case "home", "ctrl+a":
		lineHome(&a.searchCursor)
	case "end", "ctrl+e":
		lineEnd(a.searchInput, &a.searchCursor)
	case "backspace", "ctrl+h":
		lineBackspace(&a.searchInput, &a.searchCursor)
	default:
		if len(msg.Runes) > 0 {
			lineInsert(&a.searchInput, &a.searchCursor, string(msg.Runes))
		}
	}
	return nil
}

func (a *app) doSearch(query string) {
	lower := strings.ToLower(query)
	var results []searchResult
	indices := []int{}
	if strings.EqualFold(a.searchScope, "Sheet") {
		indices = []int{a.activeSheet}
	} else {
		for i := range a.sheets {
			indices = append(indices, i)
		}
	}
	for _, si := range indices {
		sh := a.sheets[si]
		// Union of cells and formulas
		coords := make(map[sheet.Coord]bool)
		cells := sh.Cells()
		formulas := sh.Formulas()
		for c := range cells {
			coords[c] = true
		}
		for c := range formulas {
			coords[c] = true
		}
		for c := range coords {
			display := sh.Display(c)
			raw := sh.Raw(c)
			formula := formulas[c]
			if strings.Contains(strings.ToLower(display), lower) || strings.Contains(strings.ToLower(raw), lower) || strings.Contains(strings.ToLower(formula), lower) {
				results = append(results, searchResult{
					sheetIdx:  si,
					sheetName: a.sheetNames[si],
					coord:     c,
					display:   display,
					raw:       raw,
				})
			}
		}
	}
	// Sort by sheet order then row then col
	sort.Slice(results, func(i, j int) bool {
		if results[i].sheetIdx != results[j].sheetIdx {
			return results[i].sheetIdx < results[j].sheetIdx
		}
		if results[i].coord.Row != results[j].coord.Row {
			return results[i].coord.Row < results[j].coord.Row
		}
		return results[i].coord.Col < results[j].coord.Col
	})
	a.searchResults = results
	a.searchSelected = 0
	a.searchScroll = 0
	if len(results) == 0 {
		a.mode = modeNormal
		a.message = fmt.Sprintf("Search %q: no results", query)
		return
	}
	a.mode = modeSearchResults
	a.message = fmt.Sprintf("Search %q: %d results", query, len(results))
}

func (a *app) handleSearchResultsKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q", "Q":
		a.mode = modeNormal
		return nil
	case "ctrl+f":
		// stay in results
		return nil
	case "enter":
		if len(a.searchResults) == 0 {
			a.mode = modeNormal
			return nil
		}
		r := a.searchResults[a.searchSelected]
		a.setActiveSheetNoReset(r.sheetIdx)
		a.active = r.coord
		a.ensureVisible()
		a.mode = modeNormal
		a.message = fmt.Sprintf("Go to %s:%s", r.sheetName, r.coord.String())
		return nil
	case "up":
		if a.searchSelected > 0 {
			a.searchSelected--
			if a.searchSelected < a.searchScroll {
				a.searchScroll = a.searchSelected
			}
		}
	case "down":
		if a.searchSelected < len(a.searchResults)-1 {
			a.searchSelected++
			// keep visible - will be adjusted in popup scroll window
		}
	case "pgup":
		step := a.popupPageStep()
		a.searchSelected -= step
		if a.searchSelected < 0 {
			a.searchSelected = 0
		}
		if a.searchSelected < a.searchScroll {
			a.searchScroll = a.searchSelected
		}
	case "pgdown":
		step := a.popupPageStep()
		a.searchSelected += step
		if a.searchSelected >= len(a.searchResults) {
			a.searchSelected = len(a.searchResults) - 1
		}
	case "home", "ctrl+a":
		a.searchSelected = 0
		a.searchScroll = 0
	case "end", "ctrl+e":
		a.searchSelected = len(a.searchResults) - 1
	}
	return nil
}

func (a *app) withSearchResultsPopup(lines []string) []string {
	boxW := max(44, a.width*8/10)
	if boxW > a.width-2 {
		boxW = a.width - 2
	}
	boxX := (a.width - boxW) / 2
	innerW := boxW - 2
	title := " Search - Enter to go to result - Ctrl+F to come back to these results "
	if displayWidth(title) > innerW {
		title = truncateDisplay(title, innerW)
	}
	// Build inner content: title + results
	var content []string
	content = append(content, style(sgrReverseVideo, fitDisplay(title, innerW)))
	scrollWindow := max(3, (a.height*7/10)-4)
	// Keep selected visible
	if a.searchSelected < a.searchScroll {
		a.searchScroll = a.searchSelected
	}
	if a.searchSelected >= a.searchScroll+scrollWindow {
		a.searchScroll = a.searchSelected - scrollWindow + 1
	}
	if a.searchScroll < 0 {
		a.searchScroll = 0
	}
	if maxOff := len(a.searchResults) - scrollWindow; maxOff >= 0 && a.searchScroll > maxOff {
		a.searchScroll = maxOff
	}
	end := a.searchScroll + scrollWindow
	if end > len(a.searchResults) {
		end = len(a.searchResults)
	}
	visible := a.searchResults[a.searchScroll:end]
	for i, r := range visible {
		idx := a.searchScroll + i
		line := fmt.Sprintf("%s:%s: %s", r.sheetName, r.coord.String(), r.display)
		if displayWidth(line) > innerW-1 {
			line = truncateDisplay(line, innerW-1)
		}
		prefix := "  "
		if idx == a.searchSelected {
			prefix = "> "
		}
		txt := prefix + line
		// Highlight is applied after the scrollbar pass below: fitting
		// already-styled text would cut its SGR reset and bleed the
		// highlight past the border.
		if idx == a.searchSelected {
			content = append(content, fitDisplay(txt, innerW-1)+" ")
		} else {
			content = append(content, fitDisplay(txt, innerW))
		}
	}
	if len(a.searchResults) == 0 {
		content = append(content, " (no results)")
	}
	innerH := len(content)
	boxH := innerH + 2
	if boxH > a.height {
		boxH = a.height
	}
	boxY := max(0, (a.height-boxH)/2)
	bar := scrollbarGlyphs(len(a.searchResults), scrollWindow, a.searchScroll)
	// Apply scrollbar: first content line is title (no bar), rest get bar glyph
	for j := 1; j < len(content); j++ {
		g := " "
		if j-1 < len(bar) {
			g = bar[j-1]
		}
		// Re-fit to innerW-1 then add bar glyph.
		content[j] = fitDisplay(content[j], innerW-1) + g
	}
	content[0] = fitDisplay(content[0], innerW)
	// Highlight the selected result now that all fitting is done, so the
	// style's SGR reset survives intact inside the borders.
	if len(a.searchResults) > 0 {
		if sel := a.searchSelected - a.searchScroll + 1; sel >= 1 && sel < len(content) {
			content[sel] = style(sgrTealBand, content[sel])
		}
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
