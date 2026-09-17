package main

import (
	"tuisheet/internal/sheet"
)

func (a *app) takeSnapshot(comment string) {
	a.undoStack = append(a.undoStack, a.captureSnapshot(comment))
	if len(a.undoStack) > 20 {
		a.undoStack[0] = appSnapshot{}
		a.undoStack = a.undoStack[1:]
	}
	a.redoStack = nil
}

// snapshotSheet captures every field of a sheet in one place so a future
// field addition only needs to be added here and in toSheet.
func snapshotSheet(s *sheet.Sheet) sheetSnapshot {
	fr, fc := s.Freeze()
	return sheetSnapshot{
		cells:      s.Cells(),
		formulas:   s.Formulas(),
		styles:     s.Styles(),
		merges:     s.Merges(),
		tables:     s.Tables(),
		hiddenCols: s.HiddenCols(),
		hiddenRows: s.HiddenRows(),
		colWidths:  s.GetAllColWidths(),
		colStyles:  s.GetAllColStyles(),
		freezeRow:  fr,
		freezeCol:  fc,
	}
}

// toSheet restores a sheet from its snapshot; kept next to snapshotSheet
// so the field list stays in sync.
func (s sheetSnapshot) toSheet() *sheet.Sheet {
	return sheet.FromSnapshotWithFreeze(s.cells, s.formulas, s.styles, s.merges, s.tables, s.hiddenCols, s.hiddenRows, s.colWidths, s.colStyles, s.freezeRow, s.freezeCol)
}

func (a *app) captureSnapshot(comment string) appSnapshot {
	snaps := make([]sheetSnapshot, len(a.sheets))
	for i, s := range a.sheets {
		snaps[i] = snapshotSheet(s)
	}
	names := make([]string, len(a.sheetNames))
	copy(names, a.sheetNames)
	return appSnapshot{
		comment:     comment,
		sheets:      snaps,
		sheetNames:  names,
		activeSheet: a.activeSheet,
		colWidth:    a.defaultColWidth,
		docProps:    a.docProps,
		docDirty:    a.docPropsDirty,
	}
}

func (a *app) restoreSnapshot(snap *appSnapshot) {
	// Persist the live cursor/scroll under the pre-undo sheet name; the
	// per-sheet maps are keyed by name so they (and the cursors in them)
	// survive the rebuild below.
	a.saveLiveState()
	a.sheets = make([]*sheet.Sheet, len(snap.sheets))
	for i, s := range snap.sheets {
		a.sheets[i] = s.toSheet()
	}
	a.sheetNames = make([]string, len(snap.sheetNames))
	copy(a.sheetNames, snap.sheetNames)
	a.activeSheet = snap.activeSheet
	a.sheet = a.sheets[a.activeSheet]
	a.restoreActiveState()
	a.defaultColWidth = snap.colWidth
	if a.defaultColWidth == 0 {
		a.defaultColWidth = 11
	}
	a.docProps = snap.docProps
	a.docPropsDirty = snap.docDirty
	a.wireResolvers()
}

func (a *app) undo() {
	if len(a.undoStack) == 0 {
		a.message = "Nothing to undo"
		return
	}
	a.redoStack = append(a.redoStack, a.captureSnapshot(a.undoStack[len(a.undoStack)-1].comment))
	snap := a.undoStack[len(a.undoStack)-1]
	a.undoStack = a.undoStack[:len(a.undoStack)-1]
	a.restoreSnapshot(&snap)
	a.dirty = true
	a.message = "Undo: " + snap.comment
}

func (a *app) redo() {
	if len(a.redoStack) == 0 {
		a.message = "Nothing to redo"
		return
	}
	a.undoStack = append(a.undoStack, a.captureSnapshot(a.redoStack[len(a.redoStack)-1].comment))
	snap := a.redoStack[len(a.redoStack)-1]
	a.redoStack = a.redoStack[:len(a.redoStack)-1]
	a.restoreSnapshot(&snap)
	a.dirty = true
	a.message = "Redo: " + snap.comment
}
