package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/mattn/go-runewidth"

	"tuisheet/internal/sheet"
)

func (a *app) selectSheetFromScreen(x, y int) bool {
	if y != a.tabRow() {
		return false
	}
	if a.addSheetAt(x) {
		return true
	}
	index, ok := a.sheetIndexAt(x)
	if !ok {
		return false
	}
	a.setActiveSheetNoReset(index)
	if a.viewMode != viewNormal {
		a.initViewSheets()
	}
	return true
}

func (a *app) gridPageRows() int {
	rows := max(1, a.height-a.gridTop()-2)
	if a.viewMode == viewNormal && a.sheet != nil && a.sheet.IsFrozen() {
		fr, _ := a.sheet.Freeze()
		rows -= fr
		if rows < 1 {
			rows = 1
		}
	}
	return rows
}

func (a *app) popupPageStep() int { return max(1, a.height/3) }

func (a *app) hitTest(x, y int) (sheetIdx int, row, col int, ok bool) {
	gridTop := a.gridTop()
	if y <= gridTop || x < 0 {
		return 0, 0, 0, false
	}

	rOff, cOff := a.rowOffset, a.colOffset
	sheetIdx = a.activeSheet

	switch a.viewMode {
	case viewHorizontal:
		avail := max(1, a.height-gridTop-2)
		perBand := max(1, avail/2)
		bandSize := 1 + perBand
		yInGrid := y - gridTop
		band := yInGrid / bandSize
		inBand := yInGrid % bandSize
		if band >= len(a.viewSheets) || inBand == 0 {
			return 0, 0, 0, false
		}
		sheetIdx = a.viewSheets[band]
		rOff, cOff = a.sheetOffset(sheetIdx)
		row = rOff + inBand - 1

	case viewVertical:
		perPane := max(1, (a.width-2*rowHeaderWidth)/(2*a.defaultColWidth))
		leftPaneWidth := rowHeaderWidth + perPane*a.defaultColWidth
		var rightPane bool
		if x < leftPaneWidth {
			sheetIdx = a.viewSheets[0]
			rightPane = false
		} else {
			sheetIdx = a.viewSheets[1]
			rightPane = true
		}
		rOff, cOff = a.sheetOffset(sheetIdx)

		if !rightPane {
			if x < rowHeaderWidth {
				row = rOff + y - gridTop - 1
				return sheetIdx, row, -1, true
			}
		} else {
			rightPaneStart := leftPaneWidth
			if x < rightPaneStart+rowHeaderWidth {
				row = rOff + y - gridTop - 1
				return sheetIdx, row, -1, true
			}
		}
		row = rOff + y - gridTop - 1
		if rightPane {
			col = a.screenXToCol(a.sheets[sheetIdx], cOff, x-leftPaneWidth+rowHeaderWidth)
		} else {
			col = a.screenXToCol(a.sheets[sheetIdx], cOff, x)
		}
		return sheetIdx, row, col, true

	case viewPerspective:
		n := len(a.viewSheets)
		avail := max(1, a.height-gridTop-2)
		perBand := max(1, (avail-n)/n)
		bandSize := 1 + perBand
		yInGrid := y - gridTop
		band := yInGrid / bandSize
		inBand := yInGrid % bandSize
		if band >= n || inBand == 0 {
			return 0, 0, 0, false
		}
		sheetIdx = a.viewSheets[band]
		rOff, cOff = a.sheetOffset(sheetIdx)
		row = rOff + inBand - 1

	default:
		row = rOff + y - gridTop - 1
	}

	if x < rowHeaderWidth {
		return sheetIdx, row, -1, true
	}
	col = a.screenXToCol(a.sheets[sheetIdx], cOff, x)
	return sheetIdx, row, col, true
}

func (a *app) perspectiveSheets() []int {
	total := len(a.sheetNames)
	show := make([]int, 0, 3)
	switch {
	case total <= 3:
		for i := 0; i < total; i++ {
			show = append(show, i)
		}
	case a.activeSheet == 0:
		show = []int{0, 1, 2}
	case a.activeSheet == total-1:
		show = []int{total - 3, total - 2, total - 1}
	default:
		show = []int{a.activeSheet - 1, a.activeSheet, a.activeSheet + 1}
	}
	return show
}

func (a *app) initViewSheets() {
	switch a.viewMode {
	case viewHorizontal:
		total := len(a.sheetNames)
		top := a.activeSheet - 1
		if top < 0 {
			top = total - 1
		}
		a.viewSheets = []int{top, a.activeSheet}
	case viewVertical:
		total := len(a.sheetNames)
		left := a.activeSheet - 1
		if left < 0 {
			left = total - 1
		}
		a.viewSheets = []int{left, a.activeSheet}
	case viewPerspective:
		a.viewSheets = a.perspectiveSheets()
	default:
		a.viewSheets = nil
	}
}

func (a *app) setActiveSheetNoReset(index int) {
	a.activateSheet(index)
}

// saveLiveState persists the live cursor/scroll under the active sheet's
// name. Maps are lazily initialised for app values built without seed().
func (a *app) saveLiveState() {
	if a.activeSheet < 0 || a.activeSheet >= len(a.sheetNames) {
		return
	}
	if a.cursors == nil {
		a.cursors = make(map[string]sheet.Coord)
	}
	if a.scrolls == nil {
		a.scrolls = make(map[string]sheet.Coord)
	}
	name := a.sheetNames[a.activeSheet]
	a.cursors[name] = a.active
	a.scrolls[name] = sheet.Coord{Row: a.rowOffset, Col: a.colOffset}
}

// restoreActiveState loads the cursor/scroll saved for the active sheet
// (zero when the sheet has no saved state) and refreshes the view.
func (a *app) restoreActiveState() {
	if a.activeSheet >= 0 && a.activeSheet < len(a.sheetNames) {
		name := a.sheetNames[a.activeSheet]
		if cur, ok := a.cursors[name]; ok {
			a.active = cur
		} else {
			a.active = sheet.Coord{}
		}
		if off, ok := a.scrolls[name]; ok {
			a.rowOffset, a.colOffset = off.Row, off.Col
		} else {
			a.rowOffset, a.colOffset = 0, 0
		}
	}
	a.gridCacheKey = ""
	a.gridCache = nil
	a.ensureSheetVisible()
	a.ensureVisible()
}

// activateSheet switches to another sheet, persisting the old sheet's
// cursor/scroll and restoring the new sheet's saved ones.
func (a *app) activateSheet(index int) {
	if index < 0 || index >= len(a.sheets) {
		return
	}
	if index == a.activeSheet && a.sheet == a.sheets[index] {
		return
	}
	a.saveLiveState()
	a.activeSheet = index
	a.sheet = a.sheets[index]
	a.restoreActiveState()
}

func (a *app) selectFromScreen(x, y int) {
	sheetIdx, row, col, ok := a.hitTest(x, y)
	if !ok || col < 0 {
		return
	}
	if sheetIdx != a.activeSheet {
		a.setActiveSheetNoReset(sheetIdx)
	}
	a.active = sheet.Coord{Row: row, Col: col}
	a.ensureVisible()
	a.closeOverlay()
}

func (a *app) switchSheet(delta int) {
	if len(a.sheets) == 0 {
		return
	}
	newIdx := (a.activeSheet + delta + len(a.sheets)) % len(a.sheets)
	a.setActiveSheetNoReset(newIdx)
	if a.viewMode != viewNormal {
		a.initViewSheets()
	}
	a.message = "Sheet " + a.sheetNames[newIdx]
}

func (a *app) setActiveSheet(index int) {
	if index < 0 || index >= len(a.sheets) {
		return
	}
	a.activeSheet = index
	a.sheet = a.sheets[index]
	a.active = sheet.Coord{}
	a.rowOffset = 0
	a.colOffset = 0
	a.ensureSheetVisible()
	a.mode = modeNormal
	a.message = "Sheet " + a.sheetNames[index]
}

func (a *app) ensureSheetVisible() {
	if len(a.sheetNames) <= 1 {
		a.tabOffset = 0
		return
	}
	a.tabOffset = max(0, min(a.tabOffset, len(a.sheetNames)-1))

	for tries := 0; tries < 3; tries++ {
		used := 4
		if a.tabOffset > 0 {
			used += 4
		}
		last := a.tabOffset - 1
		for i := a.tabOffset; i < len(a.sheetNames); i++ {
			tabWidth := 3 + 2 + runewidth.StringWidth(a.sheetNames[i])
			if used+tabWidth > a.width {
				break
			}
			used += tabWidth
			last = i
		}

		if a.activeSheet < a.tabOffset {
			a.tabOffset = a.activeSheet
			continue
		}
		if a.activeSheet > last {
			visibleCount := last - a.tabOffset + 1
			if visibleCount <= 0 {
				visibleCount = 1
			}
			newOffset := a.activeSheet - visibleCount + 1
			if newOffset < 0 {
				newOffset = 0
			}
			if newOffset == a.tabOffset {
				break
			}
			a.tabOffset = newOffset
			continue
		}
		break
	}
}

func (a *app) addSheet() {
	a.takeSnapshot("Add sheet")
	a.dirty = true
	name := a.nextSheetName()
	newSheet := sheet.New()
	a.sheets = append(a.sheets, newSheet)
	a.sheetNames = append(a.sheetNames, name)
	a.setActiveSheetNoReset(len(a.sheets) - 1)
	a.wireResolvers()
	if a.viewMode != viewNormal {
		a.initViewSheets()
	}
	a.message = "Added sheet " + name
}

func (a *app) nextSheetName() string {
	existing := make(map[string]bool, len(a.sheetNames))
	for _, name := range a.sheetNames {
		existing[name] = true
	}
	for i := 1; ; i++ {
		name := fmt.Sprintf("Φύλλο%d", i)
		if !existing[name] {
			return name
		}
	}
}

func (a *app) movePage(dr int) {
	if a.mode != modeNormal {
		return
	}
	rows := a.gridPageRows()
	a.active.Row += dr * rows
	if a.active.Row < 0 {
		a.active.Row = 0
	}
	a.ensureVisible()
}

func (a *app) move(dr, dc int) {
	if a.mode != modeNormal {
		return
	}
	newRow := a.active.Row + dr
	newCol := a.active.Col + dc
	if newRow < 0 {
		newRow = 0
	}
	if newCol < 0 {
		newCol = 0
	}
	if dr != 0 {
		step := 1
		if dr < 0 {
			step = -1
		}
		row := a.active.Row + step
		for row >= 0 && row < sheet.MaxRows && a.sheet.IsRowHidden(row) {
			row += step
		}
		if row >= 0 && row < sheet.MaxRows {
			newRow = row
		}
	}
	if dc != 0 {
		step := 1
		if dc < 0 {
			step = -1
		}
		col := a.active.Col + step
		for col >= 0 && col < sheet.MaxColumns && a.sheet.IsColHidden(col) {
			col += step
		}
		if col < 0 {
			col = 0
		}
		newCol = col
	}
	a.active.Row = newRow
	a.active.Col = newCol
	a.ensureVisible()
}

func (a *app) sheetOffset(sheetIdx int) (rowOff, colOff int) {
	if !a.syncScroll && a.viewMode != viewNormal {
		off, ok := a.viewOffsets[sheetIdx]
		if !ok {
			off = sheet.Coord{Row: a.rowOffset, Col: a.colOffset}
			a.viewOffsets[sheetIdx] = off
		}
		return off.Row, off.Col
	}
	return a.rowOffset, a.colOffset
}

func (a *app) colW(s *sheet.Sheet, col int) int {
	if w := s.ColWidth(col); w > 0 {
		return w
	}
	return a.defaultColWidth
}

func (a *app) visibleCols(s *sheet.Sheet, cOff int, availPx int) []int {
	var vc []int
	used := 0
	col := cOff
	for {
		w := a.colW(s, col)
		if !s.IsColHidden(col) {
			if len(vc) > 0 && used+w > availPx {
				break
			}
			vc = append(vc, col)
			used += w
		}
		col++
		if len(vc) > 0 && used >= availPx {
			break
		}
	}
	return vc
}

func (a *app) visibleRows(s *sheet.Sheet, rOff int, availRows int) []int {
	var vr []int
	row := rOff
	for len(vr) < availRows {
		if !s.IsRowHidden(row) {
			vr = append(vr, row)
		}
		row++
		if row >= sheet.MaxRows {
			break
		}
		if len(vr) >= availRows {
			break
		}
	}
	return vr
}

func (a *app) nextVisibleRow(s *sheet.Sheet, row int, step int) int {
	for {
		row += step
		if row < 0 {
			return 0
		}
		if row >= sheet.MaxRows {
			return sheet.MaxRows - 1
		}
		if !s.IsRowHidden(row) {
			return row
		}
	}
}

func (a *app) spanWidth(s *sheet.Sheet, vc []int, ci, span int) int {
	total := 0
	for i := 0; i < span && ci+i < len(vc); i++ {
		total += a.colW(s, vc[ci+i])
	}
	return max(1, total)
}

func (a *app) screenXToCol(s *sheet.Sheet, cOff int, x int) int {
	px := x - rowHeaderWidth
	if px < 0 {
		return cOff
	}
	col := cOff
	for px >= 0 {
		w := a.colW(s, col)
		if !s.IsColHidden(col) {
			if px < w {
				return col
			}
			px -= w
		}
		col++
	}
	return col - 1
}

func (a *app) ensureVisible() {
	var rows, px int
	switch a.viewMode {
	case viewHorizontal:
		rows = max(1, (a.height-a.gridTop()-2)/2)
		px = a.width - rowHeaderWidth
	case viewVertical:
		rows = max(1, a.height-a.gridTop()-2)
		px = (a.width - 2*rowHeaderWidth) / 2
	case viewPerspective:
		n := min(3, len(a.sheetNames))
		rows = max(1, (a.height-a.gridTop()-2-n)/n)
		px = a.width - rowHeaderWidth
	default:
		rows = max(1, a.height-a.gridTop()-2)
		px = a.width - rowHeaderWidth
	}
	// Freeze pane: keep offsets outside frozen area (only in normal view)
	fr, fc := 0, 0
	frozenW := 0
	if a.viewMode == viewNormal && a.sheet != nil && a.sheet.IsFrozen() {
		fr, fc = a.sheet.Freeze()
		// compute frozen width for px budget
		for c, cnt := 0, 0; cnt < fc && c < sheet.MaxColumns; c++ {
			if !a.sheet.IsColHidden(c) {
				frozenW += a.colW(a.sheet, c)
				cnt++
			}
		}
		if a.rowOffset < fr {
			a.rowOffset = fr
		}
		if a.colOffset < fc {
			a.colOffset = fc
		}
		for a.rowOffset < sheet.MaxRows && a.sheet.IsRowHidden(a.rowOffset) {
			a.rowOffset++
		}
	}
	effectiveRows := rows
	effectivePx := px
	if fr > 0 {
		effectiveRows = rows - fr
		if effectiveRows < 1 {
			effectiveRows = 1
		}
	}
	if fc > 0 {
		effectivePx = px - frozenW
		if effectivePx < 1 {
			effectivePx = 1
		}
	}
	fitCols := func(cOff int) int {
		w := px
		if fc > 0 && a.viewMode == viewNormal {
			w = effectivePx
		}
		return len(a.visibleCols(a.sheet, cOff, max(1, w)))
	}
	fitRows := func(rOff int) int {
		r := rows
		if fr > 0 && a.viewMode == viewNormal {
			r = effectiveRows
		}
		return len(a.visibleRows(a.sheet, rOff, r))
	}
	if !a.syncScroll && a.viewMode != viewNormal {
		off := a.viewOffsets[a.activeSheet]
		cols := fitCols(off.Col)
		vr := fitRows(off.Row)
		if a.active.Row < off.Row {
			off.Row = a.active.Row
		}
		if vr > 0 && a.active.Row >= off.Row+vr {
			off.Row = a.active.Row - vr + 1
		}
		// Ensure rowOffset points to visible row
		for off.Row < sheet.MaxRows && a.sheet.IsRowHidden(off.Row) {
			off.Row++
		}
		if a.active.Col < off.Col {
			off.Col = a.active.Col
		}
		if cols > 0 && a.active.Col >= off.Col+cols {
			off.Col = a.active.Col - cols + 1
		}
		a.viewOffsets[a.activeSheet] = off
		return
	}
	cols := fitCols(a.colOffset)
	vr := fitRows(a.rowOffset)
	// reuse fr/fc already computed above; recompute if needed
	if a.viewMode == viewNormal && a.sheet != nil && a.sheet.IsFrozen() {
		fr, fc = a.sheet.Freeze()
	} else {
		fr, fc = 0, 0
	}
	if fr == 0 || a.active.Row >= fr {
		if a.active.Row < a.rowOffset {
			a.rowOffset = a.active.Row
		}
		if vr > 0 && a.active.Row >= a.rowOffset+vr {
			a.rowOffset = a.active.Row - vr + 1
		}
	} else {
		// active inside frozen pane -> snap scroll back to top
		a.rowOffset = fr
	}
	for a.rowOffset < sheet.MaxRows && a.sheet.IsRowHidden(a.rowOffset) {
		a.rowOffset++
	}
	if fc == 0 || a.active.Col >= fc {
		if a.active.Col < a.colOffset {
			a.colOffset = a.active.Col
		}
		if cols > 0 && a.active.Col >= a.colOffset+cols {
			a.colOffset = a.active.Col - cols + 1
		}
	} else {
		a.colOffset = fc
	}
}

func (a *app) closeOverlay() {
	if a.mode != modeEdit && a.mode != modeOpenFile && a.mode != modeRenameSheet && a.mode != modeConfirmUnsaved {
		a.mode = modeNormal
	}
}

func (a *app) View() string {
	lines := make([]string, 0, a.height)
	lines = append(lines, a.statusLine())
	if a.isMenuVisible() {
		mLines := a.menuLines()
		if a.isPromptVisible() {
			if len(mLines) == 2 {
				mLines[1] = a.promptLine()
			} else {
				mLines = append(mLines, a.promptLine())
			}
		} else if a.mode != modeMenu {
			// Copy/Move: keep menu teal on 2nd line, 3rd line blank (status at bottom)
			if len(mLines) == 2 {
				mLines[1] = blankLine(a.width)
			}
		}
		lines = append(lines, mLines...)
	} else {
		lines = append(lines, blankLine(a.width), blankLine(a.width))
	}
	lines = append(lines, a.tabLine())
	switch a.viewMode {
	case viewPerspective:
		lines = append(lines, a.perspectiveLines()...)
	case viewHorizontal:
		lines = append(lines, a.horizontalLines()...)
	case viewVertical:
		lines = append(lines, a.verticalLines()...)
	default:
		lines = append(lines, a.gridLines()...)
	}
	lines = trimOrPad(lines, a.height, a.width)
	if a.height > 0 {
		lines[a.height-1] = a.messageLine()
	}
	if a.mode == modePopup {
		lines = a.withPopup(lines)
	}
	if a.mode == modeSheetTabPopup {
		lines = a.withSheetTabPopup(lines)
	}
	if a.mode == modeOpenFile || a.mode == modeSave {
		lines = a.withFileBrowserPopup(lines)
	}
	if a.mode == modeConfirmUnsaved {
		lines = a.withConfirmUnsavedPopup(lines)
	}
	if a.mode == modeConfirmSaveOverwrite {
		lines = a.withConfirmSaveOverwritePopup(lines)
	}
	if a.mode == modeUndoHistory {
		lines = a.withUndoHistoryPopup(lines)
	}
	if a.mode == modeHelp {
		lines = a.withUndoRedoHelpPopup(lines)
	}
	if a.mode == modeGeneralHelp {
		lines = a.withGeneralHelpPopup(lines)
	}
	if a.mode == modeSearchResults {
		lines = a.withSearchResultsPopup(lines)
	}
	if a.mode == modeProperties {
		lines = a.withPropertiesPopup(lines)
	}
	if a.mode == modePropertiesEdit {
		lines = a.withPropertiesEditPopup(lines)
	}
	if a.mode == modeSortOptions {
		lines = a.withSortPopup(lines)
	}
	if a.mode == modeSheetMove {
		lines = a.withSheetMovePopup(lines)
	}
	return strings.Join(lines, "\n")
}

func (a *app) statusLine() string {
	if a.mode == modeSearchInput && !a.isPromptVisible() {
		return a.inputStatusLine(fmt.Sprintf("Search (%s): ", a.searchScope), a.searchInput, a.searchCursor)
	}
	if a.mode == modeOpenFile {
		return a.inputStatusLine("Retrieve .xlsx: ", a.fileInput, a.fileInputCursor)
	}
	if a.mode == modeSave && !a.isPromptVisible() {
		return a.inputStatusLine("Save .xlsx: ", a.saveInput, a.saveCursor)
	}
	if a.mode == modeRenameSheet && !a.isPromptVisible() {
		return a.inputStatusLine("Rename sheet: ", a.renameInput, a.renameCursor)
	}

	var rightLabel string
	switch a.mode {
	case modeMenu:
		rightLabel = " MENU "
	case modeEdit:
		rightLabel = " EDIT "
	case modeNormal:
		rightLabel = " READY "
	case modeRangeSelect, modeCopySelect, modeCopyDest, modeMoveSelect, modeMoveDest, modeSortSelect:
		rightLabel = " RANGE "
	}
	frozenLabel := ""
	if a.sheet != nil && a.sheet.IsFrozen() {
		frozenLabel = style(sgrReverseVideo, " FROZEN ")
	}
	// Measure the label's display width (it carries SGR bytes) and pin it:
	// the left side never gets more than this budget.
	budget := a.width
	if frozenLabel != "" {
		budget -= displayWidth(frozenLabel)
		if rightLabel != "" {
			budget -= 1 // space between FROZEN and band
		}
	}
	if rightLabel != "" {
		budget -= displayWidth(style(sgrTealBand, rightLabel))
	}
	if budget < 0 {
		budget = 0
	}

	var left string
	if a.mode == modeEdit {
		left = a.editStatusLeft(budget)
	} else {
		prefix := fmt.Sprintf("%s:%s: ", a.sheetNames[a.activeSheet], a.active.String())
		avail := budget - displayWidth(prefix)
		if avail < 0 {
			avail = 0
		}
		content := a.sheet.Raw(a.active)
		if displayWidth(content) > avail {
			// Long cells truncate with an ellipsis; keep one column of
			// breathing space before the band.
			dots := 3
			if avail < dots+1 {
				dots = max(0, avail-1)
			}
			content = truncateDisplay(content, avail-dots-1) + strings.Repeat(".", dots)
		}
		left = prefix + content
	}

	line := fitDisplay(left, budget)
	if frozenLabel != "" {
		line += frozenLabel
		if rightLabel != "" {
			line += " "
		}
	}
	if rightLabel != "" {
		line += style(sgrTealBand, rightLabel)
	}
	return line
}

func (a *app) inputStatusLine(prefix, input string, cursor int) string {
	budget := a.width
	// prefix + input window must fit
	prefixW := displayWidth(prefix)
	textAvail := budget - prefixW
	if textAvail < 1 {
		textAvail = 1
	}
	runes := []rune(input)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	caretAtEnd := cursor == len(runes)
	const indW = 2
	rBoth := textAvail - 2*indW - caretInt(caretAtEnd)
	if rBoth < 1 {
		rBoth = 1
	}
	var lo int
	if caretAtEnd {
		lo = len(runes) - rBoth
	} else {
		lo = cursor - rBoth + 1
	}
	if lo < 0 {
		lo = 0
	}
	hasLeft := lo > 0
	runesCols := rBoth
	if !hasLeft {
		runesCols += indW
	}
	hi := min(len(runes), lo+runesCols)
	hasRight := hi < len(runes)
	if !hasRight {
		runesCols += indW
	}
	var b strings.Builder
	if hasLeft {
		b.WriteString("< ")
	}
	budgetContent := textAvail - caretInt(caretAtEnd)
	if hasLeft {
		budgetContent -= indW
	}
	if hasRight {
		budgetContent -= indW
	}
	used := 0
	for i := lo; i < hi; i++ {
		w := runewidth.StringWidth(string(runes[i]))
		if used+w > budgetContent {
			break
		}
		if i == cursor {
			b.WriteString(style(sgrReverseVideo, string(runes[i])))
		} else {
			b.WriteString(string(runes[i]))
		}
		used += w
	}
	if caretAtEnd && used < budgetContent {
		b.WriteString(style(sgrReverseVideo, " "))
		used++
	}
	if hasRight {
		b.WriteString(" >")
	}
	out := prefix + b.String()
	if w := displayWidth(out); w < budget {
		out += strings.Repeat(" ", budget-w)
	}
	return out
}

func (a *app) inputPromptLine(prefix, input string, cursor int) string {
	budget := a.width
	prefixW := displayWidth(prefix)
	textAvail := budget - prefixW
	if textAvail < 1 {
		textAvail = 1
	}
	runes := []rune(input)
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(runes) {
		cursor = len(runes)
	}
	caretAtEnd := cursor == len(runes)
	const indW = 2
	rBoth := textAvail - 2*indW - caretInt(caretAtEnd)
	if rBoth < 1 {
		rBoth = 1
	}
	var lo int
	if caretAtEnd {
		lo = len(runes) - rBoth
	} else {
		lo = cursor - rBoth + 1
	}
	if lo < 0 {
		lo = 0
	}
	hasLeft := lo > 0
	runesCols := rBoth
	if !hasLeft {
		runesCols += indW
	}
	hi := min(len(runes), lo+runesCols)
	hasRight := hi < len(runes)
	if !hasRight {
		runesCols += indW
	}
	var b strings.Builder
	if hasLeft {
		b.WriteString("< ")
	}
	budgetContent := textAvail - caretInt(caretAtEnd)
	if hasLeft {
		budgetContent -= indW
	}
	if hasRight {
		budgetContent -= indW
	}
	used := 0
	for i := lo; i < hi; i++ {
		w := runewidth.StringWidth(string(runes[i]))
		if used+w > budgetContent {
			break
		}
		if i == cursor {
			b.WriteString(style(sgrReverseVideo, string(runes[i])))
		} else {
			b.WriteString(style(sgrBold, string(runes[i])))
		}
		used += w
	}
	if caretAtEnd && used < budgetContent {
		b.WriteString(style(sgrReverseVideo, " "))
		used++
	}
	if hasRight {
		b.WriteString(" >")
	}
	out := style(sgrBold, prefix) + b.String()
	if w := displayWidth(out); w < budget {
		out += strings.Repeat(" ", budget-w)
	}
	return out
}

// editStatusLeft renders the editing buffer inside the given column budget:
// the window slides so the cursor is always visible, with "< " / " >"
// markers showing hidden content on either side. When the caret sits at
// the end of the buffer the window is a pure tail view, so a dangling ">"
// can never appear there.

func (a *app) editStatusLeft(budget int) string {
	prefix := fmt.Sprintf("%s:%s: ", a.sheetNames[a.activeSheet], a.active.String())
	textAvail := budget - runewidth.StringWidth(prefix) - 1 // one space before the band
	if textAvail < 1 {
		textAvail = 1
	}

	runes := []rune(a.edit)
	cur := a.editCursor
	if cur < 0 || cur > len(runes) {
		cur = utf8.RuneCountInString(a.edit)
	}
	caretAtEnd := cur == len(runes)
	const indW = 2 // "< " / " >" each cost two columns

	// Columns of buffer content visible when both markers are shown.
	rBoth := textAvail - 2*indW - caretInt(caretAtEnd)
	if rBoth < 1 {
		rBoth = 1
	}

	var lo int
	if caretAtEnd {
		// Tail view: the caret trails the last rune, so nothing can sit
		// to its right and ">" is structurally impossible.
		lo = len(runes) - rBoth
	} else {
		// Minimal offset that keeps the cursor rune inside the window.
		lo = cur - rBoth + 1
	}
	if lo < 0 {
		lo = 0
	}

	hasLeft := lo > 0
	runesCols := rBoth
	if !hasLeft {
		runesCols += indW // left marker freed; show more content instead
	}
	hi := min(len(runes), lo+runesCols)
	hasRight := hi < len(runes)
	if !hasRight {
		runesCols += indW // right marker freed
	}

	var b strings.Builder
	if hasLeft {
		b.WriteString("< ")
	}
	budgetContent := textAvail - caretInt(caretAtEnd)
	if hasLeft {
		budgetContent -= indW
	}
	if hasRight {
		budgetContent -= indW
	}
	used := 0
	for i := lo; i < hi; i++ {
		w := runewidth.StringWidth(string(runes[i]))
		if used+w > budgetContent {
			break
		}
		if i == cur {
			b.WriteString(style(sgrReverseVideo, string(runes[i])))
		} else {
			b.WriteString(string(runes[i]))
		}
		used += w
	}
	// Cursor at end of buffer: a reversed blank as the caret.
	if caretAtEnd && used < budgetContent {
		b.WriteString(style(sgrReverseVideo, " "))
		used++
	}
	if hasRight {
		b.WriteString(" >")
	}
	out := prefix + b.String()
	if w := displayWidth(out); w < budget {
		out += strings.Repeat(" ", budget-w)
	}
	return out
}

func (a *app) tabLine() string {
	hasLeft := a.tabOffset > 0

	used := 4
	if hasLeft {
		used += 4
	}

	count := 0
	for i := a.tabOffset; i < len(a.sheetNames); i++ {
		tabWidth := 3 + 2 + runewidth.StringWidth(a.sheetNames[i])
		if used+tabWidth > a.width {
			break
		}
		used += tabWidth
		count++
	}

	hasRight := a.tabOffset+count < len(a.sheetNames)

	// BUG: when the last rendered sheet name plus " | " reaches a.width,
	// hasRight stays true but " -> " won't fit — it overflows the terminal.
	// We previously dropped the last non-active tab to make room, but that
	// caused the indicator to disappear when it could not be shown without
	// hiding the active sheet. We kept the simpler always-show approach
	// to ensure users at least see " -> " for overflowed sheets, at the
	// cost of occasional right-edge overflow on the terminal.

	var line strings.Builder
	line.WriteString(" (+)")
	if hasLeft {
		line.WriteString(" <- ")
	}
	for i := 0; i < count; i++ {
		idx := a.tabOffset + i
		line.WriteString(" | ")
		text := " " + a.sheetNames[idx] + " "
		if idx == a.activeSheet {
			line.WriteString(style(sgrReverseVideo, text))
		} else {
			line.WriteString(text)
		}
	}
	if hasRight {
		line.WriteString(" -> ")
	}
	s := line.String()
	// Tab line is allowed to overflow by a few cols to keep the " -> "
	// indicator visible (original behaviour). Truncating it would hide the
	// indicator and break overflow tests, while grid lines must truncate
	// to avoid wrapping garble.
	if displayWidth(s) > a.width {
		return s
	}
	return s + strings.Repeat(" ", a.width-displayWidth(s))
}

func (a *app) gridLines() []string {
	var cacheKey string
	if a.viewMode == viewNormal {
		fr, fc := 0, 0
		if a.sheet != nil && a.sheet.IsFrozen() {
			fr, fc = a.sheet.Freeze()
		}
		cacheKey = fmt.Sprintf("%d:%d:%d:%d:%d:%d:%d:%d:%d:%d:%d:%d:%d:%v:%v:%d:%d", a.active.Row, a.active.Col, a.rowOffset, a.colOffset, a.width, a.height, a.activeSheet, fr, fc, a.sheet.LenCells(), a.sheet.LenFormulas(), a.sheet.LenHiddenCols(), a.sheet.LenHiddenRows(), a.mode, a.rangeSelecting, a.sheet.LenColWidths(), a.sheet.LenStyles())
		if cacheKey == a.gridCacheKey && a.gridCache != nil {
			return append([]string(nil), a.gridCache...)
		}
	}
	rows := max(1, a.height-a.gridTop()-2)
	// Freeze handling: split into frozen + scrollable
	if a.sheet != nil && a.sheet.IsFrozen() {
		fr, fc := a.sheet.Freeze()
		// frozen cols (first fc visible cols)
		frozenCols := []int{}
		for c := 0; len(frozenCols) < fc && c < sheet.MaxColumns; c++ {
			if !a.sheet.IsColHidden(c) {
				frozenCols = append(frozenCols, c)
			}
		}
		frozenRows := []int{}
		for r := 0; len(frozenRows) < fr && r < sheet.MaxRows; r++ {
			if !a.sheet.IsRowHidden(r) {
				frozenRows = append(frozenRows, r)
			}
		}
		frozenW := 0
		for _, c := range frozenCols {
			frozenW += a.colW(a.sheet, c)
		}
		availW := max(1, a.width-rowHeaderWidth)
		scrollW := availW - frozenW
		if scrollW < 1 {
			scrollW = 1
		}
		scrollCols := a.visibleCols(a.sheet, a.colOffset, scrollW)
		vc := append(append([]int{}, frozenCols...), scrollCols...)
		frozenH := len(frozenRows)
		scrollH := rows - frozenH
		if scrollH < 0 {
			scrollH = 0
		}
		scrollRows := a.visibleRows(a.sheet, a.rowOffset, max(1, scrollH))
		// if scrollRows empty due to offset, still show frozen
		if frozenH > rows {
			frozenRows = frozenRows[:rows]
			frozenH = rows
			scrollRows = nil
		}
		lines := make([]string, 0, rows+1)
		var header strings.Builder
		header.WriteString(style(sgrTealBand, strings.Repeat(" ", rowHeaderWidth)))
		for _, col := range vc {
			if col == a.active.Col {
				header.WriteString(style(sgrBlueBand, center(sheet.ColumnName(col), a.colW(a.sheet, col))))
			} else {
				header.WriteString(style(sgrTealBand, center(sheet.ColumnName(col), a.colW(a.sheet, col))))
			}
		}
		lines = append(lines, fitDisplay(header.String(), a.width))
		vrAll := append(append([]int{}, frozenRows...), scrollRows...)
		if len(vrAll) > rows {
			vrAll = vrAll[:rows]
		}
		for _, row := range vrAll {
			var line strings.Builder
			rowHeader := fmt.Sprintf("%*d ", rowHeaderWidth-1, row+1)
			if row == a.active.Row {
				line.WriteString(style(sgrBlueBand, rowHeader))
			} else {
				line.WriteString(style(sgrTealBand, rowHeader))
			}
			for ci, col := range vc {
				cell := sheet.Coord{Row: row, Col: col}
				if a.sheet.CoveredByMerge(cell) {
					line.WriteString(fit("", a.colW(a.sheet, col)))
					continue
				}
				width := a.colW(a.sheet, col)
				// merged width across vc (handle span within combined vc)
				if m, ok := a.sheet.MergeAt(cell); ok && m.Start == cell {
					span := 0
					for k := ci; k < len(vc); k++ {
						if vc[k] >= m.Start.Col && vc[k] <= m.End.Col {
							span++
						} else if vc[k] > m.End.Col {
							break
						}
					}
					if span > 1 {
						width = a.spanWidth(a.sheet, vc, ci, span)
					}
				}
				cellStyle := a.sheet.Style(cell)
				value := alignText(a.sheet.Display(cell), width, cellStyle.Align)
				if cell == a.active {
					line.WriteString(activeCell(value, cellStyle))
				} else if a.isInRangeSelection(cell) || a.isInCopyDestPreview(cell) {
					line.WriteString(style(sgrTealBand, value))
				} else {
					line.WriteString(styledCell(value, cellStyle))
				}
			}
			lines = append(lines, line.String())
		}
		if cacheKey != "" {
			a.gridCacheKey = cacheKey
			a.gridCache = append([]string(nil), lines...)
		}
		return lines
	}
	vc := a.visibleCols(a.sheet, a.colOffset, max(1, a.width-rowHeaderWidth))
	vr := a.visibleRows(a.sheet, a.rowOffset, rows)
	lines := make([]string, 0, rows+1)
	var header strings.Builder
	header.WriteString(style(sgrTealBand, strings.Repeat(" ", rowHeaderWidth)))
	for _, col := range vc {
		if col == a.active.Col {
			header.WriteString(style(sgrBlueBand, center(sheet.ColumnName(col), a.colW(a.sheet, col))))
		} else {
			header.WriteString(style(sgrTealBand, center(sheet.ColumnName(col), a.colW(a.sheet, col))))
		}
	}
	lines = append(lines, fitDisplay(header.String(), a.width))

	for r := 0; r < len(vr); r++ {
		row := vr[r]
		var line strings.Builder
		rowHeader := fmt.Sprintf("%*d ", rowHeaderWidth-1, row+1)
		if row == a.active.Row {
			line.WriteString(style(sgrBlueBand, rowHeader))
		} else {
			line.WriteString(style(sgrTealBand, rowHeader))
		}
		for ci := 0; ci < len(vc); ci++ {
			col := vc[ci]
			cell := sheet.Coord{Row: row, Col: col}
			if a.sheet.CoveredByMerge(cell) {
				if merge, ok := a.sheet.MergeAt(cell); ok && merge.Start.Row == row && merge.Start.Col >= a.colOffset {
					continue
				}
				line.WriteString(fit("", a.colW(a.sheet, col)))
				continue
			}
			width := a.cellDisplayWidth(cell, vc, ci)
			cellStyle := a.sheet.Style(cell)
			value := alignText(a.sheet.Display(cell), width, cellStyle.Align)
			if cell == a.active {
				line.WriteString(activeCell(value, cellStyle))
			} else if a.isInRangeSelection(cell) || a.isInCopyDestPreview(cell) {
				line.WriteString(style(sgrTealBand, value))
			} else {
				line.WriteString(styledCell(value, cellStyle))
			}
		}
		lines = append(lines, line.String())
	}
	if cacheKey != "" {
		a.gridCacheKey = cacheKey
		a.gridCache = append([]string(nil), lines...)
	}
	return lines
}

func (a *app) perspectiveLines() []string {
	show := a.viewSheets
	available := max(1, a.height-a.gridTop()-2)
	perBand := max(1, (available-len(show))/len(show))
	var lines []string
	for band, si := range show {
		s := a.sheets[si]
		rOff, cOff := a.sheetOffset(si)
		indent := (len(show) - 1 - band) * 2
		vc := a.visibleCols(s, cOff, max(1, a.width-rowHeaderWidth-indent))
		pad := strings.Repeat(" ", indent)
		var hdr strings.Builder
		hdr.WriteString(pad)
		label := fit(a.sheetNames[si], rowHeaderWidth)
		hdr.WriteString(style(sgrTealBand, label))
		for _, col := range vc {
			hdr.WriteString(style(sgrTealBand, center(sheet.ColumnName(col), a.colW(s, col))))
		}
		lines = append(lines, fitDisplay(hdr.String(), a.width))
		for r := 0; r < perBand; r++ {
			row := rOff + r
			var line strings.Builder
			line.WriteString(pad)
			rowHdr := fmt.Sprintf("%*d ", rowHeaderWidth-1, row+1)
			if si == a.activeSheet && row == a.active.Row {
				line.WriteString(style(sgrBlueBand, rowHdr))
			} else {
				line.WriteString(style(sgrTealBand, rowHdr))
			}
			for ci, col := range vc {
				cell := sheet.Coord{Row: row, Col: col}
				if s.CoveredByMerge(cell) {
					if merge, ok := s.MergeAt(cell); ok && merge.Start.Row == row && merge.Start.Col >= cOff {
						continue
					}
					line.WriteString(fit("", a.colW(s, col)))
					continue
				}
				merge, ok := s.MergeAt(cell)
				width := a.colW(s, col)
				if ok && merge.Start == cell && merge.Start.Row == cell.Row {
					span := min(merge.ColSpan(), len(vc)-ci)
					width = a.spanWidth(s, vc, ci, span)
				}
				cs := s.Style(cell)
				val := alignText(s.Display(cell), width, cs.Align)
				if cell == a.active && si == a.activeSheet {
					line.WriteString(activeCell(val, cs))
				} else if si == a.activeSheet && (a.isInRangeSelection(cell) || a.isInCopyDestPreview(cell)) {
					line.WriteString(style(sgrTealBand, val))
				} else {
					line.WriteString(styledCell(val, cs))
				}
			}
			lines = append(lines, fitDisplay(line.String(), a.width))
		}
	}
	return lines
}

func (a *app) horizontalLines() []string {
	available := max(1, a.height-a.gridTop()-2)
	perBand := max(1, available/2)
	var lines []string
	for pass, si := range a.viewSheets {
		if pass >= 2 {
			break
		}
		s := a.sheets[si]
		rOff, cOff := a.sheetOffset(si)
		vc := a.visibleCols(s, cOff, max(1, a.width-rowHeaderWidth))
		var hdr strings.Builder
		label := fit(a.sheetNames[si], rowHeaderWidth)
		hdr.WriteString(style(sgrTealBand, label))
		for _, col := range vc {
			hdr.WriteString(style(sgrTealBand, center(sheet.ColumnName(col), a.colW(s, col))))
		}
		lines = append(lines, fitDisplay(hdr.String(), a.width))
		for r := 0; r < perBand; r++ {
			row := rOff + r
			var line strings.Builder
			rowHdr := fmt.Sprintf("%*d ", rowHeaderWidth-1, row+1)
			if si == a.activeSheet && row == a.active.Row {
				line.WriteString(style(sgrBlueBand, rowHdr))
			} else {
				line.WriteString(style(sgrTealBand, rowHdr))
			}
			for ci, col := range vc {
				cell := sheet.Coord{Row: row, Col: col}
				if s.CoveredByMerge(cell) {
					if merge, ok := s.MergeAt(cell); ok && merge.Start.Row == row && merge.Start.Col >= cOff {
						continue
					}
					line.WriteString(fit("", a.colW(s, col)))
					continue
				}
				merge, ok := s.MergeAt(cell)
				width := a.colW(s, col)
				if ok && merge.Start == cell && merge.Start.Row == cell.Row {
					span := min(merge.ColSpan(), len(vc)-ci)
					width = a.spanWidth(s, vc, ci, span)
				}
				cs := s.Style(cell)
				val := alignText(s.Display(cell), width, cs.Align)
				if cell == a.active && si == a.activeSheet {
					line.WriteString(activeCell(val, cs))
				} else if si == a.activeSheet && (a.isInRangeSelection(cell) || a.isInCopyDestPreview(cell)) {
					line.WriteString(style(sgrTealBand, val))
				} else {
					line.WriteString(styledCell(val, cs))
				}
			}
			lines = append(lines, line.String())
		}
	}
	return lines
}

func (a *app) verticalLines() []string {
	available := max(1, a.height-a.gridTop()-2)
	panePx := max(1, (a.width-2*rowHeaderWidth)/2)
	var lines []string

	var hdr strings.Builder
	for pass, si := range a.viewSheets {
		if pass >= 2 {
			break
		}
		s := a.sheets[si]
		_, cOff := a.sheetOffset(si)
		vc := a.visibleCols(s, cOff, panePx)
		label := fit(a.sheetNames[si], rowHeaderWidth)
		hdr.WriteString(style(sgrTealBand, label))
		for _, col := range vc {
			hdr.WriteString(style(sgrTealBand, center(sheet.ColumnName(col), a.colW(s, col))))
		}
	}
	lines = append(lines, fitDisplay(hdr.String(), a.width))

	for r := 0; r < available; r++ {
		var line strings.Builder
		for pass, si := range a.viewSheets {
			if pass >= 2 {
				break
			}
			s := a.sheets[si]
			rOff, cOff := a.sheetOffset(si)
			vc := a.visibleCols(s, cOff, panePx)
			row := rOff + r
			rowHdr := fmt.Sprintf("%*d ", rowHeaderWidth-1, row+1)
			if si == a.activeSheet && row == a.active.Row {
				line.WriteString(style(sgrBlueBand, rowHdr))
			} else {
				line.WriteString(style(sgrTealBand, rowHdr))
			}
			for ci, col := range vc {
				cell := sheet.Coord{Row: row, Col: col}
				if s.CoveredByMerge(cell) {
					if merge, ok := s.MergeAt(cell); ok && merge.Start.Row == row && merge.Start.Col >= cOff {
						continue
					}
					line.WriteString(fit("", a.colW(s, col)))
					continue
				}
				merge, ok := s.MergeAt(cell)
				width := a.colW(s, col)
				if ok && merge.Start == cell && merge.Start.Row == cell.Row {
					span := min(merge.ColSpan(), len(vc)-ci)
					width = a.spanWidth(s, vc, ci, span)
				}
				cs := s.Style(cell)
				val := alignText(s.Display(cell), width, cs.Align)
				if cell == a.active && si == a.activeSheet {
					line.WriteString(activeCell(val, cs))
				} else if si == a.activeSheet && (a.isInRangeSelection(cell) || a.isInCopyDestPreview(cell)) {
					line.WriteString(style(sgrTealBand, val))
				} else {
					line.WriteString(styledCell(val, cs))
				}
			}
		}
		lines = append(lines, fitDisplay(line.String(), a.width))
	}
	return lines
}

func (a *app) cellDisplayWidth(cell sheet.Coord, vc []int, ci int) int {
	merge, ok := a.sheet.MergeAt(cell)
	if !ok || merge.Start != cell || merge.Start.Row != cell.Row {
		return a.colW(a.sheet, cell.Col)
	}
	span := min(merge.ColSpan(), len(vc)-ci)
	return a.spanWidth(a.sheet, vc, ci, span)
}

func (a *app) messageLine() string {
	msg := a.message
	if a.mode == modeEdit {
		msg = "Editing " + a.active.String() + ": Enter stores  Esc cancels"
	}
	if a.mode == modeOpenFile {
		if strings.HasPrefix(msg, "Open failed:") {
		} else {
			msg = "Select xlsx file from selector or type xlsx file name with or without path (current dir). Enter retrieves, Esc cancels"
		}
	}
	// For menu-originated prompts (ColWidth, Range, etc.) the prompt is now on 3rd line (promptLine, bold)
	// Bottom line stays status only, so do not override msg for those modes here.
	// Keep rename and other popups as status as well.
	if a.mode == modeConfirmDelete {
		msg = "Are you sure you want to delete " + a.sheetNames[a.sheetTabPopupSheetIndex] + "? (y/n)"
	}
	if a.mode == modeConfirmUnsaved {
		msg = "S to save, Esc to discard"
	}
	if a.mode == modeSave {
		msg = "path where xlsx file will be saved. Extension will be auto-appended"
		fullPath := a.currentFile
		if fullPath == "" {
			if cwd, err := os.Getwd(); err == nil {
				fullPath = cwd
			} else {
				fullPath = "."
			}
		}
		if abs, err := filepath.Abs(fullPath); err == nil {
			fullPath = abs
		}
		fileTag := style(sgrReverseVideo, " "+fullPath+" ")
		return fit(fileTag+" "+style(sgrBold, msg), a.width)
	}
	if a.currentFile != "" {
		fn := filepath.Base(a.currentFile)
		fileTag := style(sgrReverseVideo, " "+fn+" ")
		return fit(fileTag+" "+style(sgrBold, msg), a.width)
	}
	return fit(style(sgrBold, msg), a.width)
}

func (a *app) gridTop() int {
	return 4
}

// scrollbarGlyphs renders a proportional, non-interactive scrollbar as
// one glyph per visible row: a thin track with a solid thumb sized and
// positioned according to the scroll offset. Reusable by any popup that
// shows a windowed list. When everything fits, it returns blank columns.

func (a *app) tabRow() int {
	return 3
}

func alignText(s string, width int, align sheet.Alignment) string {
	switch align {
	case sheet.AlignRight:
		return fitRight(s, width)
	case sheet.AlignCenter:
		return center(s, width)
	default:
		return fit(s, width)
	}
}

// padCells pads s to exactly width columns, truncating via truncateDisplay
// on overflow. ANSI escape sequences are counted only when widthFn is
// displayWidth; leftPad selects right alignment.
func padCells(s string, width int, leftPad bool, widthFn func(string) int) string {
	if width <= 0 {
		return ""
	}
	visible := widthFn(s)
	if visible > width {
		return truncateDisplay(s, width)
	}
	pad := strings.Repeat(" ", width-visible)
	if leftPad {
		return pad + s
	}
	return s + pad
}

func fitRight(s string, width int) string {
	return padCells(s, width, true, runewidth.StringWidth)
}

func fit(s string, width int) string {
	return padCells(s, width, false, runewidth.StringWidth)
}

func center(s string, width int) string {
	visible := runewidth.StringWidth(s)
	if visible >= width {
		return fit(s, width)
	}
	left := (width - visible) / 2
	return strings.Repeat(" ", left) + fit(s, width-left)
}

func style(sgr, text string) string {
	if text == "" {
		return ""
	}
	return sgr + text + sgrReset
}

func blankLine(width int) string {
	return fit("", width)
}

func activeCell(value string, cellStyle sheet.Style) string {
	return applySGR(value, append(cellSGR(cellStyle), sgrReverseVideo)...)
}

func styledCell(value string, cellStyle sheet.Style) string {
	return applySGR(value, cellSGR(cellStyle)...)
}

func cellSGR(cellStyle sheet.Style) []string {
	var sgr []string
	if cellStyle.Bold {
		sgr = append(sgr, sgrBold)
	}
	if cellStyle.Italic {
		sgr = append(sgr, sgrItalic)
	}
	if cellStyle.Underline {
		sgr = append(sgr, sgrUnderline)
	}
	if cellStyle.Strikethrough {
		sgr = append(sgr, sgrStrikethrough)
	}
	return sgr
}

func applySGR(text string, sgr ...string) string {
	if text == "" || len(sgr) == 0 {
		return text
	}
	var out strings.Builder
	for _, code := range sgr {
		out.WriteString(code)
	}
	out.WriteString(text)
	out.WriteString(sgrReset)
	return out.String()
}

func trimOrPad(lines []string, height, width int) []string {
	if height <= 0 {
		return nil
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i := range lines {
		lines[i] = fitDisplay(lines[i], width)
	}
	return lines
}

func overlayPlain(base, overlay string, col, width int) string {
	if displayWidth(base) < col+width {
		base = fitDisplay(base, col+width)
	}
	start := displayByteOffset(base, col)
	end := displayByteOffset(base, col+width)
	return base[:start] + "\x1b[0m" + overlay + base[end:]
}

func displayByteOffset(s string, col int) int {
	inEscape := false
	curCol, i := 0, 0
	for i < len(s) && curCol < col {
		if inEscape {
			if s[i] >= '@' && s[i] <= '~' {
				inEscape = false
			}
			i++
			continue
		}
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			inEscape = true
			i += 2
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			curCol++
		} else {
			curCol += runewidth.RuneWidth(r)
		}
		i += size
	}
	return i
}

func fitDisplay(s string, width int) string {
	return padCells(s, width, false, displayWidth)
}

func displayWidth(s string) int {
	width := 0
	inEscape := false
	for i := 0; i < len(s); i++ {
		if inEscape {
			if s[i] >= '@' && s[i] <= '~' {
				inEscape = false
			}
			continue
		}
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			inEscape = true
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			width++
		} else {
			width += runewidth.RuneWidth(r)
		}
		i += size - 1
	}
	return width
}

func truncateDisplay(s string, width int) string {
	var out strings.Builder
	used := 0
	inEscape := false
	for i := 0; i < len(s); {
		if inEscape {
			// Copy escape sequence verbatim (not counted toward width)
			out.WriteByte(s[i])
			if s[i] >= '@' && s[i] <= '~' {
				inEscape = false
			}
			i++
			continue
		}
		if s[i] == '\x1b' && i+1 < len(s) && s[i+1] == '[' {
			inEscape = true
			out.WriteByte(s[i])
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		rw := runewidth.RuneWidth(r)
		if used+rw > width {
			break
		}
		out.WriteString(s[i : i+size])
		used += rw
		i += size
	}
	if used < width {
		out.WriteString(strings.Repeat(" ", width-used))
	}
	return out.String()
}

func (a *app) popupBoxMetrics(pct, minW, innerH int) (boxX, boxY, boxW, innerW, boxH int) {
	boxW = max(minW, a.width*pct/10)
	if boxW > a.width-2 {
		boxW = a.width - 2
	}
	boxX = (a.width - boxW) / 2
	innerW = boxW - 2
	boxH = innerH + 2
	if boxH > a.height {
		boxH = a.height
	}
	boxY = max(0, (a.height-boxH)/2)
	return
}

func drawBoxShadow(lines []string, boxX, boxY, boxW, boxH int) {
	for row := boxY + 1; row < boxY+boxH && row < len(lines); row++ {
		lines[row] = overlayPlain(lines[row], style(sgrReverseVideo, " "), boxX+boxW, 1)
	}
	southRow := boxY + boxH
	if southRow < len(lines) {
		shadow := style(sgrReverseVideo, strings.Repeat(" ", boxW))
		lines[southRow] = overlayPlain(lines[southRow], shadow, boxX+1, boxW)
	}
}
