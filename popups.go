package main

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mattn/go-runewidth"

	"tuisheet/internal/buildinfo"
)

func (a *app) withPopup(lines []string) []string {
	var menu []string
	switch a.popupContext {
	case 1:
		menu = rowPopupMenu
	case 2:
		menu = colPopupMenu
	default:
		menu = cellPopupMenu
	}
	width := min(24, max(1, a.width))
	x := min(max(0, a.popupCol), max(0, a.width-width))
	y := min(max(1, a.popupRow), max(1, a.height-len(menu)-1))
	for i, item := range menu {
		row := y + i
		if row < 0 || row >= len(lines) {
			continue
		}
		popupLine := style(sgrReverseVideo, fit(item, width))
		lines[row] = overlayPlain(lines[row], popupLine, x, width)
	}
	return lines
}

func (a *app) withSheetTabPopup(lines []string) []string {
	width := min(22, max(1, a.width))
	x := min(max(0, a.popupCol), max(0, a.width-width))
	y := min(max(1, a.tabRow()+1), max(1, a.height-len(sheetTabPopupMenu)-1))
	for i, item := range sheetTabPopupMenu {
		row := y + i
		if row < 0 || row >= len(lines) {
			continue
		}
		popupLine := style(sgrReverseVideo, fit(item, width))
		lines[row] = overlayPlain(lines[row], popupLine, x, width)
	}
	return lines
}

func (a *app) withConfirmUnsavedPopup(lines []string) []string {
	filename := a.currentFile
	if filename == "" {
		filename = "Untitled"
	}
	msg2 := " Press S to save and quit, Q to quit discarding changes, or ESC to go back"
	if a.pendingAction == actionRetrieve {
		msg2 = " Press S to save and continue with retrieving, Q to discarding changes, or ESC to go back"
	}
	content := []string{
		fmt.Sprintf(" The file %s has been changed.", filename),
		msg2,
	}
	a.renderSimpleBox(lines, content)
	return lines
}

func (a *app) withConfirmSaveOverwritePopup(lines []string) []string {
	filename := a.pendingSaveFile
	if filename == "" {
		filename = strings.TrimSpace(a.saveInput)
	}
	if filename == "" {
		filename = "(unknown)"
	}
	content := []string{
		fmt.Sprintf(" File %s exists.", filepath.Base(filename)),
		" Overwrite? Press Y/Enter to overwrite, Esc to change name",
	}
	a.renderSimpleBox(lines, content)
	return lines
}

func (a *app) withUndoHistoryPopup(lines []string) []string {
	stack := a.undoStack
	sel := a.undoSelect
	title := " Undo History "
	if a.historyShowRedo {
		stack = a.redoStack
		sel = a.redoSelect
		title = " Redo History "
	}
	var comments []string
	for i := len(stack) - 1; i >= 0; i-- {
		comments = append(comments, fmt.Sprintf("%d. %s", len(stack)-i, stack[i].comment))
	}
	hasItems := len(comments) > 0
	if !hasItems {
		comments = []string{"(no history)"}
	}
	boxW := max(44, a.width*7/10)
	if boxW > a.width-2 {
		boxW = a.width - 2
	}
	boxX := (a.width - boxW) / 2
	innerW := boxW - 2

	innerH := len(comments)
	boxH := innerH + 2
	if boxH > a.height {
		boxH = a.height
	}
	boxY := max(0, (a.height-boxH)/2)

	for row := boxY; row < boxY+boxH && row < len(lines); row++ {
		i := row - boxY
		var boxLine string
		switch {
		case i == 0:
			dashCount := innerW - displayWidth(title)
			if dashCount < 0 {
				dashCount = 0
			}
			boxLine = "┌" + title + strings.Repeat("─", dashCount) + "┐"
		case i == boxH-1:
			boxLine = "└" + strings.Repeat("─", innerW) + "┘"
		default:
			ci := i - 1
			var entry string
			if ci >= 0 && ci < len(comments) {
				entry = comments[ci]
			} else {
				entry = ""
			}
			if hasItems && ci == sel {
				boxLine = "│" + style(sgrReverseVideo, fitDisplay(entry, innerW)) + "│"
			} else {
				boxLine = "│" + fitDisplay(entry, innerW) + "│"
			}
		}
		lines[row] = overlayPlain(lines[row], boxLine, boxX, boxW)
	}
	drawBoxShadow(lines, boxX, boxY, boxW, boxH)
	return lines
}

// generalHelpPageRows mirrors the visible row count the scrollable popup
// renderer derives from the terminal height, so PgUp/PgDn move exactly one
// window of content.

func generalHelpPageRows(height int) int {
	rows := height * 8 / 10
	if inner := height - 6; rows > inner {
		rows = inner
	}
	return max(1, rows)
}

// renderScrollablePopup draws a titled box whose content scrolls when it
// exceeds the visible height, with a proportional scrollbar on the right
// (same mechanism as the file selector). scroll is clamped to range;
// scrolling keys are handled by the caller.

func (a *app) renderScrollablePopup(lines []string, title string, content []string, scroll *int) {
	// Hug the content: one leading space (already part of the lines) and
	// four trailing spaces after the longest sentence.
	longest := displayWidth(title)
	for _, c := range content {
		if w := runewidth.StringWidth(c); w > longest {
			longest = w
		}
	}
	boxW := longest + 4 + 2 // +4 right padding, +2 borders
	if boxW > a.width-2 {
		boxW = a.width - 2
	}
	boxX := (a.width - boxW) / 2
	innerW := boxW - 2

	maxInner := a.height * 8 / 10
	if inner := a.height - 6; maxInner > inner {
		maxInner = inner
	}
	if maxInner < 4 {
		maxInner = 4
	}
	innerVis := min(len(content), maxInner)
	if *scroll > len(content)-innerVis {
		*scroll = max(0, len(content)-innerVis)
	}
	if *scroll < 0 {
		*scroll = 0
	}

	bar := scrollbarGlyphs(len(content), innerVis, *scroll)
	hasBar := len(content) > innerVis
	textW := innerW
	if hasBar {
		textW = innerW - 1
	}

	boxH := innerVis + 2
	boxY := max(0, (a.height-boxH)/2)

	for row := boxY; row < boxY+boxH && row < len(lines); row++ {
		i := row - boxY
		var boxLine string
		switch {
		case i == 0:
			tw := displayWidth(title)
			dash := max(0, innerW-tw)
			boxLine = "┌" + style(sgrReverseVideo, title) + strings.Repeat("─", dash) + "┐"
		case i == boxH-1:
			boxLine = "└" + strings.Repeat("─", innerW) + "┘"
		default:
			ci := i - 1
			var entry string
			if idx := *scroll + ci; ci < innerVis && idx >= 0 && idx < len(content) {
				entry = content[idx]
			}
			inner := fitDisplay(entry, textW)
			if hasBar {
				g := "│"
				if ci < len(bar) {
					g = bar[ci]
				}
				inner += g
			}
			boxLine = "│" + inner + "│"
		}
		lines[row] = overlayPlain(lines[row], boxLine, boxX, boxW)
	}
	drawBoxShadow(lines, boxX, boxY, boxW, boxH)
}

func (a *app) renderSimpleBox(lines []string, content []string) {
	boxW := max(44, a.width*7/10)
	if boxW > a.width-2 {
		boxW = a.width - 2
	}
	boxX := (a.width - boxW) / 2
	innerW := boxW - 2
	innerH := len(content)
	boxH := innerH + 2
	if boxH > a.height {
		boxH = a.height
	}
	boxY := max(0, (a.height-boxH)/2)
	for i := range content {
		content[i] = fitDisplay(content[i], innerW)
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
}

// helpFunctions lists every supported formula with a short description;
// keep it in sync with the dispatch switch in internal/sheet/formula.go.
var helpFunctions = []struct{ name, desc string }{
	{"ABS", "Absolute value"},
	{"AND", "TRUE if all arguments are true"},
	{"AVERAGE", "Arithmetic mean of values"},
	{"AVERAGEIF", "Mean of cells matching a criteria"},
	{"COLUMN", "Column number of a reference"},
	{"COUNT", "Count of numeric values"},
	{"COUNTA", "Count of non-empty values"},
	{"COUNTIF", "Count of cells matching a criteria"},
	{"COUNTIFS", "Count matching multiple criteria pairs"},
	{"DATE", "Date serial from year, month, day"},
	{"DAY", "Day of month of a date serial"},
	{"DAYS", "Days between two dates"},
	{"EXACT", "Case-sensitive text comparison"},
	{"EXP", "e raised to a power"},
	{"FIND", "Position of text within text"},
	{"HLOOKUP", "Horizontal lookup in first table row"},
	{"IF", "Value depending on a condition"},
	{"IFS", "First matching condition's value"},
	{"INDEX", "Value at row/column inside a range"},
	{"INT", "Round down to integer"},
	{"IPMT", "Interest portion of a loan payment"},
	{"ISBLANK", "TRUE if the cell is empty"},
	{"ISERROR", "TRUE if the expression fails"},
	{"ISNUMBER", "TRUE if the value is a number"},
	{"ISTEXT", "TRUE if the value is text"},
	{"LEFT", "First characters of text"},
	{"LEN", "Length of text in characters"},
	{"LN", "Natural logarithm"},
	{"LOWER", "Text converted to lowercase"},
	{"MATCH", "Position of a value in a range"},
	{"MAX", "Largest numeric value"},
	{"MID", "Substring by start and length"},
	{"MIN", "Smallest numeric value"},
	{"MOD", "Remainder with sign of divisor"},
	{"MONTH", "Month of a date serial"},
	{"NOT", "Logical negation"},
	{"NOW", "Current date and time serial"},
	{"OR", "TRUE if any argument is true"},
	{"PI", "The number pi"},
	{"PMT", "Loan payment for fixed rate/terms"},
	{"POWER", "Number raised to a power"},
	{"PPMT", "Principal portion of a loan payment"},
	{"PRODUCT", "Product of values"},
	{"RIGHT", "Last characters of text"},
	{"ROUND", "Round to given decimals"},
	{"ROUNDUP", "Round away from zero"},
	{"ROW", "Row number of a reference"},
	{"ROWS", "Number of rows in a reference"},
	{"SQRT", "Square root"},
	{"SUBSTITUTE", "Replace occurrences of text"},
	{"SUBTOTAL", "Aggregate ignoring nested SUBTOTALs"},
	{"SUM", "Sum of values"},
	{"SUMIF", "Sum of cells matching a criteria"},
	{"SUMIFS", "Sum matching multiple criteria pairs"},
	{"TODAY", "Current date serial"},
	{"TRIM", "Strip and collapse spaces"},
	{"TRUE/FALSE", "Boolean literals"},
	{"UPPER", "Text converted to uppercase"},
	{"VLOOKUP", "Vertical lookup in first table column"},
	{"WEEKDAY", "Day of week of a date serial"},
	{"XLOOKUP", "Modern lookup with match modes"},
	{"YEAR", "Year of a date serial"},
	{"IFERROR", "Fallback when an expression errors"},
	{"IFNA", "Fallback on #N/A like IFERROR"},
	{"NA", "The #N/A error value"},
	{"INDIRECT", "Reference from text (A1 or R1C1, with sheet)"},
	{"LET", "Bind names to values for a calculation"},
	{"OFFSET", "Range offset by rows/cols with optional height/width"},
	{"TEXT", "Format a value as text using a number format"},
	{"VALUE", "Convert text that looks like a number to a number"},
	{"FILTER", "Filter array rows/cols by include booleans with optional if_empty"},
	{"TEXTJOIN", "Join text with delimiter, optionally ignoring empty"},
	{"SUMPRODUCT", "Sum of products of corresponding array elements"},
}

func (a *app) withGeneralHelpPopup(lines []string) []string {
	content := []string{
		"",
		" Use the up/down arrow keys or PgUp/Down to scroll this window.",
		"",
		" Keyboard shortcuts:",
		" F1               Open this general help",
		" /                Opens the menu",
		" Esc / Enter      Close the active popup",
		"",
		" Editing:",
		" F2 / Enter       Edit the active cell",
		"   Ctrl+A / Home  go to start of the cell content",
		"   Ctrl+E / End   go to end of the cell content",
		"   Esc            cancel editing",
		"   Enter          store the value, move down",
		"   Tab            store the value, move right",
		"",
		" Workbook:",
		" Ctrl+Z           Undo last action",
		" Ctrl+Y           Redo last undone action",
		" Ctrl+PgUp/PgDn   Switch sheets",
		" File->SaveAs     Auto-adds .xlsx extension",
		" File->Properties Show/Edit workbook properties",
		" Data->Sort       Sort selected range by column",
		" Search           Search, either only current sheet or globally",
		" Ctrl+F           Show last search results",
		"",
		" Workbook navigation:",
		" Mouse left cl.   Move to cell",
		" Arrow keys       Move between cells",
		" PgUp/PgDown      Scroll page up or down",
		" Home             Move to cell A1",
		" End              Move to cell in last column in current row",
		" Ctrl+End         Move to south-eastern corner cell of sheet",
		"",
		" Mouse, left click: click a cell to select it; click sheet tabs;",
		" click (+) to add a sheet.",
		" Mouse, right click: Available for sheets tabs, columns, rows and cells",
		"",
		" On the north east corner of the screen, there are labels.",
		" They represent the current state of the programme,",
		" eg READY, EDIT etc. Also FROZEN is shown as a secondary",
		" label when there are titles/frozen cells.", 
		"",
		" More help:",
		" Undo-redo specific help is available from the menu:",
		" choose Undo-redo, then Help.",
		"",
		" Supported formulas:",
	}
	sorted := append([]struct{ name, desc string }{}, helpFunctions...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].name < sorted[j].name })
	for _, fn := range sorted {
		line := fmt.Sprintf("   %-12s %s", fn.name, fn.desc)
		content = append(content, line)
	}
	a.renderScrollablePopup(lines, fmt.Sprintf(" tuisheet v%s - General Help ", buildinfo.AppVersion), content, &a.generalHelpScroll)
	return lines
}

func (a *app) withUndoRedoHelpPopup(lines []string) []string {
	content := []string{
		" Ctrl+Z  Undo last action (press repeatedly to go back further)",
		" Ctrl+Y  Redo last undone action (press repeatedly to go forward)",
		"",
		" In the Undo-redo menu:",
		"   Undo-history (U)  shows all saved undo states.",
		"                    Select a level with arrow keys,",
		"                    press Enter to jump directly to that state.",
		"                    The skipped states are pushed onto the redo",
		"                    stack so you can redo them one by one.",
		"   Redo-history (R)  same for redo states.",
		"   Help (H)         you are here.",
		"",
		" Each undo stores a snapshot of the entire workbook.",
		" The redo stack is filled automatically when you undo",
		" and cleared when you make a new edit.",
	}
	scroll := 0
	a.renderScrollablePopup(lines, " Undo-redo Help ", content, &scroll)
	return lines
}
