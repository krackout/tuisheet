package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/mattn/go-runewidth"
	"golang.org/x/text/unicode/norm"

	"tuisheet/internal/buildinfo"
	"tuisheet/internal/locale"

	"tuisheet/internal/sheet"
	"tuisheet/internal/xlsx"
)

const (
	rowHeaderWidth = 5
	defaultWidth   = 80
	defaultHeight  = 24
)

const (
	sgrReset         = "\x1b[0m"
	sgrTealBand      = "\x1b[30;46m"
	sgrBlueBand      = "\x1b[97;44m"
	sgrReverseVideo  = "\x1b[7m"
	sgrBold          = "\x1b[1m"
	sgrItalic        = "\x1b[3m"
	sgrUnderline     = "\x1b[4m"
	sgrStrikethrough = "\x1b[9m"
)

type fileEntry struct {
	name  string
	isDir bool
	path  string
}

type mode int

const (
	modeNormal mode = iota
	modeMenu
	modeEdit
	modePopup
	modeOpenFile
	modeSave
	modeRenameSheet
	modeSheetTabPopup
	modeConfirmDelete
	modeConfirmUnsaved
	modeUndoHistory
	modeHelp
	modeGeneralHelp
	modeColWidth
	modeColHide
	modeColDisplay
	modeRowHide
	modeRowDisplay
	modeRowDelete
	modeRangeSelect
	modeCopySelect
	modeCopyDest
	modeMoveSelect
	modeMoveDest
	modeConfirmOverwrite
	modeSearchInput
	modeSearchResults
	modeProperties
	modePropertiesEdit
	modeSortSelect
	modeSortOptions
	modeConfirmSaveOverwrite
	modeSheetMove
)

const (
	actionNone int = iota
	actionQuit
	actionRetrieve
)

const (
	viewNormal int = iota
	viewHorizontal
	viewVertical
	viewPerspective
)

type app struct {
	sheet             *sheet.Sheet
	sheets            []*sheet.Sheet
	sheetNames        []string
	active            sheet.Coord
	rowOffset         int
	colOffset         int
	mode              mode
	edit              string
	editCursor        int
	generalHelpScroll int
	fileInput         string
	fileDir           string
	fileEntries       []fileEntry
	fileCursor        int
	fileInputCursor   int
	saveInput         string
	saveCursor        int
	pendingSaveFile   string
	colWidthInput     string
	colWidthGlobal    bool
	colHideInput      string
	rowHideInput      string
	renameInput       string
	renameCursor      int
	message           string
	prompt            string
	dirty             bool
	pendingAction     int
	currentFile       string
	width             int
	height            int
	popupRow          int
	popupCol          int
	popupContext      int // 0=cell, 1=row, 2=col
	activeSheet       int
	// Per-sheet cursor and scroll, keyed by sheet name so insert,
	// delete, move and undo need no index fixups. The scalar
	// active/rowOffset/colOffset always hold the live values for the
	// active sheet; they are saved/restored on every sheet switch.
	cursors                 map[string]sheet.Coord
	scrolls                 map[string]sheet.Coord
	sheetTabPopupSheetIndex int
	tabOffset               int
	undoStack               []appSnapshot
	undoSelect              int
	redoStack               []appSnapshot
	redoSelect              int
	historyShowRedo         bool
	defaultColWidth         int
	viewMode                int
	syncScroll              bool
	viewOffsets             map[int]sheet.Coord
	viewSheets              []int
	menuStack               []menuLevel
	gridCacheKey            string
	gridCache               []string
	rangeAnchor             sheet.Coord
	rangeEnd                sheet.Coord
	rangePending            string // e.g. "Range/Format/Number"
	rangeSelecting          bool
	copySrcRange            sheet.Range
	copySrcSheet            int
	copyOp                  string // "Copy" or "Move"
	pendingCopySrc          sheet.Range
	pendingCopyDest         sheet.Coord
	pendingCopyDestSheet    int
	pendingCopyOp           string
	searchInput             string
	searchCursor            int
	searchScope             string // "Global" or "Sheet"
	searchResults           []searchResult
	searchSelected          int
	searchScroll            int
	propertiesLines         []string
	propertiesScroll        int
	docProps                xlsx.DocProps
	docPropsDirty           bool
	docPropsLoaded          bool
	propEditField           int
	propEditInput           string
	propEditCursor          int
	propEditOrig            xlsx.DocProps
	sortRange               sheet.Range
	sortPrimaryCol          string
	sortSecondaryCol        string
	sortPrimaryAsc          bool
	sortSecondaryAsc        bool
	sortFocus               int // 0 primary col, 1 primary order, 2 secondary col, 3 secondary order
	sheetMoveIdx            int
	sheetMoveOrigNames      []string
	sheetMoveOrigSheets     []*sheet.Sheet
	sheetMoveOrigActive     int
}

type searchResult struct {
	sheetIdx  int
	sheetName string
	coord     sheet.Coord
	display   string
	raw       string
}

type sheetSnapshot struct {
	cells      map[sheet.Coord]string
	formulas   map[sheet.Coord]string
	styles     map[sheet.Coord]sheet.Style
	merges     []sheet.Range
	tables     []sheet.TableDef
	hiddenCols map[int]bool
	hiddenRows map[int]bool
	colWidths  map[int]int
	colStyles  map[int]sheet.Style
	freezeRow  int
	freezeCol  int
}

type appSnapshot struct {
	comment     string
	sheets      []sheetSnapshot
	sheetNames  []string
	activeSheet int
	colWidth    int
	docProps    xlsx.DocProps
	docDirty    bool
}

type menuEntry struct {
	label string
	desc  string
	items []menuEntry
}

type menuLevel struct {
	parentLabel string
	items       []menuEntry
	sel         int
}

const helpText = `

tuisheet, a terminal user interface spreadsheet programme.
Supports xlsx (OpenXML) files.

For help, press F1 inside the programme.
/ (slash) opens the menu. Capital letters (usually the
first letter of the menu option) select/open an option.
You can also use arrow keys to select, Enter to open.

You can directly open a file,
tuisheet file.xlsx or,
/ -> F -> R
(slash -> File -> Retrieve-load)
and choose it on file selector

For version, tuisheet --version or -V

`

func printHelp() {
	fmt.Fprint(os.Stdout, strings.TrimPrefix(helpText, "\n"))
}

func printVersion() {
	fmt.Fprintf(os.Stdout, "%s %s\n", buildinfo.AppName, buildinfo.AppVersion)
}

func main() {
	args := os.Args[1:]
	if len(args) == 1 {
		switch args[0] {
		case "--help", "-h", "-?":
			printHelp()
			return
		case "--version", "-V":
			printVersion()
			return
		default:
			if strings.HasPrefix(args[0], "-") {
				fmt.Fprintf(os.Stdout, "Unknown parameter: %s\n", args[0])
				printHelp()
				os.Exit(1)
				return
			}
		}
	} else if len(args) > 1 {
		// Only one filename is supported; treat extra args as unknown parameter
		for _, a := range args {
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stdout, "Unknown parameter: %s\n", a)
				printHelp()
				os.Exit(1)
				return
			}
		}
		fmt.Fprintln(os.Stdout, "Unknown parameter")
		printHelp()
		os.Exit(1)
		return
	}

	a := newApp()
	if len(args) == 1 && !strings.HasPrefix(args[0], "-") {
		filename := args[0]
		if err := a.openXLSX(filename); err != nil {
			fmt.Fprintf(os.Stderr, "tuisheet: %v\n", err)
			os.Exit(1)
		}
	}
	p := tea.NewProgram(a, tea.WithAltScreen(), tea.WithMouseCellMotion(), tea.WithFPS(30), tea.WithFilter(func(_ tea.Model, msg tea.Msg) tea.Msg {
		if m, ok := msg.(tea.MouseMsg); ok && m.Action == tea.MouseActionMotion {
			return nil
		}
		return msg
	}))
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "tuisheet: %v\n", err)
		os.Exit(1)
	}
}

func newApp() *app {
	a := &app{
		sheet:  sheet.New(),
		width:  defaultWidth,
		height: defaultHeight,
	}
	a.seed()
	return a
}

func (a *app) seed() {
	a.sheets = []*sheet.Sheet{a.sheet}
	a.sheetNames = []string{"Φύλλο1"}
	a.tabOffset = 0
	a.syncScroll = false
	a.viewOffsets = make(map[int]sheet.Coord)
	a.cursors = make(map[string]sheet.Coord)
	a.scrolls = make(map[string]sheet.Coord)
	a.defaultColWidth = 11
	a.message = "Blank sheet  / opens menu"
	a.docProps = xlsx.DocProps{}
	a.docPropsDirty = false
	a.docPropsLoaded = false
}

// wireResolvers connects cross-sheet references ('Name'!A1) to the current
// sheet list. Must be re-run whenever sheets are created or replaced.
func (a *app) wireResolvers() {
	byName := make(map[string]*sheet.Sheet, len(a.sheets))
	for i, s := range a.sheets {
		if i < len(a.sheetNames) {
			byName[strings.ToLower(a.sheetNames[i])] = s
		}
	}
	for _, s := range a.sheets {
		s.SetSheetResolver(func(name string) *sheet.Sheet {
			return byName[strings.ToLower(name)]
		})
	}
}

func (a *app) openXLSX(filename string) error {
	// Parse first: if the file fails to open the current workbook must
	// stay completely untouched (including undo history).
	wb, err := xlsx.Open(filename)
	if err != nil {
		return err
	}
	newSheets := make([]*sheet.Sheet, 0, len(wb.Sheets))
	names := make([]string, 0, len(wb.Sheets))
	for _, ws := range wb.Sheets {
		newSheets = append(newSheets, ws.Sheet)
		names = append(names, ws.Name)
	}

	a.undoStack = nil
	a.redoStack = nil
	a.viewOffsets = make(map[int]sheet.Coord)
	a.cursors = make(map[string]sheet.Coord)
	a.scrolls = make(map[string]sheet.Coord)
	a.defaultColWidth = 11
	a.sheets = newSheets
	a.sheetNames = names
	a.activeSheet = 0
	a.sheet = a.sheets[0]
	a.ensureSheetVisible()
	a.active = sheet.Coord{}
	a.rowOffset = 0
	a.colOffset = 0
	// Snap offsets past frozen panes and drop the previous file's grid
	// cache: without this a frozen file opened in-session renders with
	// rowOffset 0 (frozen rows repeated) until the next keypress, since
	// no WindowSizeMsg arrives to trigger ensureVisible.
	a.ensureVisible()
	a.gridCacheKey = ""
	a.gridCache = nil
	a.currentFile = filename
	a.dirty = false
	if p, ok := xlsx.LoadDocProps(filename); ok {
		a.docProps = p
		a.docPropsLoaded = true
	} else {
		a.docProps = xlsx.DocProps{}
		a.docPropsLoaded = false
	}
	a.docPropsDirty = false
	a.wireResolvers()
	a.message = "Opened " + filename
	return nil
}

func (a *app) Init() tea.Cmd {
	return nil
}

func (a *app) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.width = max(1, msg.Width)
		a.height = max(1, msg.Height)
		a.ensureVisible()
	case tea.KeyMsg:
		return a, a.handleKey(msg)
	case tea.MouseMsg:
		a.handleMouse(tea.MouseEvent(msg))
	}
	return a, nil
}

func (a *app) handleKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()

	if key == "ctrl+z" && a.mode != modeEdit {
		a.undo()
		return nil
	}
	if key == "ctrl+y" && a.mode != modeEdit {
		a.redo()
		return nil
	}

	if a.mode == modeConfirmUnsaved {
		return a.handleConfirmUnsavedKey(msg)
	}
	if a.mode == modeEdit {
		return a.handleEditKey(msg)
	}
	if a.mode == modeOpenFile {
		return a.handleOpenFileKey(msg)
	}
	if a.mode == modeSave {
		return a.handleSaveKey(msg)
	}
	if a.mode == modeRenameSheet {
		return a.handleRenameSheetKey(msg)
	}
	if a.mode == modeMenu {
		return a.handleMenuKey(msg)
	}
	if a.mode == modePopup {
		return a.handlePopupKey(msg)
	}
	if a.mode == modeSheetTabPopup {
		return a.handleSheetTabPopupKey(msg)
	}
	if a.mode == modeConfirmDelete {
		return a.handleConfirmDeleteKey(msg)
	}
	if a.mode == modeUndoHistory {
		return a.handleUndoHistoryKey(msg)
	}
	if a.mode == modeHelp {
		return a.handleHelpKey(msg)
	}
	if a.mode == modeGeneralHelp {
		return a.handleGeneralHelpKey(msg)
	}
	if a.mode == modeColWidth {
		return a.handleColWidthKey(msg)
	}
	if a.mode == modeColHide {
		return a.handleColHideKey(msg)
	}
	if a.mode == modeColDisplay {
		return a.handleColDisplayKey(msg)
	}
	if a.mode == modeRowHide {
		return a.handleRowHideKey(msg)
	}
	if a.mode == modeRowDisplay {
		return a.handleRowDisplayKey(msg)
	}
	if a.mode == modeRowDelete {
		return a.handleRowDeleteKey(msg)
	}
	if a.mode == modeRangeSelect {
		return a.handleRangeSelectKey(msg)
	}
	if a.mode == modeCopySelect {
		return a.handleCopySelectKey(msg)
	}
	if a.mode == modeCopyDest {
		return a.handleCopyDestKey(msg)
	}
	if a.mode == modeMoveSelect {
		return a.handleMoveSelectKey(msg)
	}
	if a.mode == modeMoveDest {
		return a.handleMoveDestKey(msg)
	}
	if a.mode == modeConfirmOverwrite {
		return a.handleConfirmOverwriteKey(msg)
	}
	if a.mode == modeSearchInput {
		return a.handleSearchInputKey(msg)
	}
	if a.mode == modeSearchResults {
		return a.handleSearchResultsKey(msg)
	}
	if a.mode == modeProperties {
		return a.handlePropertiesKey(msg)
	}
	if a.mode == modePropertiesEdit {
		return a.handlePropertiesEditKey(msg)
	}
	if a.mode == modeSortSelect {
		return a.handleSortSelectKey(msg)
	}
	if a.mode == modeSortOptions {
		return a.handleSortOptionsKey(msg)
	}
	if a.mode == modeConfirmSaveOverwrite {
		return a.handleConfirmSaveOverwriteKey(msg)
	}
	if a.mode == modeSheetMove {
		return a.handleSheetMoveKey(msg)
	}

	switch key {
	case "ctrl+f":
		if len(a.searchResults) > 0 {
			a.mode = modeSearchResults
			a.message = fmt.Sprintf("Search results (%d)", len(a.searchResults))
		} else {
			a.message = "No search results — use / Search"
		}
	case "esc":
		a.closeOverlay()
	case "enter":
		a.handleEnter()
	case "f2":
		a.handleEnter() // mainstream convention: F2 edits the active cell
	case "f1":
		a.generalHelpScroll = 0
		a.mode = modeGeneralHelp
	case "up":
		a.move(-1, 0)
	case "down":
		a.move(1, 0)
	case "left":
		a.move(0, -1)
	case "right":
		a.move(0, 1)
	case "ctrl+pgup":
		a.switchSheet(-1)
	case "ctrl+pgdown":
		a.switchSheet(1)
	case "pgup":
		a.movePage(-1)
	case "pgdown":
		a.movePage(1)
	case "home":
		a.active = sheet.Coord{Row: 0, Col: 0}
		a.ensureVisible()
		a.message = "Go to A1"
	case "ctrl+home":
		a.active = sheet.Coord{Row: 0, Col: 0}
		a.ensureVisible()
		a.message = "Go to A1"
	case "end":
		if col := a.sheet.LastUsedInRow(a.active.Row); col >= 0 {
			a.active.Col = col
		} else {
			_, maxCol := a.sheet.MaxUsed()
			if maxCol >= 0 {
				a.active.Col = maxCol
			}
		}
		if a.active.Col >= sheet.MaxColumns {
			a.active.Col = sheet.MaxColumns - 1
		}
		a.ensureVisible()
		a.message = "Go to " + a.active.String()
	case "ctrl+end":
		maxRow, maxCol := a.sheet.MaxUsed()
		if maxRow >= 0 && maxCol >= 0 {
			if maxRow >= sheet.MaxRows {
				maxRow = sheet.MaxRows - 1
			}
			if maxCol >= sheet.MaxColumns {
				maxCol = sheet.MaxColumns - 1
			}
			a.active = sheet.Coord{Row: maxRow, Col: maxCol}
			a.ensureVisible()
			a.message = "Go to " + a.active.String()
		}
	case "/":
		if a.mode == modeNormal {
			a.openMenu()
		}
	default:
		if len(msg.Runes) > 0 {
			a.mode = modeEdit
			a.edit = string(msg.Runes)
			a.editCursor = utf8.RuneCountInString(a.edit)
		}
	}
	return nil
}

func (a *app) handlePopupKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "enter":
		a.closeOverlay()
	}
	return nil
}

func (a *app) handleEditKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		a.mode = modeNormal
		a.edit = ""
	case "enter":
		if strings.TrimSpace(a.edit) == "" {
			a.takeSnapshot("Clear " + a.active.String())
		} else {
			a.takeSnapshot("Edit " + a.active.String())
		}
		a.sheet.SetUserInput(a.active, a.edit)
		a.dirty = true
		a.mode = modeNormal
		a.message = "Stored " + a.active.String()
		a.active.Row++
		a.ensureVisible()
	case "tab":
		if strings.TrimSpace(a.edit) == "" {
			a.takeSnapshot("Clear " + a.active.String())
		} else {
			a.takeSnapshot("Edit " + a.active.String())
		}
		a.sheet.SetUserInput(a.active, a.edit)
		a.dirty = true
		a.mode = modeNormal
		prev := a.active.String()
		a.message = "Stored " + prev
		// move right, skipping hidden columns
		nextCol := a.active.Col + 1
		for nextCol < sheet.MaxColumns && a.sheet.IsColHidden(nextCol) {
			nextCol++
		}
		if nextCol < sheet.MaxColumns {
			a.active.Col = nextCol
		}
		a.ensureVisible()
	case "backspace", "ctrl+h":
		lineBackspace(&a.edit, &a.editCursor)
	case "left":
		lineMoveLeft(&a.editCursor)
	case "right":
		lineMoveRight(a.edit, &a.editCursor)
	case "home", "ctrl+a":
		lineHome(&a.editCursor)
	case "end", "ctrl+e":
		lineEnd(a.edit, &a.editCursor)
	default:
		if len(msg.Runes) > 0 {
			lineInsert(&a.edit, &a.editCursor, string(msg.Runes))
		}
	}
	return nil
}

func (a *app) handleOpenFileKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		a.exitOpenFile()
		a.message = "File retrieve cancelled"
	case "enter":
		typed := strings.TrimSpace(a.fileInput)
		if typed != "" {
			if info, err := os.Stat(typed); err == nil && info.IsDir() {
				a.fileDir = typed
				a.fileCursor = 0
				a.fileInput = ""
				a.fileInputCursor = 0
				a.refreshFileList()
				return nil
			}
			if err := a.openXLSX(typed); err != nil {
				a.message = "Open failed: " + err.Error()
				return nil
			}
			a.exitOpenFile()
			return nil
		}
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
			if err := a.openXLSX(entry.path); err != nil {
				a.message = "Open failed: " + err.Error()
				return nil
			}
			a.exitOpenFile()
		} else {
			a.message = "No xlsx files found in this directory"
		}
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
		lineMoveLeft(&a.fileInputCursor)
	case "right":
		lineMoveRight(a.fileInput, &a.fileInputCursor)
	case "home", "ctrl+a":
		lineHome(&a.fileInputCursor)
	case "end", "ctrl+e":
		lineEnd(a.fileInput, &a.fileInputCursor)
	case "backspace", "ctrl+h":
		lineBackspace(&a.fileInput, &a.fileInputCursor)
	default:
		if len(msg.Runes) > 0 {
			lineInsert(&a.fileInput, &a.fileInputCursor, string(msg.Runes))
		}
	}
	return nil
}

func (a *app) handleRenameSheetKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		a.mode = modeNormal
		a.renameInput = ""
		a.renameCursor = 0
		a.message = "Rename sheet cancelled"
	case "enter":
		name := strings.TrimSpace(a.renameInput)
		if name == "" {
			a.message = "Sheet name cannot be blank"
			return nil
		}
		for i, n := range a.sheetNames {
			if i != a.activeSheet && strings.EqualFold(n, name) {
				a.message = "Sheet name already exists"
				return nil
			}
		}
		if err := sheet.ValidateSheetName(name); err != nil {
			a.message = err.Error()
			return nil
		}
		old := a.sheetNames[a.activeSheet]
		a.takeSnapshot("Rename sheet")
		if old != name {
			a.dirty = true
		}
		a.sheetNames[a.activeSheet] = name
		if cur, ok := a.cursors[old]; ok {
			a.cursors[name] = cur
			delete(a.cursors, old)
		}
		if off, ok := a.scrolls[old]; ok {
			a.scrolls[name] = off
			delete(a.scrolls, old)
		}
		if !strings.EqualFold(old, name) {
			for _, sh := range a.sheets {
				sh.RenameSheetInFormulas(old, name)
			}
			a.wireResolvers()
		}
		a.mode = modeNormal
		a.renameInput = ""
		a.renameCursor = 0
		a.message = "Renamed sheet to " + name
	case "left":
		lineMoveLeft(&a.renameCursor)
	case "right":
		lineMoveRight(a.renameInput, &a.renameCursor)
	case "home", "ctrl+a":
		lineHome(&a.renameCursor)
	case "end", "ctrl+e":
		lineEnd(a.renameInput, &a.renameCursor)
	case "backspace", "ctrl+h":
		lineBackspace(&a.renameInput, &a.renameCursor)
	default:
		if len(msg.Runes) > 0 {
			lineInsert(&a.renameInput, &a.renameCursor, string(msg.Runes))
		}
	}
	return nil
}

func (a *app) handleMouse(msg tea.MouseEvent) {
	if msg.Action != tea.MouseActionPress {
		return
	}
	switch msg.Button {
	case tea.MouseButtonRight:
		if a.handleSheetTabRightClick(msg.X, msg.Y) {
			return
		}
		if a.viewMode != viewNormal {
			return
		}
		sheetIdx, row, col, hitOk := a.hitTest(msg.X, msg.Y)

		if msg.Y == a.gridTop() {
			// Column header
			a.popupContext = 2
			a.message = "Right-click column"
			gC := a.screenXToCol(a.sheet, a.colOffset, msg.X)
			if gC >= 0 {
				a.active.Col = gC
			}
		} else if hitOk && col < 0 {
			// Row header
			if sheetIdx != a.activeSheet {
				a.setActiveSheetNoReset(sheetIdx)
			}
			a.popupContext = 1
			a.message = "Right-click row"
			if row >= 0 {
				a.active.Row = row
			}
		} else if hitOk && col >= 0 {
			// Cell
			if sheetIdx != a.activeSheet {
				a.setActiveSheetNoReset(sheetIdx)
			}
			a.popupContext = 0
			a.message = "Right-click menu: Delete cells"
			a.active = sheet.Coord{Row: row, Col: col}
			a.ensureVisible()
		} else {
			return
		}
		a.mode = modePopup
		a.popupCol = msg.X
		a.popupRow = msg.Y
	case tea.MouseButtonLeft:
		if a.mode == modeSheetMove && a.handleSheetMoveClick(msg.X, msg.Y) {
			return
		}
		if a.mode == modePopup && a.handlePopupClick(msg.X, msg.Y) {
			return
		}
		if a.mode == modeSheetTabPopup && a.handleSheetTabPopupClick(msg.X, msg.Y) {
			return
		}
		if a.selectSheetFromScreen(msg.X, msg.Y) {
			return
		}
		a.selectFromScreen(msg.X, msg.Y)
	}
}

func (a *app) handleSheetTabRightClick(x, y int) bool {
	if y != a.tabRow() {
		return false
	}
	index, ok := a.sheetIndexAt(x)
	if !ok {
		return false
	}
	a.setActiveSheetNoReset(index)
	if a.viewMode != viewNormal {
		a.initViewSheets()
	}
	a.mode = modeSheetTabPopup
	a.sheetTabPopupSheetIndex = index
	a.popupCol = x
	a.popupRow = y
	a.message = "Sheet tab menu"
	return true
}

func (a *app) handleSheetTabPopupKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		a.mode = modeNormal
		a.message = ""
		a.prompt = ""
	}
	return nil
}

func (a *app) handleSheetTabPopupClick(x, y int) bool {
	width := min(22, max(1, a.width))
	popupX := min(max(0, a.popupCol), max(0, a.width-width))
	popupY := min(max(1, a.tabRow()+1), max(1, a.height-len(sheetTabPopupMenu)-1))
	if y < popupY || y >= popupY+len(sheetTabPopupMenu) || x < popupX || x >= popupX+width {
		a.mode = modeNormal
		return false
	}
	index := y - popupY
	switch index {
	case 0:
		a.mode = modeRenameSheet
		a.renameInput = a.sheetNames[a.activeSheet]
		a.renameCursor = utf8.RuneCountInString(a.renameInput)
		a.message = "Rename sheet: "
	case 1:
		if len(a.sheets) <= 1 {
			a.message = "Cannot delete the last sheet"
			a.mode = modeNormal
			return true
		}
		a.mode = modeConfirmDelete
	}
	return true
}

func (a *app) handlePopupClick(x, y int) bool {
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
	popupX := min(max(0, a.popupCol), max(0, a.width-width))
	popupY := min(max(1, a.popupRow), max(1, a.height-len(menu)-1))
	if y < popupY || y >= popupY+len(menu) || x < popupX || x >= popupX+width {
		a.mode = modeNormal
		return false
	}
	index := y - popupY
	if index < 0 || index >= len(menu) {
		return true
	}
	switch a.popupContext {
	case 0:
		if index == 0 {
			a.takeSnapshot("Delete cells up")
			a.sheet.DeleteCellShiftUp(a.active)
		} else if index == 1 {
			a.takeSnapshot("Delete cells left")
			a.sheet.DeleteCellShiftLeft(a.active)
		} else {
			return true
		}
		a.dirty = true
		a.mode = modeNormal
		a.message = "Deleted"
	case 1:
		switch index {
		case 0:
			a.takeSnapshot("Insert row above")
			a.sheet.InsertRow(a.active.Row)
			a.dirty = true
			a.message = fmt.Sprintf("Inserted row above %d", a.active.Row+1)
		case 1:
			a.takeSnapshot("Insert row below")
			a.sheet.InsertRow(a.active.Row + 1)
			a.dirty = true
			a.message = fmt.Sprintf("Inserted row below %d", a.active.Row+1)
		case 2:
			a.takeSnapshot(fmt.Sprintf("Hide row %d", a.active.Row+1))
			a.sheet.HideRow(a.active.Row)
			a.dirty = true
			a.message = fmt.Sprintf("Row %d hidden", a.active.Row+1)
		case 3:
			a.takeSnapshot("Delete row")
			a.sheet.DeleteRow(a.active.Row)
			a.dirty = true
			a.message = "Deleted"
		default:
			return true
		}
		a.mode = modeNormal
	case 2:
		switch index {
		case 0:
			a.colWidthGlobal = false
			a.colWidthInput = strconv.Itoa(a.defaultColWidth)
			a.mode = modeColWidth
			return true
		case 1:
			a.takeSnapshot("Reset column width")
			a.sheet.ResetColWidth(a.active.Col)
			a.message = "Column " + sheet.ColumnName(a.active.Col) + " width reset"
		case 2:
			a.takeSnapshot("Insert column left")
			a.sheet.InsertColumn(a.active.Col)
			a.dirty = true
			a.message = "Inserted column left of " + sheet.ColumnName(a.active.Col)
			a.mode = modeNormal
			return true
		case 3:
			a.takeSnapshot("Insert column right")
			a.sheet.InsertColumn(a.active.Col + 1)
			a.dirty = true
			a.message = "Inserted column right of " + sheet.ColumnName(a.active.Col)
			a.mode = modeNormal
			return true
		case 4:
			a.takeSnapshot("Hide column " + sheet.ColumnName(a.active.Col))
			a.sheet.HideCol(a.active.Col)
			a.message = "Column " + sheet.ColumnName(a.active.Col) + " hidden"
		case 5:
			a.takeSnapshot("Delete column " + sheet.ColumnName(a.active.Col))
			a.sheet.DeleteColumn(a.active.Col)
			a.message = "Deleted column " + sheet.ColumnName(a.active.Col)
		default:
			return true
		}
		a.dirty = true
		a.mode = modeNormal
	}
	return true
}

func (a *app) handleConfirmDeleteKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "n", "N":
		a.mode = modeNormal
		a.message = "Delete cancelled"
	case "y", "Y":
		a.deleteSheet(a.sheetTabPopupSheetIndex)
		a.mode = modeNormal
	}
	return nil
}

func (a *app) handleUndoHistoryKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		a.mode = modeMenu
	case "enter":
		if a.historyShowRedo {
			if len(a.redoStack) > 0 {
				sel := a.redoSelect
				if sel >= 0 && sel < len(a.redoStack) {
					snapIdx := len(a.redoStack) - 1 - sel
					snap := a.redoStack[snapIdx]
					// Save current state onto undo, push skipped onto undo
					a.undoStack = append(a.undoStack, a.captureSnapshot(snap.comment))
					for i := snapIdx + 1; i < len(a.redoStack); i++ {
						a.undoStack = append(a.undoStack, a.redoStack[i])
					}
					a.restoreSnapshot(&snap)
					a.redoStack = a.redoStack[:snapIdx]
					a.dirty = true
					a.message = "Redo to: " + snap.comment
				}
			}
		} else {
			if len(a.undoStack) > 0 {
				sel := a.undoSelect
				if sel >= 0 && sel < len(a.undoStack) {
					snapIdx := len(a.undoStack) - 1 - sel
					snap := a.undoStack[snapIdx]
					// Push current state and skipped snapshots onto redo
					a.redoStack = append(a.redoStack, a.captureSnapshot(snap.comment))
					for i := snapIdx + 1; i < len(a.undoStack); i++ {
						a.redoStack = append(a.redoStack, a.undoStack[i])
					}
					a.restoreSnapshot(&snap)
					a.undoStack = a.undoStack[:snapIdx]
					a.dirty = true
					a.message = "Undo to: " + snap.comment
				}
			}
		}
		a.mode = modeNormal
	case "up":
		if a.historyShowRedo {
			if a.redoSelect > 0 {
				a.redoSelect--
			}
		} else {
			if a.undoSelect > 0 {
				a.undoSelect--
			}
		}
	case "down":
		if a.historyShowRedo {
			if len(a.redoStack) > 0 && a.redoSelect < len(a.redoStack)-1 {
				a.redoSelect++
			}
		} else {
			if len(a.undoStack) > 0 && a.undoSelect < len(a.undoStack)-1 {
				a.undoSelect++
			}
		}
	}
	return nil
}

func (a *app) handleGeneralHelpKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "enter", "q", "Q":
		a.mode = modeNormal
	case "down":
		a.generalHelpScroll++
	case "up":
		a.generalHelpScroll--
	case "pgdown":
		a.generalHelpScroll += generalHelpPageRows(a.height)
	case "pgup":
		a.generalHelpScroll -= generalHelpPageRows(a.height)
	case "end", "ctrl+e":
		a.generalHelpScroll = 1 << 30 // clamped by the renderer
	case "home", "ctrl+a":
		a.generalHelpScroll = 0
	}
	return nil
}

func (a *app) handleHelpKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "enter", "q", "Q":
		a.mode = modeMenu
	}
	return nil
}

func (a *app) handleColWidthKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "esc":
		a.mode = modeMenu
	case "enter":
		if a.colWidthInput == "" || a.colWidthInput == "0" {
			a.message = "Invalid width"
			a.mode = modeMenu
			return nil
		}
		w, err := strconv.Atoi(a.colWidthInput)
		if err != nil || w < 1 || w > 999 {
			a.message = "Invalid width (1-999)"
			a.mode = modeMenu
			return nil
		}
		if a.colWidthGlobal {
			a.takeSnapshot("Set global column width")
			a.defaultColWidth = w
			// Apply to all columns that have a specific width and clear others to use default
			for c := range a.sheet.GetAllColWidths() {
				a.sheet.SetColWidth(c, w)
			}
			a.dirty = true
			a.message = "All columns width set to " + strconv.Itoa(w)
		} else {
			a.sheet.SetColWidth(a.active.Col, w)
			a.dirty = true
			a.message = "Column " + sheet.ColumnName(a.active.Col) + " width set to " + strconv.Itoa(w)
		}
		a.colWidthGlobal = false
		a.mode = modeNormal
	case "backspace":
		if len(a.colWidthInput) > 0 {
			a.colWidthInput = a.colWidthInput[:len(a.colWidthInput)-1]
		}
	default:
		if len(msg.Runes) == 1 && msg.Runes[0] >= '0' && msg.Runes[0] <= '9' {
			if len(a.colWidthInput) < 3 {
				a.colWidthInput += string(msg.Runes)
			}
		}
	}
	return nil
}

func (a *app) handleColHideKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "esc":
		a.mode = modeMenu
	case "enter":
		col, err := sheet.ParseColumn(a.colHideInput)
		if err != nil {
			a.message = "Invalid column"
			a.mode = modeMenu
			return nil
		}
		a.takeSnapshot("Hide column " + sheet.ColumnName(col))
		a.sheet.HideCol(col)
		a.dirty = true
		a.message = "Column " + a.colHideInput + " hidden"
		a.mode = modeNormal
	case "backspace":
		if len(a.colHideInput) > 0 {
			a.colHideInput = a.colHideInput[:len(a.colHideInput)-1]
		}
	default:
		if len(msg.Runes) == 1 && unicode.IsLetter(msg.Runes[0]) {
			a.colHideInput = strings.ToUpper(string(msg.Runes[0]))
		}
	}
	return nil
}

func (a *app) handleColDisplayKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "esc":
		a.mode = modeMenu
	case "enter":
		col, err := sheet.ParseColumn(a.colHideInput)
		if err != nil {
			a.message = "Invalid column"
			a.mode = modeMenu
			return nil
		}
		a.takeSnapshot("Show column " + sheet.ColumnName(col))
		a.sheet.ShowCol(col)
		a.dirty = true
		a.message = "Column " + a.colHideInput + " visible"
		a.mode = modeNormal
	case "backspace":
		if len(a.colHideInput) > 0 {
			a.colHideInput = a.colHideInput[:len(a.colHideInput)-1]
		}
	default:
		if len(msg.Runes) == 1 && unicode.IsLetter(msg.Runes[0]) {
			a.colHideInput = strings.ToUpper(string(msg.Runes[0]))
		}
	}
	return nil
}

func parseRowRange(s string) (int, int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false
	}
	// Accept "2" or "2-100" or "2:100" with optional spaces
	s = strings.ReplaceAll(s, " ", "")
	sep := ""
	if strings.Contains(s, "-") {
		sep = "-"
	} else if strings.Contains(s, ":") {
		sep = ":"
	}
	if sep == "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > sheet.MaxRows {
			return 0, 0, false
		}
		return n - 1, n - 1, true
	}
	parts := strings.SplitN(s, sep, 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	a, err1 := strconv.Atoi(parts[0])
	b, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || a < 1 || b < 1 || a > sheet.MaxRows || b > sheet.MaxRows {
		return 0, 0, false
	}
	a--
	b--
	if a > b {
		a, b = b, a
	}
	return a, b, true
}

func (a *app) handleRowHideKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "esc":
		a.mode = modeMenu
		a.rowHideInput = ""
	case "enter":
		start, end, ok := parseRowRange(a.rowHideInput)
		if !ok {
			a.message = "Invalid row (e.g. 2 or 2-100)"
			a.mode = modeMenu
			a.rowHideInput = ""
			return nil
		}
		a.takeSnapshot(fmt.Sprintf("Hide rows %d-%d", start+1, end+1))
		for r := start; r <= end; r++ {
			a.sheet.HideRow(r)
		}
		a.dirty = true
		a.message = fmt.Sprintf("Rows %s hidden", a.rowHideInput)
		a.mode = modeNormal
		a.rowHideInput = ""
	case "backspace", "ctrl+h":
		if len(a.rowHideInput) > 0 {
			a.rowHideInput = a.rowHideInput[:len(a.rowHideInput)-1]
		}
	default:
		if len(msg.Runes) == 1 {
			r := msg.Runes[0]
			if (r >= '0' && r <= '9') || r == '-' || r == ':' {
				a.rowHideInput += string(r)
			}
		}
	}
	return nil
}

func (a *app) handleRowDisplayKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "esc":
		a.mode = modeMenu
		a.rowHideInput = ""
	case "enter":
		start, end, ok := parseRowRange(a.rowHideInput)
		if !ok {
			a.message = "Invalid row (e.g. 2 or 2-100)"
			a.mode = modeMenu
			a.rowHideInput = ""
			return nil
		}
		a.takeSnapshot(fmt.Sprintf("Display rows %d-%d", start+1, end+1))
		for r := start; r <= end; r++ {
			a.sheet.ShowRow(r)
		}
		a.dirty = true
		a.message = fmt.Sprintf("Rows %s visible", a.rowHideInput)
		a.mode = modeNormal
		a.rowHideInput = ""
	case "backspace", "ctrl+h":
		if len(a.rowHideInput) > 0 {
			a.rowHideInput = a.rowHideInput[:len(a.rowHideInput)-1]
		}
	default:
		if len(msg.Runes) == 1 {
			r := msg.Runes[0]
			if (r >= '0' && r <= '9') || r == '-' || r == ':' {
				a.rowHideInput += string(r)
			}
		}
	}
	return nil
}

func (a *app) handleRowDeleteKey(msg tea.KeyMsg) tea.Cmd {
	key := msg.String()
	switch key {
	case "esc":
		a.mode = modeMenu
		a.rowHideInput = ""
	case "enter":
		start, end, ok := parseRowRange(a.rowHideInput)
		if !ok {
			a.message = "Invalid row (e.g. 2 or 2-100)"
			a.mode = modeMenu
			a.rowHideInput = ""
			return nil
		}
		a.takeSnapshot(fmt.Sprintf("Delete rows %d-%d", start+1, end+1))
		for r := end; r >= start; r-- {
			a.sheet.DeleteRow(r)
		}
		a.dirty = true
		a.message = fmt.Sprintf("Rows %s deleted", a.rowHideInput)
		a.mode = modeNormal
		a.rowHideInput = ""
		// Clamp active row if beyond
		if a.active.Row > sheet.MaxRows-1 {
			a.active.Row = sheet.MaxRows - 1
		}
		a.ensureVisible()
	case "backspace", "ctrl+h":
		if len(a.rowHideInput) > 0 {
			a.rowHideInput = a.rowHideInput[:len(a.rowHideInput)-1]
		}
	default:
		if len(msg.Runes) == 1 {
			r := msg.Runes[0]
			if (r >= '0' && r <= '9') || r == '-' || r == ':' {
				a.rowHideInput += string(r)
			}
		}
	}
	return nil
}

func (a *app) handleSheetMoveKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		// Restore original order
		if a.sheetMoveOrigNames != nil {
			a.sheets = append([]*sheet.Sheet(nil), a.sheetMoveOrigSheets...)
			a.sheetNames = append([]string(nil), a.sheetMoveOrigNames...)
			a.activeSheet = a.sheetMoveOrigActive
			a.sheet = a.sheets[a.activeSheet]
			a.wireResolvers()
			a.ensureSheetVisible()
			if a.viewMode != viewNormal {
				a.initViewSheets()
			}
		}
		a.sheetMoveOrigNames = nil
		a.sheetMoveOrigSheets = nil
		a.mode = modeNormal
		a.message = "Move cancelled"
	case "enter":
		// Commit live order
		if a.sheetMoveOrigNames != nil {
			changed := false
			if len(a.sheetMoveOrigNames) != len(a.sheetNames) {
				changed = true
			} else {
				for i := range a.sheetMoveOrigNames {
					if a.sheetMoveOrigNames[i] != a.sheetNames[i] {
						changed = true
						break
					}
				}
			}
			if changed {
				a.takeSnapshot("Move sheet")
				a.dirty = true
			}
		}
		a.sheetMoveOrigNames = nil
		a.sheetMoveOrigSheets = nil
		a.activeSheet = a.sheetMoveIdx
		a.sheet = a.sheets[a.activeSheet]
		a.ensureSheetVisible()
		if a.viewMode != viewNormal {
			a.initViewSheets()
		}
		a.mode = modeNormal
		a.message = "Moved sheet to position " + strconv.Itoa(a.sheetMoveIdx+1)
	case "up", "k":
		if a.sheetMoveIdx > 0 {
			idx := a.sheetMoveIdx
			a.sheets[idx], a.sheets[idx-1] = a.sheets[idx-1], a.sheets[idx]
			a.sheetNames[idx], a.sheetNames[idx-1] = a.sheetNames[idx-1], a.sheetNames[idx]
			a.sheetMoveIdx--
			a.activeSheet = a.sheetMoveIdx
			a.sheet = a.sheets[a.activeSheet]
			a.wireResolvers()
			a.ensureSheetVisible()
			if a.viewMode != viewNormal {
				a.initViewSheets()
			}
		}
	case "down", "j":
		if a.sheetMoveIdx < len(a.sheets)-1 {
			idx := a.sheetMoveIdx
			a.sheets[idx], a.sheets[idx+1] = a.sheets[idx+1], a.sheets[idx]
			a.sheetNames[idx], a.sheetNames[idx+1] = a.sheetNames[idx+1], a.sheetNames[idx]
			a.sheetMoveIdx++
			a.activeSheet = a.sheetMoveIdx
			a.sheet = a.sheets[a.activeSheet]
			a.wireResolvers()
			a.ensureSheetVisible()
			if a.viewMode != viewNormal {
				a.initViewSheets()
			}
		}
	}
	return nil
}

func (a *app) handleSheetMoveClick(x, y int) bool {
	boxW := max(36, min(50, a.width-4))
	if boxW > a.width-2 {
		boxW = a.width - 2
	}
	boxX := (a.width - boxW) / 2
	innerH := len(a.sheetNames) + 3 // title + blank + sheets + blank + hint + borders
	boxH := innerH + 2
	boxY := max(0, (a.height-boxH)/2)
	if x < boxX || x >= boxX+boxW || y < boxY || y >= boxY+boxH {
		return false
	}
	// Click inside content: map to sheet index (offset 2: title, blank)
	innerY := y - boxY - 1
	if innerY < 2 || innerY >= 2+len(a.sheetNames) {
		return true // swallowed but no move
	}
	target := innerY - 2
	if target == a.sheetMoveIdx {
		return true
	}
	// Move element from current to target (live)
	cur := a.sheetMoveIdx
	elemSheet := a.sheets[cur]
	elemName := a.sheetNames[cur]
	// remove cur
	a.sheets = append(a.sheets[:cur], a.sheets[cur+1:]...)
	a.sheetNames = append(a.sheetNames[:cur], a.sheetNames[cur+1:]...)
	if target > cur {
		target--
	}
	// insert at target
	a.sheets = append(a.sheets[:target], append([]*sheet.Sheet{elemSheet}, a.sheets[target:]...)...)
	a.sheetNames = append(a.sheetNames[:target], append([]string{elemName}, a.sheetNames[target:]...)...)
	a.sheetMoveIdx = target
	a.activeSheet = target
	a.sheet = a.sheets[target]
	a.wireResolvers()
	a.ensureSheetVisible()
	if a.viewMode != viewNormal {
		a.initViewSheets()
	}
	return true
}

func (a *app) startRangeSelect(path string) {
	a.rangePending = path
	a.rangeAnchor = a.active
	a.rangeEnd = a.active
	a.rangeSelecting = true
	a.mode = modeRangeSelect
	r := a.rangeBounds()
	a.prompt = fmt.Sprintf("Range %s: select with arrows/PgUp/PgDn, Enter to apply, Esc to cancel (%s)", path, r.Start.String()+":"+r.End.String())
	a.message = "Select range"
}

func (a *app) rangeBounds() sheet.Range {
	r1 := a.rangeAnchor
	r2 := a.rangeEnd
	sr, er := r1.Row, r2.Row
	if sr > er {
		sr, er = er, sr
	}
	sc, ec := r1.Col, r2.Col
	if sc > ec {
		sc, ec = ec, sc
	}
	return sheet.Range{Start: sheet.Coord{Row: sr, Col: sc}, End: sheet.Coord{Row: er, Col: ec}}
}

func (a *app) isInRangeSelection(c sheet.Coord) bool {
	if !a.rangeSelecting {
		return false
	}
	if a.mode != modeRangeSelect && a.mode != modeCopySelect && a.mode != modeMoveSelect && a.mode != modeSortSelect {
		return false
	}
	r := a.rangeBounds()
	return c.Row >= r.Start.Row && c.Row <= r.End.Row && c.Col >= r.Start.Col && c.Col <= r.End.Col
}

func (a *app) rangeMove(dr, dc int) {
	newRow := a.active.Row + dr
	newCol := a.active.Col + dc
	if newRow < 0 {
		newRow = 0
	}
	if newCol < 0 {
		newCol = 0
	}
	if newRow >= sheet.MaxRows {
		newRow = sheet.MaxRows - 1
	}
	if newCol >= sheet.MaxColumns {
		newCol = sheet.MaxColumns - 1
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
		if col >= sheet.MaxColumns {
			col = sheet.MaxColumns - 1
		}
		newCol = col
	}
	a.active.Row = newRow
	a.active.Col = newCol
	a.rangeEnd = a.active
	a.ensureVisible()
	r := a.rangeBounds()
	a.prompt = fmt.Sprintf("Range %s: %s (Enter to apply, Esc to cancel)", a.rangePending, r.Start.String()+":"+r.End.String())
}

func (a *app) rangeMovePage(dr int) {
	rows := a.gridPageRows()
	a.active.Row += dr * rows
	if a.active.Row < 0 {
		a.active.Row = 0
	}
	if a.active.Row >= sheet.MaxRows {
		a.active.Row = sheet.MaxRows - 1
	}
	a.rangeEnd = a.active
	a.ensureVisible()
	r := a.rangeBounds()
	a.prompt = fmt.Sprintf("Range %s: %s (Enter to apply, Esc to cancel)", a.rangePending, r.Start.String()+":"+r.End.String())
}

func (a *app) handleRangeSelectKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		a.mode = modeNormal
		a.rangeSelecting = false
		a.prompt = ""
		a.message = "Range cancelled"
		return nil
	case "enter":
		a.applyRangePending()
		a.mode = modeNormal
		a.rangeSelecting = false
		a.prompt = ""
		return nil
	case "up":
		a.rangeMove(-1, 0)
	case "down":
		a.rangeMove(1, 0)
	case "left":
		a.rangeMove(0, -1)
	case "right":
		a.rangeMove(0, 1)
	case "pgup":
		a.rangeMovePage(-1)
	case "pgdown":
		a.rangeMovePage(1)
	}
	return nil
}

func (a *app) startCopySelect() {
	a.copyOp = "Copy"
	a.rangeAnchor = a.active
	a.rangeEnd = a.active
	a.rangeSelecting = true
	a.mode = modeCopySelect
	a.copySrcSheet = a.activeSheet
	r := a.rangeBounds()
	a.message = fmt.Sprintf("Copy: select source %s (arrows/PgUp/PgDn, Enter to next, Esc to cancel)", r.Start.String()+":"+r.End.String())
}

func (a *app) startMoveSelect() {
	a.copyOp = "Move"
	a.rangeAnchor = a.active
	a.rangeEnd = a.active
	a.rangeSelecting = true
	a.mode = modeMoveSelect
	a.copySrcSheet = a.activeSheet
	r := a.rangeBounds()
	a.message = fmt.Sprintf("Move: select source %s (arrows/PgUp/PgDn, Enter to next, Esc to cancel)", r.Start.String()+":"+r.End.String())
}

func (a *app) handleCopySelectKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		a.mode = modeNormal
		a.rangeSelecting = false
		a.copyOp = ""
		a.message = "Copy cancelled"
		return nil
	case "enter":
		a.copySrcRange = a.rangeBounds()
		a.copySrcSheet = a.activeSheet
		a.rangeSelecting = false
		a.mode = modeCopyDest
		a.message = fmt.Sprintf("Copy %s -> select destination (top-left) with arrows, Ctrl+PgUp/PgDn switch sheet, Enter to copy, Esc to cancel", a.copySrcRange.Start.String()+":"+a.copySrcRange.End.String())
		a.ensureVisible()
		return nil
	case "up":
		a.rangeMove(-1, 0)
		a.message = fmt.Sprintf("Copy: select source %s (Enter next, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	case "down":
		a.rangeMove(1, 0)
		a.message = fmt.Sprintf("Copy: select source %s (Enter next, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	case "left":
		a.rangeMove(0, -1)
		a.message = fmt.Sprintf("Copy: select source %s (Enter next, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	case "right":
		a.rangeMove(0, 1)
		a.message = fmt.Sprintf("Copy: select source %s (Enter next, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	case "pgup":
		a.rangeMovePage(-1)
		a.message = fmt.Sprintf("Copy: select source %s (Enter next, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	case "pgdown":
		a.rangeMovePage(1)
		a.message = fmt.Sprintf("Copy: select source %s (Enter next, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	}
	return nil
}

func (a *app) handleMoveSelectKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		a.mode = modeNormal
		a.rangeSelecting = false
		a.copyOp = ""
		a.message = "Move cancelled"
		return nil
	case "enter":
		a.copySrcRange = a.rangeBounds()
		a.copySrcSheet = a.activeSheet
		a.rangeSelecting = false
		a.mode = modeMoveDest
		a.message = fmt.Sprintf("Move %s -> select destination (top-left) with arrows, Ctrl+PgUp/PgDn switch sheet, Enter to move, Esc to cancel", a.copySrcRange.Start.String()+":"+a.copySrcRange.End.String())
		a.ensureVisible()
		return nil
	case "up":
		a.rangeMove(-1, 0)
		a.message = fmt.Sprintf("Move: select source %s (Enter next, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	case "down":
		a.rangeMove(1, 0)
		a.message = fmt.Sprintf("Move: select source %s (Enter next, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	case "left":
		a.rangeMove(0, -1)
		a.message = fmt.Sprintf("Move: select source %s (Enter next, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	case "right":
		a.rangeMove(0, 1)
		a.message = fmt.Sprintf("Move: select source %s (Enter next, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	case "pgup":
		a.rangeMovePage(-1)
		a.message = fmt.Sprintf("Move: select source %s (Enter next, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	case "pgdown":
		a.rangeMovePage(1)
		a.message = fmt.Sprintf("Move: select source %s (Enter next, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	}
	return nil
}

func (a *app) destBounds() sheet.Range {
	rows := a.copySrcRange.End.Row - a.copySrcRange.Start.Row + 1
	cols := a.copySrcRange.End.Col - a.copySrcRange.Start.Col + 1
	start := a.active
	end := sheet.Coord{Row: start.Row + rows - 1, Col: start.Col + cols - 1}
	return sheet.Range{Start: start, End: end}
}

func (a *app) isInCopyDestPreview(c sheet.Coord) bool {
	if a.mode != modeCopyDest && a.mode != modeMoveDest {
		return false
	}
	r := a.destBounds()
	return c.Row >= r.Start.Row && c.Row <= r.End.Row && c.Col >= r.Start.Col && c.Col <= r.End.Col
}

func (a *app) destMove(dr, dc int) {
	newRow := a.active.Row + dr
	newCol := a.active.Col + dc
	if newRow < 0 {
		newRow = 0
	}
	if newCol < 0 {
		newCol = 0
	}
	if newRow >= sheet.MaxRows {
		newRow = sheet.MaxRows - 1
	}
	if newCol >= sheet.MaxColumns {
		newCol = sheet.MaxColumns - 1
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
		if col >= 0 && col < sheet.MaxColumns {
			newCol = col
		}
	}
	a.active.Row = newRow
	a.active.Col = newCol
	a.ensureVisible()
	r := a.destBounds()
	a.message = fmt.Sprintf("%s %s -> %s (Enter to %s, Esc to cancel)", a.copyOp, a.copySrcRange.Start.String()+":"+a.copySrcRange.End.String(), r.Start.String(), strings.ToLower(a.copyOp))
}

func (a *app) destMovePage(dr int) {
	rows := a.gridPageRows()
	a.active.Row += dr * rows
	if a.active.Row < 0 {
		a.active.Row = 0
	}
	if a.active.Row >= sheet.MaxRows {
		a.active.Row = sheet.MaxRows - 1
	}
	a.ensureVisible()
	r := a.destBounds()
	a.message = fmt.Sprintf("%s %s -> %s (Enter to %s, Esc to cancel)", a.copyOp, a.copySrcRange.Start.String()+":"+a.copySrcRange.End.String(), r.Start.String(), strings.ToLower(a.copyOp))
}

func (a *app) handleCopyDestKey(msg tea.KeyMsg) tea.Cmd { return a.handleDestKey(msg, false) }
func (a *app) handleMoveDestKey(msg tea.KeyMsg) tea.Cmd { return a.handleDestKey(msg, true) }

func (a *app) handleDestKey(msg tea.KeyMsg, isMove bool) tea.Cmd {
	switch msg.String() {
	case "esc":
		a.mode = modeNormal
		a.copyOp = ""
		a.message = a.copyOp + " cancelled"
		if isMove {
			a.message = "Move cancelled"
		} else {
			a.message = "Copy cancelled"
		}
		return nil
	case "enter":
		a.attemptCopyMove(isMove)
		return nil
	case "up":
		a.destMove(-1, 0)
	case "down":
		a.destMove(1, 0)
	case "left":
		a.destMove(0, -1)
	case "right":
		a.destMove(0, 1)
	case "pgup":
		a.destMovePage(-1)
	case "pgdown":
		a.destMovePage(1)
	case "ctrl+pgup":
		if a.activeSheet > 0 {
			a.setActiveSheetNoReset(a.activeSheet - 1)
			a.ensureVisible()
			r := a.destBounds()
			a.message = fmt.Sprintf("%s %s -> %s on %s (Enter to %s, Esc to cancel)", a.copyOp, a.copySrcRange.Start.String()+":"+a.copySrcRange.End.String(), r.Start.String(), a.sheetNames[a.activeSheet], strings.ToLower(a.copyOp))
		}
	case "ctrl+pgdown":
		if a.activeSheet < len(a.sheets)-1 {
			a.setActiveSheetNoReset(a.activeSheet + 1)
			a.ensureVisible()
			r := a.destBounds()
			a.message = fmt.Sprintf("%s %s -> %s on %s (Enter to %s, Esc to cancel)", a.copyOp, a.copySrcRange.Start.String()+":"+a.copySrcRange.End.String(), r.Start.String(), a.sheetNames[a.activeSheet], strings.ToLower(a.copyOp))
		}
	}
	return nil
}

func (a *app) handleConfirmOverwriteKey(msg tea.KeyMsg) tea.Cmd {
	switch strings.ToLower(msg.String()) {
	case "y", "enter":
		a.doCopyMove(a.pendingCopyOp == "Move")
		a.mode = modeNormal
		a.pendingCopyOp = ""
		return nil
	case "n", "esc":
		a.mode = modeNormal
		a.pendingCopyOp = ""
		a.message = a.copyOp + " cancelled (overwrite denied)"
		a.copyOp = ""
		return nil
	}
	return nil
}

func (a *app) setPropFieldValue(idx int, v string) {
	v = strings.TrimSpace(v)
	if idx < 0 || idx >= len(propFields) {
		return
	}
	propFields[idx].set(&a.propEditOrig, v)
}

func (a *app) startSortSelect() {
	a.rangeAnchor = a.active
	a.rangeEnd = a.active
	a.rangeSelecting = true
	a.mode = modeSortSelect
	r := a.rangeBounds()
	a.prompt = fmt.Sprintf("Sort: select range %s (arrows/PgUp/PgDn, Enter OK, Esc cancel)", r.Start.String()+":"+r.End.String())
	a.message = "Select range"
}

func (a *app) handleSortSelectKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q", "Q":
		a.mode = modeNormal
		a.rangeSelecting = false
		a.prompt = ""
		a.message = "Sort cancelled"
		return nil
	case "enter":
		a.sortRange = a.rangeBounds()
		a.rangeSelecting = false
		a.sortPrimaryCol = ""
		a.sortSecondaryCol = ""
		a.sortPrimaryAsc = true
		a.sortSecondaryAsc = true
		a.sortFocus = 0
		a.mode = modeSortOptions
		a.prompt = ""
		return nil
	case "up":
		a.rangeMove(-1, 0)
		a.prompt = fmt.Sprintf("Sort: select range %s (Enter OK, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	case "down":
		a.rangeMove(1, 0)
		a.prompt = fmt.Sprintf("Sort: select range %s (Enter OK, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	case "left":
		a.rangeMove(0, -1)
		a.prompt = fmt.Sprintf("Sort: select range %s (Enter OK, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	case "right":
		a.rangeMove(0, 1)
		a.prompt = fmt.Sprintf("Sort: select range %s (Enter OK, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	case "pgup":
		a.rangeMovePage(-1)
		a.prompt = fmt.Sprintf("Sort: select range %s (Enter OK, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	case "pgdown":
		a.rangeMovePage(1)
		a.prompt = fmt.Sprintf("Sort: select range %s (Enter OK, Esc cancel)", a.rangeBounds().Start.String()+":"+a.rangeBounds().End.String())
	}
	return nil
}

func (a *app) handleSortOptionsKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc", "q", "Q":
		a.mode = modeNormal
		a.message = "Sort cancelled"
		return nil
	case "enter":
		// Validate and execute
		primary := strings.TrimSpace(strings.ToUpper(a.sortPrimaryCol))
		if primary == "" {
			a.message = "Primary column required"
			return nil
		}
		if _, err := sheet.ParseColumn(primary); err != nil {
			a.message = "Invalid primary column: " + primary
			return nil
		}
		pc, _ := sheet.ParseColumn(primary)
		if pc < a.sortRange.Start.Col || pc > a.sortRange.End.Col {
			a.message = fmt.Sprintf("Primary column %s out of range %s", primary, a.sortRange.Start.String()+":"+a.sortRange.End.String())
			return nil
		}
		secondary := strings.TrimSpace(strings.ToUpper(a.sortSecondaryCol))
		if secondary != "" {
			if _, err := sheet.ParseColumn(secondary); err != nil {
				a.message = "Invalid secondary column: " + secondary
				return nil
			}
			sc, _ := sheet.ParseColumn(secondary)
			if sc < a.sortRange.Start.Col || sc > a.sortRange.End.Col {
				a.message = fmt.Sprintf("Secondary column %s out of range %s", secondary, a.sortRange.Start.String()+":"+a.sortRange.End.String())
				return nil
			}
		}
		a.doSort()
		return nil
	case "tab", "down":
		a.sortFocus = (a.sortFocus + 1) % 4
	case "shift+tab", "up":
		a.sortFocus = (a.sortFocus + 3) % 4
	case " ":
		// Toggle order for focused key
		if a.sortFocus == 0 || a.sortFocus == 1 {
			a.sortPrimaryAsc = !a.sortPrimaryAsc
		} else {
			a.sortSecondaryAsc = !a.sortSecondaryAsc
		}
	case "backspace", "ctrl+h":
		if a.sortFocus == 0 {
			if len(a.sortPrimaryCol) > 0 {
				a.sortPrimaryCol = a.sortPrimaryCol[:len(a.sortPrimaryCol)-1]
			}
		} else if a.sortFocus == 2 {
			if len(a.sortSecondaryCol) > 0 {
				a.sortSecondaryCol = a.sortSecondaryCol[:len(a.sortSecondaryCol)-1]
			}
		} else if a.sortFocus == 1 {
			a.sortPrimaryAsc = !a.sortPrimaryAsc
		} else if a.sortFocus == 3 {
			a.sortSecondaryAsc = !a.sortSecondaryAsc
		}
	case "left", "right":
		// treat as tab navigation
		if msg.String() == "left" {
			a.sortFocus = (a.sortFocus + 3) % 4
		} else {
			a.sortFocus = (a.sortFocus + 1) % 4
		}
	default:
		if len(msg.Runes) == 1 && unicode.IsLetter(msg.Runes[0]) {
			ch := strings.ToUpper(string(msg.Runes[0]))
			if a.sortFocus == 0 {
				if len(a.sortPrimaryCol) < 3 {
					a.sortPrimaryCol += ch
				}
			} else if a.sortFocus == 2 {
				if len(a.sortSecondaryCol) < 3 {
					a.sortSecondaryCol += ch
				}
			} else if a.sortFocus == 1 {
				// letter on order field toggles? ignore
			}
		}
	}
	return nil
}

func (a *app) doSort() {
	r := a.sortRange
	primary := strings.TrimSpace(strings.ToUpper(a.sortPrimaryCol))
	secondary := strings.TrimSpace(strings.ToUpper(a.sortSecondaryCol))
	pc, _ := sheet.ParseColumn(primary)
	var sc *int
	if secondary != "" {
		v, _ := sheet.ParseColumn(secondary)
		sc = &v
	}
	// Need at least 2 rows to sort
	if r.End.Row <= r.Start.Row {
		a.mode = modeNormal
		a.message = "Sort: single row, nothing to sort"
		return
	}
	a.takeSnapshot(fmt.Sprintf("Sort %s", r.Start.String()+":"+r.End.String()))
	// Collect row data
	type cellData struct {
		raw   string
		style sheet.Style
	}
	rows := r.End.Row - r.Start.Row + 1
	cols := r.End.Col - r.Start.Col + 1
	// snapshot all rows
	rowData := make([][]cellData, rows)
	for i := 0; i < rows; i++ {
		rowData[i] = make([]cellData, cols)
		for j := 0; j < cols; j++ {
			c := sheet.Coord{Row: r.Start.Row + i, Col: r.Start.Col + j}
			rowData[i][j] = cellData{raw: a.sheet.Raw(c), style: a.sheet.Style(c)}
		}
	}
	// Collect sort keys per row
	type key struct {
		idx            int
		primaryDisp    string
		primaryNum     float64
		primaryIsNum   bool
		secondaryDisp  string
		secondaryNum   float64
		secondaryIsNum bool
	}
	keys := make([]key, rows)
	for i := 0; i < rows; i++ {
		absRow := r.Start.Row + i
		primaryCoord := sheet.Coord{Row: absRow, Col: pc}
		disp := a.sheet.Display(primaryCoord)
		num, isNum := parseNumeric(disp)
		keys[i] = key{idx: i, primaryDisp: disp, primaryNum: num, primaryIsNum: isNum}
		if sc != nil {
			secCoord := sheet.Coord{Row: absRow, Col: *sc}
			disp2 := a.sheet.Display(secCoord)
			num2, isNum2 := parseNumeric(disp2)
			keys[i].secondaryDisp = disp2
			keys[i].secondaryNum = num2
			keys[i].secondaryIsNum = isNum2
		}
	}
	sort.SliceStable(keys, func(i, j int) bool {
		ki, kj := keys[i], keys[j]
		cmp := compareSortKey(ki.primaryDisp, ki.primaryNum, ki.primaryIsNum, kj.primaryDisp, kj.primaryNum, kj.primaryIsNum)
		if cmp != 0 {
			if a.sortPrimaryAsc {
				return cmp < 0
			}
			return cmp > 0
		}
		if sc != nil {
			cmp2 := compareSortKey(ki.secondaryDisp, ki.secondaryNum, ki.secondaryIsNum, kj.secondaryDisp, kj.secondaryNum, kj.secondaryIsNum)
			if cmp2 != 0 {
				if a.sortSecondaryAsc {
					return cmp2 < 0
				}
				return cmp2 > 0
			}
		}
		return ki.idx < kj.idx
	})
	// Reorder rows in sheet
	// Build reordered data
	reordered := make([][]cellData, rows)
	for i, k := range keys {
		reordered[i] = rowData[k.idx]
	}
	// Write back
	for i := 0; i < rows; i++ {
		for j := 0; j < cols; j++ {
			c := sheet.Coord{Row: r.Start.Row + i, Col: r.Start.Col + j}
			cd := reordered[i][j]
			a.sheet.Set(c, cd.raw)
			a.sheet.SetStyle(c, cd.style)
		}
	}
	a.dirty = true
	a.mode = modeNormal
	a.message = fmt.Sprintf("Sorted %s by %s", r.Start.String()+":"+r.End.String(), primary)
	if secondary != "" {
		a.message += fmt.Sprintf(" then %s", secondary)
	}
}

func parseNumeric(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	// Remove locale commas? For sort we try plain parse
	clean := strings.ReplaceAll(s, ",", "")
	if v, err := strconv.ParseFloat(clean, 64); err == nil {
		return v, true
	}
	return 0, false
}

func normalizeForSort(s string) string {
	// NFD decompose, strip Mn, lower
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range norm.NFD.String(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

func isAccentedRune(r rune) bool {
	base := normalizeForSort(string(r))
	if base == "" {
		return false
	}
	br := []rune(base)[0]
	return unicode.ToLower(r) != br
}

func compareLatinStrings(a, b string) int {
	na := normalizeForSort(a)
	nb := normalizeForSort(b)
	if na < nb {
		return -1
	}
	if na > nb {
		return 1
	}
	// Primary equal (case/accent insensitive): tie-breaker capital first, then accent/plain, then codepoint
	ra := []rune(a)
	rb := []rune(b)
	n := len(ra)
	if len(rb) < n {
		n = len(rb)
	}
	for i := 0; i < n; i++ {
		if ra[i] == rb[i] {
			continue
		}
		la := unicode.ToLower(ra[i])
		lb := unicode.ToLower(rb[i])
		// capital first regardless of accent
		ua := unicode.IsUpper(ra[i])
		ub := unicode.IsUpper(rb[i])
		if ua != ub {
			if ua {
				return -1
			}
			return 1
		}
		// plain before accented
		accA := isAccentedRune(ra[i])
		accB := isAccentedRune(rb[i])
		if accA != accB {
			if !accA {
				return -1
			}
			return 1
		}
		if la < lb {
			return -1
		}
		if la > lb {
			return 1
		}
		if ra[i] < rb[i] {
			return -1
		}
		return 1
	}
	if len(ra) < len(rb) {
		return -1
	}
	if len(ra) > len(rb) {
		return 1
	}
	return 0
}

func compareSortKey(aDisp string, aNum float64, aIsNum bool, bDisp string, bNum float64, bIsNum bool) int {
	if aIsNum && bIsNum {
		if aNum < bNum {
			return -1
		}
		if aNum > bNum {
			return 1
		}
		return 0
	}
	if aIsNum && !bIsNum {
		return -1 // numbers before text?
	}
	if !aIsNum && bIsNum {
		return 1
	}
	return compareLatinStrings(aDisp, bDisp)
}

func readZipFileBytes(f *zip.File) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func (a *app) attemptCopyMove(isMove bool) {
	rows := a.copySrcRange.End.Row - a.copySrcRange.Start.Row + 1
	cols := a.copySrcRange.End.Col - a.copySrcRange.Start.Col + 1
	dest := a.active
	destEnd := sheet.Coord{Row: dest.Row + rows - 1, Col: dest.Col + cols - 1}
	if destEnd.Row >= sheet.MaxRows || destEnd.Col >= sheet.MaxColumns {
		a.message = fmt.Sprintf("%s: destination out of bounds (%s)", a.copyOp, dest.String()+":"+destEnd.String())
		return
	}
	// Check overwrite and merges overlapping dest
	destSheet := a.sheets[a.activeSheet]
	overwriteCount := 0
	for r := dest.Row; r <= destEnd.Row; r++ {
		for c := dest.Col; c <= destEnd.Col; c++ {
			coord := sheet.Coord{Row: r, Col: c}
			if destSheet.Raw(coord) != "" || !isDefaultStyle(destSheet.Style(coord)) {
				overwriteCount++
			}
		}
	}
	// Check merge overlap: if any merge intersects dest, treat as overwrite
	for _, m := range destSheet.Merges() {
		if rangesOverlap(m, sheet.Range{Start: dest, End: destEnd}) {
			overwriteCount++
			break
		}
	}
	if overwriteCount > 0 {
		a.pendingCopySrc = a.copySrcRange
		a.pendingCopyDest = dest
		a.pendingCopyDestSheet = a.activeSheet
		a.pendingCopyOp = a.copyOp
		a.mode = modeConfirmOverwrite
		a.message = fmt.Sprintf("Overwrite %d cells at %s? (Y/N)", overwriteCount, dest.String()+":"+destEnd.String())
		return
	}
	a.pendingCopySrc = a.copySrcRange
	a.pendingCopyDest = dest
	a.pendingCopyDestSheet = a.activeSheet
	a.pendingCopyOp = a.copyOp
	a.doCopyMove(isMove)
}

func isDefaultStyle(s sheet.Style) bool { return s.IsDefault() }

func rangesOverlap(a, b sheet.Range) bool {
	return a.Start.Row <= b.End.Row && a.End.Row >= b.Start.Row && a.Start.Col <= b.End.Col && a.End.Col >= b.Start.Col
}

func (a *app) doCopyMove(isMove bool) {
	srcRange := a.pendingCopySrc
	srcSheetIdx := a.copySrcSheet
	dest := a.pendingCopyDest
	destSheetIdx := a.pendingCopyDestSheet
	op := a.pendingCopyOp
	if op == "" {
		op = a.copyOp
	}
	srcSheet := a.sheets[srcSheetIdx]
	destSheet := a.sheets[destSheetIdx]
	rows := srcRange.End.Row - srcRange.Start.Row + 1
	cols := srcRange.End.Col - srcRange.Start.Col + 1
	dr := dest.Row - srcRange.Start.Row
	dc := dest.Col - srcRange.Start.Col
	// Snapshot for undo (all sheets snapshot is single sheet snapshot in this app; we snapshot active dest)
	// Use takeSnapshot which snapshots current sheet; for cross-sheet we snapshot both by pushing two undos? Simplify: snapshot dest and if different snapshot src too.
	curSheet := a.activeSheet
	a.activeSheet = destSheetIdx
	a.sheet = destSheet
	a.takeSnapshot(op + " " + srcRange.Start.String() + ":" + srcRange.End.String() + " -> " + dest.String())
	if srcSheetIdx != destSheetIdx {
		// Also snapshot source for Move
		if isMove {
			a.activeSheet = srcSheetIdx
			a.sheet = srcSheet
			a.takeSnapshot(op + " source clear")
			a.activeSheet = destSheetIdx
			a.sheet = destSheet
		}
	}
	// Collect src cells (to handle overlapping copy within same sheet)
	type cellData struct {
		raw   string
		style sheet.Style
	}
	srcData := make(map[sheet.Coord]cellData)
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			sc := sheet.Coord{Row: srcRange.Start.Row + r, Col: srcRange.Start.Col + c}
			srcData[sc] = cellData{raw: srcSheet.Raw(sc), style: srcSheet.Style(sc)}
		}
	}
	// Collect merges that are fully inside source
	var srcMerges []sheet.Range
	for _, m := range srcSheet.Merges() {
		if m.Start.Row >= srcRange.Start.Row && m.End.Row <= srcRange.End.Row && m.Start.Col >= srcRange.Start.Col && m.End.Col <= srcRange.End.Col {
			srcMerges = append(srcMerges, m)
		}
	}
	// Clear dest merges overlapping? remove merges overlapping dest range first
	for _, m := range destSheet.Merges() {
		if rangesOverlap(m, sheet.Range{Start: dest, End: sheet.Coord{Row: dest.Row + rows - 1, Col: dest.Col + cols - 1}}) {
			destSheet.Unmerge(m.Start)
		}
	}
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			sc := sheet.Coord{Row: srcRange.Start.Row + r, Col: srcRange.Start.Col + c}
			dc2 := sheet.Coord{Row: dest.Row + r, Col: dest.Col + c}
			cd := srcData[sc]
			raw := cd.raw
			if strings.HasPrefix(raw, "=") {
				if isMove {
					// Move retains original refs; global ref update handles refs to moved block
				} else {
					raw = "=" + adjustFormulaForCopy(raw[1:], dr, dc)
				}
			}
			destSheet.Set(dc2, raw)
			destSheet.SetStyle(dc2, cd.style)
		}
	}
	// Re-create merges at destination
	for _, m := range srcMerges {
		nr := sheet.Range{
			Start: sheet.Coord{Row: m.Start.Row + dr, Col: m.Start.Col + dc},
			End:   sheet.Coord{Row: m.End.Row + dr, Col: m.End.Col + dc},
		}
		destSheet.AddMerge(nr)
	}
	if isMove {
		// Update references to moved block in all formulas (including moved block's internal refs)
		srcName := ""
		destName := ""
		if srcSheetIdx < len(a.sheetNames) {
			srcName = a.sheetNames[srcSheetIdx]
		}
		if destSheetIdx < len(a.sheetNames) {
			destName = a.sheetNames[destSheetIdx]
		}
		for si, sh := range a.sheets {
			formulas := sh.Formulas()
			for coord, f := range formulas {
				if si == srcSheetIdx && coord.Row >= srcRange.Start.Row && coord.Row <= srcRange.End.Row && coord.Col >= srcRange.Start.Col && coord.Col <= srcRange.End.Col {
					continue
				}
				body := strings.TrimPrefix(f, "=")
				newBody := adjustFormulaForMoveRefs(body, srcRange, dr, dc, srcName, destName)
				if newBody != body {
					sh.Set(coord, "="+newBody)
				}
			}
		}
		// Clear source cells and styles and merges
		for r := 0; r < rows; r++ {
			for c := 0; c < cols; c++ {
				sc := sheet.Coord{Row: srcRange.Start.Row + r, Col: srcRange.Start.Col + c}
				srcSheet.Set(sc, "")
				srcSheet.SetStyle(sc, sheet.Style{})
			}
		}
		for _, m := range srcMerges {
			srcSheet.Unmerge(m.Start)
		}
	}
	a.dirty = true
	a.copyOp = ""
	a.pendingCopyOp = ""
	a.mode = modeNormal
	// Keep active on dest sheet at dest
	a.activeSheet = destSheetIdx
	a.sheet = destSheet
	a.active = dest
	a.ensureVisible()
	if isMove {
		a.message = fmt.Sprintf("Moved %s -> %s", srcRange.Start.String()+":"+srcRange.End.String(), dest.String())
	} else {
		a.message = fmt.Sprintf("Copied %s -> %s", srcRange.Start.String()+":"+srcRange.End.String(), dest.String())
	}
	_ = curSheet
}

func adjustFormulaForCopy(formula string, dr, dc int) string {
	// Adjust relative refs like xlsx.adjustFormula: preserve $ for absolute
	var out strings.Builder
	i := 0
	inStr := false
	for i < len(formula) {
		ch := formula[i]
		if ch == '"' {
			out.WriteByte(ch)
			i++
			inStr = !inStr
			for inStr && i < len(formula) {
				out.WriteByte(formula[i])
				if formula[i] == '"' {
					// handle escaped ""
					if i+1 < len(formula) && formula[i+1] == '"' {
						out.WriteByte(formula[i+1])
						i += 2
						continue
					}
					inStr = false
					i++
					break
				}
				i++
			}
			continue
		}
		if !inStr && (ch == '$' || (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z')) {
			// Try parse cell ref: [$]Col[$]Row
			start := i
			colAbs := false
			if formula[i] == '$' {
				colAbs = true
				i++
				if i >= len(formula) || !isColChar(formula[i]) {
					out.WriteString(formula[start:i])
					continue
				}
			}
			colStart := i
			for i < len(formula) && isColChar(formula[i]) {
				i++
			}
			if colStart == i {
				out.WriteString(formula[start:i])
				continue
			}
			colStr := formula[colStart:i]
			// Must have row part
			rowAbs := false
			if i < len(formula) && formula[i] == '$' {
				rowAbs = true
				i++
			}
			rowStart := i
			for i < len(formula) && formula[i] >= '0' && formula[i] <= '9' {
				i++
			}
			if rowStart == i {
				// No row digits -> not a cell ref (maybe range col only)
				out.WriteString(formula[start:i])
				continue
			}
			rowStr := formula[rowStart:i]
			// Valid cell ref — on parse failure preserve original text
			// (e.g. locale text that looks like a ref).
			col, err1 := sheet.ParseColumn(colStr)
			row, err2 := strconv.Atoi(rowStr)
			if err1 != nil || err2 != nil {
				out.WriteString(formula[start:i])
				continue
			}
			rowIdx := row - 1
			if !colAbs {
				col += dc
				if col < 0 {
					col = 0
				}
				if col >= sheet.MaxColumns {
					col = sheet.MaxColumns - 1
				}
			}
			if !rowAbs {
				rowIdx += dr
				if rowIdx < 0 {
					rowIdx = 0
				}
				if rowIdx >= sheet.MaxRows {
					rowIdx = sheet.MaxRows - 1
				}
			}
			if colAbs {
				out.WriteByte('$')
			}
			out.WriteString(sheet.ColumnName(col))
			if rowAbs {
				out.WriteByte('$')
			}
			out.WriteString(strconv.Itoa(rowIdx + 1))
			continue
		}
		out.WriteByte(ch)
		i++
	}
	return out.String()
}

func isColChar(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func adjustFormulaForMoveRefs(formula string, src sheet.Range, dr, dc int, srcName, destName string) string {
	// Adjust refs that fall inside src to new location (ignoring $), handling optional Sheet! prefix
	var out strings.Builder
	i := 0
	inStr := false
	for i < len(formula) {
		ch := formula[i]
		if ch == '"' {
			out.WriteByte(ch)
			i++
			inStr = !inStr
			for inStr && i < len(formula) {
				out.WriteByte(formula[i])
				if formula[i] == '"' {
					if i+1 < len(formula) && formula[i+1] == '"' {
						out.WriteByte(formula[i+1])
						i += 2
						continue
					}
					inStr = false
					i++
					break
				}
				i++
			}
			continue
		}
		if !inStr {
			// Try to detect sheet-qualified ref: SheetName! or 'Sheet Name'! [$]Col[$]Row
			sheetPrefix := ""
			if formula[i] == '\'' {
				j := i + 1
				for j < len(formula) {
					if formula[j] == '\'' {
						if j+1 < len(formula) && formula[j+1] == '\'' {
							j += 2
							continue
						}
						j++
						break
					}
					j++
				}
				if j < len(formula) && formula[j] == '!' && j > i+1 && formula[j-1] == '\'' {
					candidate := formula[i+1 : j-1]
					candidate = strings.ReplaceAll(candidate, "''", "'")
					k := j + 1
					if k < len(formula) && (formula[k] == '$' || isColChar(formula[k])) {
						sheetPrefix = candidate
						i = j + 1
					}
				}
			}
			if sheetPrefix == "" {
				j := i
				for j < len(formula) && (isSheetChar(formula[j])) {
					j++
				}
				if j < len(formula) && formula[j] == '!' && j > i {
					candidate := formula[i:j]
					k := j + 1
					if k < len(formula) && (formula[k] == '$' || isColChar(formula[k])) {
						sheetPrefix = candidate
						i = j + 1
					} else {
						out.WriteByte(ch)
						i++
						continue
					}
				}
			} else {
				// already consumed quoted prefix, continue to cell parse
			}
			if i < len(formula) && (formula[i] == '$' || isColChar(formula[i])) {
				start := i
				colAbs := false
				if formula[i] == '$' {
					colAbs = true
					i++
					if i >= len(formula) || !isColChar(formula[i]) {
						// rollback sheet prefix if any
						if sheetPrefix != "" {
							out.WriteString(sheetPrefix)
							out.WriteByte('!')
						}
						out.WriteString(formula[start:i])
						continue
					}
				}
				colStart := i
				for i < len(formula) && isColChar(formula[i]) {
					i++
				}
				if colStart == i {
					if sheetPrefix != "" {
						out.WriteString(sheetPrefix)
						out.WriteByte('!')
					}
					out.WriteString(formula[start:i])
					continue
				}
				colStr := formula[colStart:i]
				rowAbs := false
				if i < len(formula) && formula[i] == '$' {
					rowAbs = true
					i++
				}
				rowStart := i
				for i < len(formula) && formula[i] >= '0' && formula[i] <= '9' {
					i++
				}
				if rowStart == i {
					if sheetPrefix != "" {
						out.WriteString(sheet.QuoteSheetName(sheetPrefix))
						out.WriteByte('!')
					}
					out.WriteString(formula[start:i])
					continue
				}
				rowStr := formula[rowStart:i]
				col, err1 := sheet.ParseColumn(strings.ToUpper(colStr))
				row, err2 := strconv.Atoi(rowStr)
				if err1 != nil || err2 != nil {
					if sheetPrefix != "" {
						out.WriteString(sheet.QuoteSheetName(sheetPrefix))
						out.WriteByte('!')
					}
					out.WriteString(formula[start:i])
					continue
				}
				rowIdx := row - 1
				// Determine if this ref should be remapped: it refers to src sheet and coord inside srcRange
				// For unqualified refs (sheetPrefix==""), it refers to formula's sheet; we assume caller already filtered to relevant sheets, but we check: if unqualified we treat as candidate for remap (since update loop already iterates per sheet). For qualified, check prefix equals srcName.
				shouldMap := false
				if sheetPrefix == "" {
					// Unqualified: only map if this formula's sheet is src's sheet? Caller ensures we only treat relevant sheets via filtering elsewhere, but we still need check coord inside src
					shouldMap = true
				} else {
					if strings.EqualFold(sheetPrefix, srcName) {
						shouldMap = true
					} else {
						shouldMap = false
					}
				}
				if shouldMap && col >= src.Start.Col && col <= src.End.Col && rowIdx >= src.Start.Row && rowIdx <= src.End.Row {
					// Remap to dest: new = old + delta, preserve $ but adjust coord regardless of $
					newCol := col + dc
					newRow := rowIdx + dr
					if newCol < 0 {
						newCol = 0
					}
					if newCol >= sheet.MaxColumns {
						newCol = sheet.MaxColumns - 1
					}
					if newRow < 0 {
						newRow = 0
					}
					if newRow >= sheet.MaxRows {
						newRow = sheet.MaxRows - 1
					}
					// Handle sheet prefix for cross-sheet move
					if sheetPrefix != "" {
						if !strings.EqualFold(srcName, destName) {
							out.WriteString(sheet.QuoteSheetName(destName))
							out.WriteByte('!')
						} else {
							out.WriteString(sheet.QuoteSheetName(sheetPrefix))
							out.WriteByte('!')
						}
					} else if !strings.EqualFold(srcName, destName) {
						// Unqualified ref that moves to a different sheet needs
						// the dest prefix: if src != dest, emit destName! for
						// unqualified refs that are remapped.
						if srcName != "" && destName != "" && !strings.EqualFold(srcName, destName) {
							// If formula was on src sheet, its unqualified ref now points to dest sheet
							out.WriteString(sheet.QuoteSheetName(destName))
							out.WriteByte('!')
						}
					}
					if colAbs {
						out.WriteByte('$')
					}
					out.WriteString(sheet.ColumnName(newCol))
					if rowAbs {
						out.WriteByte('$')
					}
					out.WriteString(strconv.Itoa(newRow + 1))
					continue
				}
				// Fallback: just write original with prefix preservation
				if sheetPrefix != "" {
					out.WriteString(sheet.QuoteSheetName(sheetPrefix))
					out.WriteByte('!')
				}
				if colAbs {
					out.WriteByte('$')
				}
				out.WriteString(colStr)
				if rowAbs {
					out.WriteByte('$')
				}
				out.WriteString(rowStr)
				continue
			}
			if sheetPrefix != "" {
				// We consumed sheet prefix but not a cell ref, emit it
				out.WriteString(sheet.QuoteSheetName(sheetPrefix))
				out.WriteByte('!')
				continue
			}
		}
		out.WriteByte(ch)
		i++
	}
	return out.String()
}

func isSheetChar(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_' || b == '.'
}

func (a *app) applyRangePending() {
	r := a.rangeBounds()
	path := a.rangePending
	// All Range actions are undoable
	a.takeSnapshot(path)
	switch path {
	case "Range/Format/General":
		a.applyRangeFormat(r, "")
		a.applyRangeAlignment(r, sheet.AlignGeneral)
	case "Range/Format/Number":
		a.applyRangeFormat(r, a.localeNumberFormat())
		a.applyRangeAlignment(r, sheet.AlignRight)
	case "Range/Format/Percentage":
		a.applyRangeFormat(r, "0.00%")
		a.applyRangeAlignment(r, sheet.AlignRight)
	case "Range/Format/Currency":
		a.applyRangeFormat(r, a.localeCurrencyFormat())
		a.applyRangeAlignment(r, sheet.AlignRight)
	case "Range/Format/Date":
		a.applyRangeFormat(r, "yyyy-mm-dd")
		a.applyRangeAlignment(r, sheet.AlignRight)
	case "Range/Format/Text":
		a.applyRangeFormat(r, "@")
		a.applyRangeAlignment(r, sheet.AlignGeneral)
	case "Range/Format/Bold":
		a.applyRangeBold(r)
	case "Range/Format/Italic":
		a.applyRangeItalic(r)
	case "Range/Format/Underline":
		a.applyRangeUnderline(r)
	case "Range/Format/Strikethrough":
		a.applyRangeStrikethrough(r)
	case "Range/Erase/All":
		a.eraseRange(r, true, true)
	case "Range/Erase/Content":
		a.eraseRange(r, true, false)
	case "Range/Erase/Style":
		a.eraseRange(r, false, true)
	case "Range/Alignment/Left":
		a.applyRangeAlignment(r, sheet.AlignLeft)
	case "Range/Alignment/Right":
		a.applyRangeAlignment(r, sheet.AlignRight)
	case "Range/Alignment/Centre":
		a.applyRangeAlignment(r, sheet.AlignCenter)
	default:
		a.message = path + " (no action)"
		return
	}
	a.dirty = true
	a.message = path + " applied to " + r.Start.String() + ":" + r.End.String()
}

func (a *app) applyRangeFormat(r sheet.Range, fmt string) {
	for row := r.Start.Row; row <= r.End.Row; row++ {
		for col := r.Start.Col; col <= r.End.Col; col++ {
			c := sheet.Coord{Row: row, Col: col}
			st := a.sheet.Style(c)
			st.NumFmt = fmt
			a.sheet.SetStyle(c, st)
		}
	}
}

func (a *app) applyRangeAlignment(r sheet.Range, align sheet.Alignment) {
	for row := r.Start.Row; row <= r.End.Row; row++ {
		for col := r.Start.Col; col <= r.End.Col; col++ {
			c := sheet.Coord{Row: row, Col: col}
			st := a.sheet.Style(c)
			st.Align = align
			a.sheet.SetStyle(c, st)
		}
	}
}

func (a *app) applyRangeBoolToggle(r sheet.Range, get func(sheet.Style) bool, set func(*sheet.Style, bool)) {
	all := true
	for row := r.Start.Row; row <= r.End.Row; row++ {
		for col := r.Start.Col; col <= r.End.Col; col++ {
			if !get(a.sheet.Style(sheet.Coord{Row: row, Col: col})) {
				all = false
				break
			}
		}
		if !all {
			break
		}
	}
	newVal := !all
	for row := r.Start.Row; row <= r.End.Row; row++ {
		for col := r.Start.Col; col <= r.End.Col; col++ {
			c := sheet.Coord{Row: row, Col: col}
			st := a.sheet.Style(c)
			set(&st, newVal)
			a.sheet.SetStyle(c, st)
		}
	}
}

func (a *app) applyRangeBold(r sheet.Range) {
	a.applyRangeBoolToggle(r, func(s sheet.Style) bool { return s.Bold }, func(s *sheet.Style, v bool) { s.Bold = v })
}

func (a *app) applyRangeItalic(r sheet.Range) {
	a.applyRangeBoolToggle(r, func(s sheet.Style) bool { return s.Italic }, func(s *sheet.Style, v bool) { s.Italic = v })
}

func (a *app) applyRangeUnderline(r sheet.Range) {
	a.applyRangeBoolToggle(r, func(s sheet.Style) bool { return s.Underline }, func(s *sheet.Style, v bool) { s.Underline = v })
}

func (a *app) applyRangeStrikethrough(r sheet.Range) {
	a.applyRangeBoolToggle(r, func(s sheet.Style) bool { return s.Strikethrough }, func(s *sheet.Style, v bool) { s.Strikethrough = v })
}

func (a *app) eraseRange(r sheet.Range, content bool, style bool) {
	for row := r.Start.Row; row <= r.End.Row; row++ {
		for col := r.Start.Col; col <= r.End.Col; col++ {
			c := sheet.Coord{Row: row, Col: col}
			if content && style {
				a.sheet.Set(c, "")
			} else if content && !style {
				st := a.sheet.Style(c)
				a.sheet.Set(c, "")
				if !st.IsDefault() {
					a.sheet.SetStyle(c, st)
				}
			} else if !content && style {
				a.sheet.SetStyle(c, sheet.Style{})
			}
		}
	}
}

// Locale helpers for Number/Currency
func (a *app) localeNumberFormat() string {
	// Stored format is locale-independent; rendering will be locale-aware via applyNumberFormat
	return "#,##0.00"
}

func (a *app) localeCurrencyFormat() string {
	_, sym := a.localeInfo()
	// Use quoted symbol + number pattern; normalizeFormat will keep symbol
	if sym == "" {
		sym = "$"
	}
	// For Euro locales, put symbol after with space per common convention
	dec, _ := a.localeSeparators()
	// Heuristic: if decimal is ',' (European), put symbol after
	if dec == "," {
		return "#,##0.00 \"" + sym + "\""
	}
	return "\"" + sym + "\"#,##0.00"
}

func (a *app) localeSeparators() (string, string) { return locale.Separators() }

func (a *app) localeInfo() (string, string) { return locale.Currency() }

func (a *app) localeTag() string { return locale.Tag() }

func (a *app) deleteSheet(index int) {
	if len(a.sheets) <= 1 {
		a.message = "Cannot delete the last sheet"
		return
	}
	deletedName := ""
	if index >= 0 && index < len(a.sheetNames) {
		deletedName = a.sheetNames[index]
	}
	a.takeSnapshot("Delete sheet")
	a.dirty = true
	a.saveLiveState()
	a.sheets = append(a.sheets[:index], a.sheets[index+1:]...)
	a.sheetNames = append(a.sheetNames[:index], a.sheetNames[index+1:]...)
	if a.activeSheet >= len(a.sheets) {
		a.activeSheet = len(a.sheets) - 1
	} else if index < a.activeSheet {
		a.activeSheet--
	}
	a.sheet = a.sheets[a.activeSheet]
	delete(a.cursors, deletedName)
	delete(a.scrolls, deletedName)
	a.restoreActiveState()
	a.mode = modeNormal
	a.message = "Deleted sheet"
}

func (a *app) handleEnter() {
	switch a.mode {
	case modeEdit:
		if strings.TrimSpace(a.edit) == "" {
			a.takeSnapshot("Clear " + a.active.String())
		} else {
			a.takeSnapshot("Edit " + a.active.String())
		}
		a.sheet.SetUserInput(a.active, a.edit)
		a.mode = modeNormal
		a.message = "Stored " + a.active.String()
	default:
		a.mode = modeEdit
		a.edit = a.sheet.EditableText(a.active)
		a.editCursor = utf8.RuneCountInString(a.edit)
	}
}

func (a *app) handleConfirmSaveOverwriteKey(msg tea.KeyMsg) tea.Cmd {
	switch strings.ToLower(msg.String()) {
	case "y", "enter":
		filename := a.pendingSaveFile
		if filename == "" {
			filename = strings.TrimSpace(a.saveInput)
		}
		if filename == "" {
			a.mode = modeSave
			return nil
		}
		if err := a.saveXLSX(filename); err != nil {
			a.message = "Save failed: " + err.Error()
			a.mode = modeSave
			return nil
		}
		a.currentFile = filename
		a.saveInput = ""
		a.saveCursor = 0
		a.pendingSaveFile = ""
		a.message = "Saved " + filename
		action := a.pendingAction
		a.pendingAction = actionNone
		a.mode = modeNormal
		switch action {
		case actionQuit:
			return tea.Quit
		case actionRetrieve:
			a.enterOpenFileMode()
		}
	case "n", "esc":
		a.mode = modeSave
		a.pendingSaveFile = ""
		a.message = "Change name or Esc to cancel"
	}
	return nil
}

func ensureXlsxExt(filename string) string {
	if !strings.HasSuffix(strings.ToLower(filename), ".xlsx") {
		return filename + ".xlsx"
	}
	return filename
}

func (a *app) saveXLSX(filename string) error {
	var props *xlsx.DocProps
	if a.docPropsDirty {
		pp := a.docProps
		props = &pp
	} else if a.currentFile == "" {
		// New file without edits still needs Application/AppVersion stamp
		pp := a.docProps
		props = &pp
	} else {
		// Check if original has no docProps -> need stamp even without edits
		// Detect by checking original file's docProps existence
		hasProps := false
		if a.currentFile != "" {
			if _, ok := xlsx.LoadDocProps(a.currentFile); ok {
				hasProps = true
			}
		}
		if !hasProps {
			pp := a.docProps
			props = &pp
		}
	}
	var err error
	if props != nil {
		err = xlsx.SaveWithProps(filename, a.currentFile, a.sheets, a.sheetNames, props)
	} else {
		err = xlsx.Save(filename, a.currentFile, a.sheets, a.sheetNames)
	}
	if err == nil {
		a.dirty = false
		if props != nil {
			a.docPropsLoaded = true
			a.docPropsDirty = false
		}
		if filename != a.currentFile {
			if p, ok := xlsx.LoadDocProps(filename); ok {
				a.docProps = p
				a.docPropsLoaded = true
			}
		}
	}
	return err
}

func (a *app) exitOpenFile() {
	a.mode = modeNormal
	a.fileInput = ""
	a.fileInputCursor = 0
	a.fileEntries = nil
}

func (a *app) handleConfirmUnsavedKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "s", "S":
		if a.currentFile != "" {
			if err := a.saveXLSX(a.currentFile); err != nil {
				a.message = "Save failed: " + err.Error()
				return nil
			}
			action := a.pendingAction
			a.pendingAction = actionNone
			switch action {
			case actionQuit:
				return tea.Quit
			case actionRetrieve:
				a.enterOpenFileMode()
			default:
				a.mode = modeNormal
				a.message = "Saved " + a.currentFile
			}
		} else {
			a.mode = modeSave
			a.saveInput = ""
			a.message = "Save .xlsx path: "
		}
	case "q", "Q":
		a.dirty = false
		a.message = "Changes discarded"
		action := a.pendingAction
		a.pendingAction = actionNone
		a.mode = modeNormal
		switch action {
		case actionQuit:
			return tea.Quit
		case actionRetrieve:
			a.enterOpenFileMode()
		}
	case "esc":
		a.pendingAction = actionNone
		a.mode = modeNormal
		a.message = "Cancelled"
	}
	return nil
}

func caretInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (a *app) addSheetAt(x int) bool {
	if x >= 0 && x < 4 {
		a.addSheet()
		return true
	}
	return false
}

func (a *app) sheetIndexAt(x int) (int, bool) {
	col := 4
	if a.tabOffset > 0 {
		col += 4
	}
	for i := a.tabOffset; i < len(a.sheetNames); i++ {
		col += 3
		width := runewidth.StringWidth(a.sheetNames[i]) + 2
		if x >= col && x < col+width {
			return i, true
		}
		col += width
	}
	return 0, false
}

func (a *app) withSortPopup(lines []string) []string {
	title := fmt.Sprintf(" Sort Options - Range: %s ", a.sortRange.Start.String()+":"+a.sortRange.End.String())
	boxW := max(48, a.width*7/10)
	if boxW > a.width-2 {
		boxW = a.width - 2
	}
	boxX := (a.width - boxW) / 2
	innerW := boxW - 2
	// Build content lines
	var content []string
	content = append(content, style(sgrReverseVideo, fitDisplay(title, innerW)))
	content = append(content, "")
	content = append(content, " Primary key:")
	// Primary column field
	primaryField := fmt.Sprintf("   Column: [ %-3s ]", a.sortPrimaryCol)
	if a.sortFocus == 0 {
		primaryField = style(sgrTealBand, fitDisplay(primaryField, innerW))
	}
	content = append(content, primaryField)
	primaryAsc := "   Order:  ( ) Ascending   ( ) Descending"
	if a.sortPrimaryAsc {
		primaryAsc = "   Order:  (•) Ascending   ( ) Descending"
	} else {
		primaryAsc = "   Order:  ( ) Ascending   (•) Descending"
	}
	if a.sortFocus == 1 {
		primaryAsc = style(sgrTealBand, fitDisplay(primaryAsc, innerW))
	}
	content = append(content, primaryAsc)
	content = append(content, "")
	content = append(content, " Secondary key (optional):")
	secondaryField := fmt.Sprintf("   Column: [ %-3s ]", a.sortSecondaryCol)
	if a.sortFocus == 2 {
		secondaryField = style(sgrTealBand, fitDisplay(secondaryField, innerW))
	}
	content = append(content, secondaryField)
	secondaryAsc := "   Order:  ( ) Ascending   ( ) Descending"
	if a.sortSecondaryAsc {
		secondaryAsc = "   Order:  (•) Ascending   ( ) Descending"
	} else {
		secondaryAsc = "   Order:  ( ) Ascending   (•) Descending"
	}
	if a.sortFocus == 3 {
		secondaryAsc = style(sgrTealBand, fitDisplay(secondaryAsc, innerW))
	}
	content = append(content, secondaryAsc)
	content = append(content, "")
	content = append(content, " [Enter] Sort   [Esc] Cancel")
	content = append(content, " [Tab/Arrows] field  [Space] toggle")
	content = append(content, "")
	content = append(content, style(sgrItalic, " Tip: leave Secondary empty for single-key sort"))
	// Render box
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
	return lines
}

func (a *app) withSheetMovePopup(lines []string) []string {
	title := " Move Sheet — Up/Down moves, Enter confirms, Esc cancels "
	boxW := max(36, min(50, a.width-4))
	if boxW > a.width-2 {
		boxW = a.width - 2
	}
	boxX := (a.width - boxW) / 2
	innerW := boxW - 2
	var content []string
	content = append(content, style(sgrReverseVideo, fitDisplay(title, innerW)))
	content = append(content, "")
	for i, name := range a.sheetNames {
		line := "  " + name + "  "
		if i == a.sheetMoveIdx {
			line = style(sgrTealBand, fitDisplay("▶ "+name, innerW))
		} else {
			line = fitDisplay("  "+name, innerW)
		}
		content = append(content, line)
	}
	content = append(content, "")
	content = append(content, " [Enter] Confirm   [Esc] Cancel ")
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
	return lines
}

// withUndoRedoHelpPopup shows the undo-redo specific help reached from
// the Undo-redo menu.
