package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"tuisheet/internal/sheet"
)

var menuTree = []menuEntry{
	{label: "Worksheet", items: []menuEntry{
		{label: "Column", items: []menuEntry{
			{label: "Set-width"},
			{label: "Reset-width"},
			{label: "set-Global-width"},
			{label: "reset-globaL-width"},
			{label: "Insert", items: []menuEntry{
				{label: "Left"},
				{label: "Right"},
			}},
			{label: "Delete"},
			{label: "Hide"},
			{label: "displaY"},
		}},
		{label: "Row", items: []menuEntry{
			{label: "Insert", items: []menuEntry{
				{label: "Above"},
				{label: "Below"},
			}},
			{label: "Delete"},
			{label: "Hide"},
			{label: "displaY"},
		}},
		{label: "Sheet", items: []menuEntry{
			{label: "New"},
			{label: "Rename"},
			{label: "Move"},
			{label: "Delete"},
		}},
		{label: "Titles-freeze"},
		{label: "Window", items: []menuEntry{
			{label: "Horizontal"},
			{label: "Vertical"},
			{label: "Sync-unsync"},
			{label: "Clear"},
			{label: "Perspective"},
		}},
	}},
	{label: "Range", items: []menuEntry{
		{label: "Format", items: []menuEntry{
			{label: "General"},
			{label: "Number"},
			{label: "Percentage"},
			{label: "Currency"},
			{label: "Date"},
			{label: "Text"},
			{label: "Bold"},
			{label: "Italic"},
			{label: "Underline"},
			{label: "Strikethrough"},
		}},
		{label: "Erase", items: []menuEntry{
			{label: "All"},
			{label: "Content"},
			{label: "Style"},
		}},
		{label: "Alignment", items: []menuEntry{
			{label: "Left"},
			{label: "Right"},
			{label: "Centre"},
		}},
	}},
	{label: "Copy", desc: "Copy a cell or range of cells"},
	{label: "Move", desc: "Move a cell or range of cells"},
	{label: "File", items: []menuEntry{
		{label: "Retrieve-load"},
		{label: "Save"},
		{label: "saveAs"},
		{label: "Properties"},
	}},
	{label: "Undo-redo", items: []menuEntry{
		{label: "Undo-history"},
		{label: "Redo-history"},
		{label: "Help"},
	}},
	{label: "Data", items: []menuEntry{
		{label: "Table"},
		{label: "Sort"},
	}},
	{label: "Search", items: []menuEntry{
		{label: "Global", desc: "Search whole file"},
		{label: "Sheet", desc: "Search current sheet"},
	}},
	{label: "Quit", desc: "End program"},
}

var cellPopupMenu = []string{"Delete cells up", "Delete cells left"}

var rowPopupMenu = []string{"Insert row above", "Insert row below", "Hide row", "Delete row"}

var colPopupMenu = []string{"Set-width", "Reset-width", "Insert column left", "Insert column right", "Hide column", "Delete column"}

var sheetTabPopupMenu = []string{"Rename sheet", "Delete sheet"}

func (a *app) isMenuVisible() bool {
	switch a.mode {
	case modeMenu, modeColWidth, modeColHide, modeColDisplay, modeRowHide, modeRowDisplay, modeRowDelete, modeRangeSelect, modeCopySelect, modeCopyDest, modeMoveSelect, modeMoveDest, modeSortSelect, modeSearchInput, modeSave, modeRenameSheet, modeSheetMove:
		return true
	case modeSortOptions:
		return true // popup but keep menu behind
	default:
		return false
	}
}

func (a *app) isPromptVisible() bool {
	switch a.mode {
	case modeCopySelect, modeCopyDest, modeMoveSelect, modeMoveDest:
		return false // status only per spec
	case modeColWidth, modeColHide, modeColDisplay, modeRowHide, modeRowDisplay, modeRowDelete, modeRangeSelect, modeSortSelect, modeSearchInput, modeSave, modeRenameSheet:
		return true
	default:
		return false
	}
}

func (a *app) promptLine() string {
	if !a.isPromptVisible() {
		return blankLine(a.width)
	}
	switch a.mode {
	case modeColWidth:
		return a.inputPromptLine("Column width (1-99): ", a.colWidthInput, utf8.RuneCountInString(a.colWidthInput))
	case modeColHide:
		return a.inputPromptLine("Column to hide: ", a.colHideInput, utf8.RuneCountInString(a.colHideInput))
	case modeColDisplay:
		return a.inputPromptLine("Column to display: ", a.colHideInput, utf8.RuneCountInString(a.colHideInput))
	case modeRowHide:
		return a.inputPromptLine("Row to hide (e.g. 2 or 2-100): ", a.rowHideInput, utf8.RuneCountInString(a.rowHideInput))
	case modeRowDisplay:
		return a.inputPromptLine("Row to display (e.g. 2 or 2-100): ", a.rowHideInput, utf8.RuneCountInString(a.rowHideInput))
	case modeRowDelete:
		return a.inputPromptLine("Row to delete (e.g. 2 or 2-100): ", a.rowHideInput, utf8.RuneCountInString(a.rowHideInput))
	case modeSearchInput:
		return a.inputPromptLine(fmt.Sprintf("Search (%s): ", a.searchScope), a.searchInput, a.searchCursor)
	case modeSave:
		return a.inputPromptLine("Save .xlsx: ", a.saveInput, a.saveCursor)
	case modeRenameSheet:
		return a.inputPromptLine("Rename sheet: ", a.renameInput, a.renameCursor)
	default:
		return fitDisplay(style(sgrBold, a.prompt), a.width)
	}
}

func (a *app) executeMenuLeaf() tea.Cmd {
	if len(a.menuStack) == 0 {
		return nil
	}

	// Build the path from root to current selection
	var parts []string
	for _, level := range a.menuStack {
		parts = append(parts, level.items[level.sel].label)
	}
	path := strings.Join(parts, "/")

	a.mode = modeNormal
	switch path {
	case "File/Retrieve-load":
		if a.dirty {
			a.mode = modeConfirmUnsaved
			a.pendingAction = actionRetrieve
			return nil
		}
		a.enterOpenFileMode()
	case "File/Save":
		a.enterSaveMode()
	case "File/saveAs":
		a.enterSaveAsMode()
	case "Undo-redo/Help":
		a.mode = modeHelp
	case "Undo-redo/Undo-history":
		a.undoSelect = 0
		a.historyShowRedo = false
		a.mode = modeUndoHistory
	case "Undo-redo/Redo-history":
		a.redoSelect = 0
		a.historyShowRedo = true
		a.mode = modeUndoHistory
	case "Worksheet/Window/Perspective":
		a.viewMode = viewPerspective
		a.initViewSheets()
	case "Worksheet/Window/Horizontal":
		a.viewMode = viewHorizontal
		a.initViewSheets()
	case "Worksheet/Window/Vertical":
		a.viewMode = viewVertical
		a.initViewSheets()
	case "Worksheet/Window/Sync-unsync":
		a.syncScroll = !a.syncScroll
		if a.syncScroll {
			a.message = "Scrolling synced"
		} else {
			a.message = "Scrolling unsynced"
		}
	case "Worksheet/Window/Clear":
		a.viewMode = viewNormal
	case "Worksheet/Column/Set-width":
		a.colWidthGlobal = false
		a.colWidthInput = strconv.Itoa(a.defaultColWidth)
		a.mode = modeColWidth
	case "Worksheet/Column/Reset-width":
		a.sheet.ResetColWidth(a.active.Col)
		a.dirty = true
		a.message = "Column " + sheet.ColumnName(a.active.Col) + " width reset to default"
	case "Worksheet/Column/set-Global-width":
		a.colWidthGlobal = true
		a.colWidthInput = strconv.Itoa(a.defaultColWidth)
		a.mode = modeColWidth
	case "Worksheet/Column/reset-globaL-width":
		a.takeSnapshot("Reset all columns width")
		for c := range a.sheet.GetAllColWidths() {
			a.sheet.ResetColWidth(c)
		}
		a.defaultColWidth = 11
		a.dirty = true
		a.message = "All columns width reset to default"
	case "Worksheet/Column/Insert/Left":
		a.takeSnapshot("Insert column left")
		a.sheet.InsertColumn(a.active.Col)
		a.dirty = true
		a.message = "Inserted column left of " + sheet.ColumnName(a.active.Col)
	case "Worksheet/Column/Insert/Right":
		a.takeSnapshot("Insert column right")
		a.sheet.InsertColumn(a.active.Col + 1)
		a.dirty = true
		a.message = "Inserted column right of " + sheet.ColumnName(a.active.Col)
	case "Worksheet/Column/Delete":
		a.takeSnapshot("Delete column " + sheet.ColumnName(a.active.Col))
		a.sheet.DeleteColumn(a.active.Col)
		a.dirty = true
		a.message = "Deleted column " + sheet.ColumnName(a.active.Col)
	case "Worksheet/Column/Hide":
		a.colHideInput = ""
		a.mode = modeColHide
	case "Worksheet/Column/displaY":
		a.colHideInput = ""
		a.mode = modeColDisplay
	case "Worksheet/Row/Insert/Above":
		a.takeSnapshot("Insert row above")
		a.sheet.InsertRow(a.active.Row)
		a.dirty = true
		a.message = fmt.Sprintf("Inserted row above %d", a.active.Row+1)
	case "Worksheet/Row/Insert/Below":
		a.takeSnapshot("Insert row below")
		a.sheet.InsertRow(a.active.Row + 1)
		a.dirty = true
		a.message = fmt.Sprintf("Inserted row below %d", a.active.Row+1)
	case "Worksheet/Row/Delete":
		a.rowHideInput = ""
		a.mode = modeRowDelete
	case "Worksheet/Row/Hide":
		a.rowHideInput = ""
		a.mode = modeRowHide
	case "Worksheet/Row/displaY":
		a.rowHideInput = ""
		a.mode = modeRowDisplay
	case "Worksheet/Sheet/New":
		a.addSheet()
		a.dirty = true
	case "Worksheet/Sheet/Rename":
		a.mode = modeRenameSheet
		a.renameInput = a.sheetNames[a.activeSheet]
		a.renameCursor = utf8.RuneCountInString(a.renameInput)
		a.message = "Rename sheet: "
	case "Worksheet/Sheet/Move":
		if len(a.sheets) <= 1 {
			a.message = "Cannot move single sheet"
			break
		}
		a.sheetMoveIdx = a.activeSheet
		a.sheetMoveOrigNames = append([]string(nil), a.sheetNames...)
		a.sheetMoveOrigSheets = append([]*sheet.Sheet(nil), a.sheets...)
		a.sheetMoveOrigActive = a.activeSheet
		a.mode = modeSheetMove
		a.message = "Move sheet: Up/Down to reorder, Enter to confirm, Esc to cancel"
	case "Worksheet/Sheet/Delete":
		if len(a.sheets) <= 1 {
			a.message = "Cannot delete the last sheet"
			break
		}
		a.sheetTabPopupSheetIndex = a.activeSheet
		a.mode = modeConfirmDelete
	case "Worksheet/Titles-freeze":
		if a.viewMode != viewNormal {
			a.viewMode = viewNormal
			a.viewSheets = nil
		}
		if a.sheet.IsFrozen() {
			a.takeSnapshot("Unfreeze titles")
			a.sheet.SetFreeze(0, 0)
			a.dirty = true
			a.message = "Titles unfrozen"
		} else {
			if a.active.Row == 0 && a.active.Col == 0 {
				a.message = "Titles-freeze: no freeze at A1 — move cursor to freeze rows/columns"
			} else {
				a.takeSnapshot("Freeze titles")
				a.sheet.SetFreeze(a.active.Row, a.active.Col)
				a.dirty = true
				a.message = "Titles-freeze selected. If titles are active (sheet frozen), press again to disable. Viewable in default view only"
			}
			// auto-clear handled above
			if a.sheet.IsFrozen() {
				a.ensureVisible()
			}
		}
	case "Range/Format/General", "Range/Format/Number", "Range/Format/Percentage", "Range/Format/Currency", "Range/Format/Date", "Range/Format/Text",
		"Range/Format/Bold", "Range/Format/Italic", "Range/Format/Underline", "Range/Format/Strikethrough",
		"Range/Erase/All", "Range/Erase/Content", "Range/Erase/Style",
		"Range/Alignment/Left", "Range/Alignment/Right", "Range/Alignment/Centre":
		a.startRangeSelect(path)
	case "File/Properties":
		a.propertiesLines = a.buildPropertiesLines()
		a.propertiesScroll = 0
		a.mode = modeProperties
	case "Data/Sort":
		a.startSortSelect()
	case "Search/Global":
		a.searchScope = "Global"
		a.searchInput = ""
		a.searchCursor = 0
		a.mode = modeSearchInput
		a.prompt = ""
		a.message = "Select range"
	case "Search/Sheet":
		a.searchScope = "Sheet"
		a.searchInput = ""
		a.searchCursor = 0
		a.mode = modeSearchInput
		a.prompt = ""
		a.message = "Select range"
	case "Copy":
		a.startCopySelect()
	case "Move":
		a.startMoveSelect()
	case "Quit":
		if a.dirty {
			a.mode = modeConfirmUnsaved
			a.pendingAction = actionQuit
			return nil
		}
		return tea.Quit
	default:
		// For leaf items without a specific action, just clear the menu
		if path == "Data/Table" {
			a.message = path + " (no action, NOT IMPLEMENTED YET)"
		} else {
			a.message = path + " (no action)"
		}
	}
	return nil
}

func (a *app) openMenu() {
	a.mode = modeMenu
	a.menuStack = []menuLevel{{items: menuTree, sel: 0}}
	a.message = "Type a highlighted capital letter to choose a command; Esc closes menu"
}

func (a *app) handleMenuKey(msg tea.KeyMsg) tea.Cmd {
	if len(a.menuStack) == 0 {
		return nil
	}
	top := &a.menuStack[len(a.menuStack)-1]

	switch msg.String() {
	case "esc":
		if len(a.menuStack) > 1 {
			a.menuStack = a.menuStack[:len(a.menuStack)-1]
		} else {
			a.closeOverlay()
		}
	case "left":
		top.sel = (top.sel - 1 + len(top.items)) % len(top.items)
		a.message = top.items[top.sel].label + " selected"
	case "right":
		top.sel = (top.sel + 1) % len(top.items)
		a.message = top.items[top.sel].label + " selected"
	case "enter":
		selItem := &top.items[top.sel]
		if len(selItem.items) > 0 {
			a.menuStack = append(a.menuStack, menuLevel{
				parentLabel: selItem.label,
				items:       selItem.items,
				sel:         0,
			})
		} else {
			return a.executeMenuLeaf()
		}
	default:
		if len(msg.Runes) > 0 {
			ch := unicode.ToUpper(msg.Runes[0])
			for i, item := range top.items {
				for _, r := range item.label {
					if unicode.IsUpper(r) && r == ch {
						top.sel = i
						if len(item.items) > 0 {
							a.menuStack = append(a.menuStack, menuLevel{
								parentLabel: item.label,
								items:       item.items,
								sel:         0,
							})
						} else {
							return a.executeMenuLeaf()
						}
						return nil
					}
				}
			}
		}
	}
	return nil
}

func (a *app) menuLine(items []menuEntry, selected int) string {
	var line strings.Builder
	for i, item := range items {
		if i > 0 {
			line.WriteString("  ")
		}
		if i == selected {
			line.WriteString(style(sgrTealBand, item.label))
		} else {
			line.WriteString(item.label)
		}
	}
	return fitDisplay(line.String(), a.width)
}

func (a *app) menuLines() []string {
	if len(a.menuStack) == 0 {
		return []string{blankLine(a.width), blankLine(a.width)}
	}
	top := a.menuStack[len(a.menuStack)-1]
	line1 := a.menuLine(top.items, top.sel)
	selItem := top.items[top.sel]
	if len(selItem.items) > 0 {
		return []string{line1, a.menuLine(selItem.items, -1)}
	}
	if selItem.desc != "" {
		return []string{line1, fitDisplay(selItem.desc, a.width)}
	}
	return []string{line1, blankLine(a.width)}
}
