package sheet

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// ---------- Table definitions ----------

type TableColumnDef struct {
	Name string
}

type TableDef struct {
	Name    string
	Ref     Range
	Columns []TableColumnDef
}

func (t *TableDef) ColumnIndex(colName string) int {
	for i, c := range t.Columns {
		if strings.EqualFold(c.Name, colName) {
			return t.Ref.Start.Col + i
		}
	}
	return -1
}

// ---------- Sheet ----------

type Sheet struct {
	cells        map[Coord]string
	formulas     map[Coord]string
	styles       map[Coord]Style
	merges       []Range
	tables       []TableDef
	hiddenCols   map[int]bool
	hiddenRows   map[int]bool
	colWidths    map[int]int
	colStyles    map[int]Style
	freezeRow    int // ySplit: rows frozen (0 = unfrozen)
	freezeCol    int // xSplit: cols frozen
	cachedMaxRow int
	cacheValid   bool
	mergeMap     map[Coord]Range // per-cell merge index, rebuilt on mutation

	formulaGen      int
	formulaCacheGen map[Coord]int // valid when == formulaGen

	// resolveSheet maps a sheet name from a cross-sheet reference to its
	// Sheet; nil until a workbook wires it up.
	resolveSheet func(name string) *Sheet
	resolveTable func(name string) *TableDef

	// definedNames holds workbook-global defined names (lowercased) → raw
	// content as found in workbook.xml, e.g. "'Lookups'!$E$2:$E$11" or
	// "123456.789" or "=TODAY()".
	definedNames map[string]string
}

// SetSheetResolver installs the lookup used to resolve 'Other Sheet'!A1
// style references. Pass nil to clear it.
func (s *Sheet) SetSheetResolver(fn func(name string) *Sheet) {
	s.resolveSheet = fn
}

func (s *Sheet) SetTableResolver(fn func(name string) *TableDef) {
	s.resolveTable = fn
}

// SetDefinedNames installs the workbook-global defined-name map (name is
// matched case-insensitively). The map is copied.
func (s *Sheet) SetDefinedNames(m map[string]string) {
	if len(m) == 0 {
		s.definedNames = nil
		return
	}
	cp := make(map[string]string, len(m))
	for k, v := range m {
		cp[strings.ToLower(k)] = v
	}
	s.definedNames = cp
}

func (s *Sheet) lookupDefinedName(name string) (string, bool) {
	if s.definedNames == nil {
		return "", false
	}
	v, ok := s.definedNames[strings.ToLower(name)]
	return v, ok
}

// resolveTarget looks up another sheet by name via the installed resolver,
// which is expected to match case-insensitively.
func (s *Sheet) resolveTarget(name string) *Sheet {
	if s.resolveSheet == nil {
		return nil
	}
	return s.resolveSheet(name)
}

func (s *Sheet) invalidateMaxCache() { s.cacheValid = false }

// maxUsedRow returns the last row holding any cell content or formula.
func (s *Sheet) maxUsedRow() int {
	if s.cacheValid {
		return s.cachedMaxRow
	}
	max := -1
	for c := range s.cells {
		if c.Row > max {
			max = c.Row
		}
	}
	for c := range s.formulas {
		if c.Row > max {
			max = c.Row
		}
	}
	s.cachedMaxRow = max
	s.cacheValid = true
	return max
}

// MaxUsed returns the bottom-right of the used range (inclusive) including
// merges and tables. For an empty sheet it returns (-1,-1); callers should
// treat that as A1.
func (s *Sheet) MaxUsed() (int, int) {
	maxRow, maxCol := -1, -1
	for c := range s.cells {
		if c.Row > maxRow {
			maxRow = c.Row
		}
		if c.Col > maxCol {
			maxCol = c.Col
		}
	}
	for c := range s.formulas {
		if c.Row > maxRow {
			maxRow = c.Row
		}
		if c.Col > maxCol {
			maxCol = c.Col
		}
	}
	for _, m := range s.merges {
		if m.End.Row > maxRow {
			maxRow = m.End.Row
		}
		if m.End.Col > maxCol {
			maxCol = m.End.Col
		}
	}
	for _, t := range s.tables {
		if t.Ref.End.Row > maxRow {
			maxRow = t.Ref.End.Row
		}
		if t.Ref.End.Col > maxCol {
			maxCol = t.Ref.End.Col
		}
	}
	return maxRow, maxCol
}

// LastUsedInRow returns the last used column in the given row, including
// merges/tables that cover the row, or -1 if the row has no used cells.
func (s *Sheet) LastUsedInRow(row int) int {
	maxCol := -1
	for c := range s.cells {
		if c.Row == row && c.Col > maxCol {
			maxCol = c.Col
		}
	}
	for c := range s.formulas {
		if c.Row == row && c.Col > maxCol {
			maxCol = c.Col
		}
	}
	for _, m := range s.merges {
		if row >= m.Start.Row && row <= m.End.Row && m.End.Col > maxCol {
			maxCol = m.End.Col
		}
	}
	for _, t := range s.tables {
		if row >= t.Ref.Start.Row && row <= t.Ref.End.Row && t.Ref.End.Col > maxCol {
			maxCol = t.Ref.End.Col
		}
	}
	return maxCol
}

type Alignment int

const (
	AlignGeneral Alignment = iota
	AlignLeft
	AlignCenter
	AlignRight
)

type Style struct {
	Bold          bool
	Italic        bool
	Underline     bool
	Strikethrough bool
	Align         Alignment
	Wrap          bool   // wrap text (renders multiline cells in Excel/Calc)
	NumFmt        string // Excel number-format code; empty means General
}

func (s Style) IsDefault() bool {
	return s.NumFmt == "" && s.Align == AlignGeneral && !s.Wrap && !s.Bold && !s.Italic && !s.Underline && !s.Strikethrough
}

func New() *Sheet {
	return &Sheet{
		cells:      make(map[Coord]string),
		formulas:   make(map[Coord]string),
		styles:     make(map[Coord]Style),
		hiddenCols: make(map[int]bool),
		hiddenRows: make(map[int]bool),
		colWidths:  make(map[int]int),
		colStyles:  make(map[int]Style),
	}
}

func (s *Sheet) Set(c Coord, value string) {
	s.invalidateMaxCache()
	// Imported formula results are only valid for the workbook state they were
	// read from. Any cell edit may affect an arbitrary formula, so discard all
	// cached results before changing data.
	s.invalidateFormulaCaches()
	if s.formulaCacheGen != nil {
		delete(s.formulaCacheGen, c)
	}
	if strings.TrimSpace(value) == "" {
		delete(s.cells, c)
		delete(s.formulas, c)
		delete(s.styles, c)
		return
	}
	if strings.HasPrefix(strings.TrimSpace(value), "=") {
		s.formulas[c] = strings.TrimSpace(value)
		s.cells[c] = ""
	} else {
		delete(s.formulas, c)
		s.cells[c] = value
	}
}

func (s *Sheet) invalidateFormulaCaches() {
	s.formulaGen++
	if s.formulaGen == 0 {
		// overflowed — clear map to avoid false hits
		s.formulaCacheGen = nil
		s.formulaGen = 1
	}
	// Stale s.cells entries for formulas remain but are ignored via gen check.
}

// SetForImport and SetFormulaForImport are bulk-load variants of Set/SetFormula
// that skip invalidateFormulaCaches. The caller must ensure the sheet is fresh
// (no cached formula results to invalidate) or call invalidate once after the batch.
// This avoids O(n²) invalidation when streaming 50k+ cells.
func (s *Sheet) SetForImport(c Coord, value string) {
	s.invalidateMaxCache()
	if strings.TrimSpace(value) == "" {
		delete(s.cells, c)
		delete(s.formulas, c)
		delete(s.styles, c)
		return
	}
	if strings.HasPrefix(strings.TrimSpace(value), "=") {
		s.formulas[c] = strings.TrimSpace(value)
		s.cells[c] = ""
	} else {
		delete(s.formulas, c)
		s.cells[c] = value
	}
}

func (s *Sheet) SetFormulaForImport(c Coord, formula string, displayValue string) {
	s.invalidateMaxCache()
	if strings.TrimSpace(formula) == "" {
		s.SetForImport(c, displayValue)
		return
	}
	s.formulas[c] = formula
	s.cells[c] = displayValue
	if displayValue != "" {
		if s.formulaCacheGen == nil {
			s.formulaCacheGen = make(map[Coord]int, 8)
		}
		s.formulaCacheGen[c] = s.formulaGen
	} else if s.formulaCacheGen != nil {
		delete(s.formulaCacheGen, c)
	}
}

// SetFormula records a formula and its cached result. It is used when loading
// workbooks so formulas outside this evaluator can still be displayed until
// the sheet is edited.
func (s *Sheet) SetFormula(c Coord, formula string, displayValue string) {
	s.invalidateMaxCache()
	if strings.TrimSpace(formula) == "" {
		s.Set(c, displayValue)
		return
	}
	s.formulas[c] = formula
	s.cells[c] = displayValue
	if displayValue != "" {
		if s.formulaCacheGen == nil {
			s.formulaCacheGen = make(map[Coord]int, 8)
		}
		s.formulaCacheGen[c] = s.formulaGen
	} else if s.formulaCacheGen != nil {
		delete(s.formulaCacheGen, c)
	}
}

func (s *Sheet) SetStyle(c Coord, style Style) {
	if style.IsDefault() {
		delete(s.styles, c)
		return
	}
	s.styles[c] = style
}

func (s *Sheet) Raw(c Coord) string {
	if formula, ok := s.formulas[c]; ok && formula != "" {
		return formula
	}
	return s.cells[c]
}

func (s *Sheet) Style(c Coord) Style {
	if st, ok := s.styles[c]; ok {
		return st
	}
	if s.colStyles != nil {
		if st, ok := s.colStyles[c.Col]; ok {
			return st
		}
	}
	return Style{}
}

func (s *Sheet) SetColStyle(col int, style Style) {
	if col < 0 {
		return
	}
	if style.IsDefault() {
		delete(s.colStyles, col)
		return
	}
	if s.colStyles == nil {
		s.colStyles = make(map[int]Style)
	}
	s.colStyles[col] = style
}

func (s *Sheet) ColStyle(col int) Style {
	if s.colStyles == nil {
		return Style{}
	}
	return s.colStyles[col]
}

func (s *Sheet) ColStyles() map[int]Style {
	m := make(map[int]Style, len(s.colStyles))
	for c, st := range s.colStyles {
		m[c] = st
	}
	return m
}

func (s *Sheet) Cells() map[Coord]string {
	m := make(map[Coord]string, len(s.cells))
	for c, v := range s.cells {
		m[c] = v
	}
	return m
}

func (s *Sheet) Formulas() map[Coord]string {
	m := make(map[Coord]string, len(s.formulas))
	for c, f := range s.formulas {
		m[c] = f
	}
	return m
}

func (s *Sheet) Styles() map[Coord]Style {
	m := make(map[Coord]Style, len(s.styles))
	for c, st := range s.styles {
		m[c] = st
	}
	return m
}

// Len accessors avoid full-map copies when only the count is needed
// (e.g. the per-frame grid cache key in cmd/tuisheet/view.go).
func (s *Sheet) LenCells() int      { return len(s.cells) }
func (s *Sheet) LenFormulas() int   { return len(s.formulas) }
func (s *Sheet) LenStyles() int     { return len(s.styles) }
func (s *Sheet) LenHiddenCols() int { return len(s.hiddenCols) }
func (s *Sheet) LenHiddenRows() int { return len(s.hiddenRows) }
func (s *Sheet) LenColWidths() int  { return len(s.colWidths) }

func (s *Sheet) HiddenCols() map[int]bool {
	m := make(map[int]bool, len(s.hiddenCols))
	for c, v := range s.hiddenCols {
		m[c] = v
	}
	return m
}

func (s *Sheet) GetAllColWidths() map[int]int {
	m := make(map[int]int, len(s.colWidths))
	for c, w := range s.colWidths {
		m[c] = w
	}
	return m
}

func (s *Sheet) IsColHidden(col int) bool {
	return s.hiddenCols[col]
}

func (s *Sheet) HideCol(col int) {
	if col >= 0 {
		s.hiddenCols[col] = true
	}
}

func (s *Sheet) ShowCol(col int) {
	delete(s.hiddenCols, col)
}

func (s *Sheet) IsRowHidden(row int) bool {
	return s.hiddenRows[row]
}

func (s *Sheet) HideRow(row int) {
	if row >= 0 {
		s.hiddenRows[row] = true
	}
}

func (s *Sheet) ShowRow(row int) {
	delete(s.hiddenRows, row)
}

func (s *Sheet) HiddenRows() map[int]bool {
	m := make(map[int]bool, len(s.hiddenRows))
	for k, v := range s.hiddenRows {
		m[k] = v
	}
	return m
}

func (s *Sheet) SetColWidth(col int, width int) {
	if width <= 0 {
		delete(s.colWidths, col)
	} else {
		s.colWidths[col] = width
	}
}

func (s *Sheet) ResetColWidth(col int) {
	delete(s.colWidths, col)
}

func (s *Sheet) ColWidth(col int) int {
	return s.colWidths[col]
}

func FromSnapshotWithFreeze(cells map[Coord]string, formulas map[Coord]string, styles map[Coord]Style, merges []Range, tables []TableDef, hiddenCols map[int]bool, hiddenRows map[int]bool, colWidths map[int]int, colStyles map[int]Style, freezeRow, freezeCol int) *Sheet {
	c := make(map[Coord]string, len(cells))
	for k, v := range cells {
		c[k] = v
	}
	f := make(map[Coord]string, len(formulas))
	for k, v := range formulas {
		f[k] = v
	}
	st := make(map[Coord]Style, len(styles))
	for k, v := range styles {
		st[k] = v
	}
	m := append([]Range(nil), merges...)
	t := append([]TableDef(nil), tables...)
	hc := make(map[int]bool, len(hiddenCols))
	for k, v := range hiddenCols {
		hc[k] = v
	}
	hr := make(map[int]bool, len(hiddenRows))
	for k, v := range hiddenRows {
		hr[k] = v
	}
	wc := make(map[int]int, len(colWidths))
	for k, v := range colWidths {
		wc[k] = v
	}
	cs := make(map[int]Style, len(colStyles))
	for k, v := range colStyles {
		cs[k] = v
	}
	sh := &Sheet{cells: c, formulas: f, styles: st, merges: m, tables: t, hiddenCols: hc, hiddenRows: hr, colWidths: wc, colStyles: cs, freezeRow: freezeRow, freezeCol: freezeCol}
	sh.rebuildMergeMap()
	// Restore formula cache validity for cells that carry a cached display.
	if len(f) > 0 {
		sh.formulaCacheGen = make(map[Coord]int, len(f))
		for coord := range f {
			if v, ok := c[coord]; ok && v != "" {
				sh.formulaCacheGen[coord] = sh.formulaGen
			}
		}
		if len(sh.formulaCacheGen) == 0 {
			sh.formulaCacheGen = nil
		}
	}
	return sh
}

func (s *Sheet) Freeze() (int, int) { return s.freezeRow, s.freezeCol }
func (s *Sheet) IsFrozen() bool     { return s.freezeRow > 0 || s.freezeCol > 0 }
func (s *Sheet) SetFreeze(r, c int) {
	if r < 0 {
		r = 0
	}
	if c < 0 {
		c = 0
	}
	s.freezeRow = r
	s.freezeCol = c
}

func (s *Sheet) GetAllColStyles() map[int]Style {
	m := make(map[int]Style, len(s.colStyles))
	for c, st := range s.colStyles {
		m[c] = st
	}
	return m
}

func (s *Sheet) ForEachCell(fn func(c Coord, value string, formula string, style Style) bool) {
	for c := range s.cells {
		raw := s.cells[c]
		formula := s.formulas[c]
		st := s.styles[c]
		if formula != "" {
			raw = formula
		}
		if !fn(c, raw, formula, st) {
			return
		}
	}
	for c, formula := range s.formulas {
		if _, ok := s.cells[c]; !ok {
			raw := formula
			if !fn(c, raw, formula, s.styles[c]) {
				return
			}
		}
	}
}

func (s *Sheet) AddMerge(r Range) {
	if !r.Valid() || r.RowSpan() == 1 && r.ColSpan() == 1 {
		return
	}
	s.merges = append(s.merges, r)
	s.rebuildMergeMap()
}

func (s *Sheet) Merges() []Range {
	return append([]Range(nil), s.merges...)
}

func (s *Sheet) rebuildMergeMap() {
	if len(s.merges) == 0 {
		s.mergeMap = nil
		return
	}
	m := make(map[Coord]Range, len(s.merges)*4)
	for _, r := range s.merges {
		for rr := r.Start.Row; rr <= r.End.Row; rr++ {
			for cc := r.Start.Col; cc <= r.End.Col; cc++ {
				m[Coord{Row: rr, Col: cc}] = r
			}
		}
	}
	s.mergeMap = m
}

func (s *Sheet) MergeAt(c Coord) (Range, bool) {
	if s.mergeMap != nil {
		if r, ok := s.mergeMap[c]; ok {
			return r, true
		}
		return Range{}, false
	}
	for _, r := range s.merges {
		if r.Contains(c) {
			return r, true
		}
	}
	return Range{}, false
}

func (s *Sheet) CoveredByMerge(c Coord) bool {
	r, ok := s.MergeAt(c)
	return ok && c != r.Start
}

func (s *Sheet) RemoveMerge(r Range) {
	newM := s.merges[:0]
	for _, m := range s.merges {
		if m != r {
			newM = append(newM, m)
		}
	}
	s.merges = newM
	s.rebuildMergeMap()
}

func (s *Sheet) Unmerge(c Coord) {
	if r, ok := s.MergeAt(c); ok {
		s.RemoveMerge(r)
	}
}

// ---------- Table management ----------

func (s *Sheet) AddTable(t TableDef) {
	s.tables = append(s.tables, t)
}

func (s *Sheet) Tables() []TableDef {
	return append([]TableDef(nil), s.tables...)
}

func (s *Sheet) FindTable(name string) *TableDef {
	for i := range s.tables {
		if strings.EqualFold(s.tables[i].Name, name) {
			return &s.tables[i]
		}
	}
	// Structured references may refer to a table defined on another sheet
	// (e.g. Expected Results!B28 =ROWS(RawDataTable[#Data]) where
	// RawDataTable lives on "Raw Data"). Fall back to a workbook-global
	// lookup if available.
	if s.resolveTable != nil {
		if t := s.resolveTable(name); t != nil {
			return t
		}
	}
	return nil
}

// ---------- Display & Eval ----------

func (s *Sheet) Display(c Coord) string {
	if cached, ok := s.cells[c]; ok && s.formulas[c] != "" && cached != "" {
		if s.formulaCacheGen != nil && s.formulaCacheGen[c] == s.formulaGen {
			if st := s.Style(c); st.NumFmt != "" {
				if v := parseValue(cached); v.Type == valNumber {
					return SanitizeForDisplay(applyNumberFormat(v, st.NumFmt))
				}
			}
			return SanitizeForDisplay(cached)
		}
	}
	raw := s.Raw(c)
	if !strings.HasPrefix(raw, "=") {
		if strings.TrimSpace(raw) == "" {
			return ""
		}
		if st := s.Style(c); st.NumFmt != "" {
			v := parseValue(raw)
			if v.Type == valNumber {
				return SanitizeForDisplay(applyNumberFormat(v, st.NumFmt))
			}
		}
		return SanitizeForDisplay(raw)
	}
	v, err := s.Eval(c)
	if err != nil {
		if isDiv0Error(err) {
			return "#DIV/0!"
		}
		if strings.Contains(err.Error(), "#CALC!") {
			return "#CALC!"
		}
		if strings.Contains(err.Error(), "#VALUE!") {
			return "#VALUE!"
		}
		if strings.Contains(err.Error(), "#N/A") {
			return "#N/A"
		}
		return "#ERR"
	}
	var disp string
	// Cache the RAW computed value for dependent formulas — never the
	// formatted display string. Storing e.g. "1.022,00 €" makes
	// cellNode.eval parse it as text and dependents cascade #ERR.
	storeRaw := func(x Value) {
		if s.formulas[c] == "" {
			return
		}
		var cached string
		switch x.Type {
		case valNumber:
			cached = strconv.FormatFloat(x.Num, 'f', -1, 64)
			s.cells[c] = cached
		case valString:
			if x.Str != "" {
				cached = x.Str
				s.cells[c] = cached
			} else {
				return
			}
		default:
			return
		}
		if s.formulaCacheGen == nil {
			s.formulaCacheGen = make(map[Coord]int, 8)
		}
		s.formulaCacheGen[c] = s.formulaGen
	}
	storeRaw(v)
	if v.Type == valRange {
		// Top-level range (e.g. =OFFSET(...), =INDIRECT("A1:B2")).
		// Single-cell ranges dereference to the cell's value; multi-cell
		// ranges are not displayable as scalar → #VALUE! (Excel behaviour
		// is similar; SUM(OFFSET(...)) still works via aggregation).
		if v.R.Start == v.R.End {
			t := s
			if v.SheetName != "" && s.resolveSheet != nil {
				if tgt := s.resolveSheet(v.SheetName); tgt != nil {
					t = tgt
				}
			}
			cv, err := t.evaluateCell(v.R.Start, map[evalKey]bool{{s, c}: true})
			if err != nil {
				if isDiv0Error(err) {
					return "#DIV/0!"
				}
				return "#ERR"
			}
			if cv.Type == valString {
				storeRaw(cv)
				disp = SanitizeForDisplay(cv.Str)
				return disp
			}
			if st := s.Style(c); cv.Type == valNumber && st.NumFmt != "" {
				storeRaw(cv)
				disp = applyNumberFormat(cv, st.NumFmt)
				return disp
			}
			storeRaw(cv)
			disp = strconv.FormatFloat(cv.Num, 'f', -1, 64)
			return disp
		}
		disp = "#VALUE!"
		return disp
	}
	if v.Type == valArray {
		if len(v.Arr) == 1 && len(v.Arr[0]) == 1 {
			cv := v.Arr[0][0]
			if cv.Type == valString {
				storeRaw(cv)
				disp = SanitizeForDisplay(cv.Str)
				return disp
			}
			if st := s.Style(c); cv.Type == valNumber && st.NumFmt != "" {
				storeRaw(cv)
				disp = applyNumberFormat(cv, st.NumFmt)
				return disp
			}
			if cv.Type == valNumber {
				storeRaw(cv)
				disp = strconv.FormatFloat(cv.Num, 'f', -1, 64)
				return disp
			}
			disp = SanitizeForDisplay(cv.Str)
			return disp
		}
		disp = "#VALUE!"
		return disp
	}
	if v.Type == valEmpty {
		disp = ""
		return disp
	}
	if st := s.Style(c); v.Type == valNumber && st.NumFmt != "" {
		disp = applyNumberFormat(v, st.NumFmt)
		return disp
	}
	if v.Type == valString {
		disp = SanitizeForDisplay(v.Str)
		return disp
	}
	disp = strconv.FormatFloat(v.Num, 'f', -1, 64)
	return disp
}

func (s *Sheet) Eval(c Coord) (Value, error) {
	raw := strings.TrimSpace(s.Raw(c))
	if strings.TrimSpace(raw) == "" {
		return Value{Type: valEmpty}, nil
	}
	if !strings.HasPrefix(raw, "=") {
		return parseValue(raw), nil
	}
	seen := map[evalKey]bool{{s, c}: true}
	return s.evalExpr(strings.TrimSpace(raw[1:]), c, seen)
}

// EvalCached is Eval but reuses the raw value Display already computed and
// cached, avoiding a second parse+evaluation of the same formula.
func (s *Sheet) EvalCached(c Coord) (Value, error) {
	if s.formulas[c] != "" {
		if cached, ok := s.cells[c]; ok && cached != "" && !strings.HasPrefix(cached, "=") {
			return parseValue(cached), nil
		}
	}
	return s.Eval(c)
}

func (s *Sheet) evalExpr(expr string, current Coord, seen map[evalKey]bool) (Value, error) {
	return s.parseFormula(expr, current, seen)
}

// SanitizeForDisplay replaces control/BiDi characters that would garble the
// terminal. Conservative: \n -> ␤, \t -> 4 spaces, \r/\x1b stripped, Cc/Cf
// BiDi isolates stripped. Used at sheet.Display and xlsx import.
func SanitizeForDisplay(s string) string {
	// Fast path: most strings are plain ASCII.
	has := false
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			has = true
			break
		}
	}
	if !has {
		// BiDi/zero-width controls are 2-3 byte UTF-8 starting with 0xE2,
		// 0xEF or 0xD8 (U+061C). A byte check avoids 13 substring scans.
		if strings.IndexByte(s, 0xE2) == -1 && strings.IndexByte(s, 0xEF) == -1 && strings.IndexByte(s, 0xD8) == -1 {
			return s
		}
	}
	var b strings.Builder
	b.Grow(len(s) + 4)
	for _, r := range s {
		switch r {
		case '\n':
			b.WriteRune('␤') // U+2424 SYMBOL FOR NEWLINE
		case '\r':
			// strip
		case '\t':
			b.WriteString("    ")
		case '\x1b':
			// strip ESC
		case '\u202A', '\u202B', '\u202C', '\u202D', '\u202E',
			'\u2066', '\u2067', '\u2068', '\u2069',
			'\u200B', '\u200C', '\u200D', '\u200E', '\u200F',
			'\uFEFF', '\u061C':
			// strip BiDi / zero-width
		default:
			if r < 0x20 || r == 0x7F {
				// strip other Cc
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ---------- Helpers ----------

func isDiv0Error(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "#DIV/0!") || strings.Contains(s, "division by zero") || strings.Contains(s, "MOD by zero")
}

func parseNumber(value string) (float64, error) {
	v := parseValue(value)
	if v.Type == valNumber {
		return v.Num, nil
	}
	return 0, strconv.ErrSyntax
}

func splitFormulaArgs(args string) []string {
	parts := splitTopLevel(args, ',')
	if len(parts) == 1 {
		parts = splitTopLevel(args, ';')
	}
	return parts
}

func splitTopLevel(expr string, sep rune) []string {
	var parts []string
	var current strings.Builder
	depth := 0
	inString := false
	for _, r := range expr {
		if r == '"' {
			inString = !inString
		}
		if !inString {
			switch r {
			case '(', '[':
				depth++
			case ')', ']':
				if depth > 0 {
					depth--
				}
			}
		}
		if !inString && r == sep && depth == 0 {
			parts = append(parts, current.String())
			current.Reset()
			continue
		}
		current.WriteRune(r)
	}
	parts = append(parts, current.String())
	return parts
}

// ---------- Delete / shift operations ----------

func (s *Sheet) DeleteRow(r int) {
	s.invalidateMaxCache()
	nc := make(map[Coord]string, len(s.cells))
	for c, v := range s.cells {
		if c.Row == r {
			continue
		}
		nr := c.Row
		if nr > r {
			nr--
		}
		key := Coord{Row: nr, Col: c.Col}
		if _, isFormula := s.formulas[c]; !isFormula {
			nc[key] = v
		}
	}
	nf := make(map[Coord]string, len(s.formulas))
	for c, f := range s.formulas {
		if c.Row == r {
			continue
		}
		nr := c.Row
		if nr > r {
			nr--
		}
		nf[Coord{Row: nr, Col: c.Col}] = f
	}
	ns := make(map[Coord]Style, len(s.styles))
	for c, st := range s.styles {
		if c.Row == r {
			continue
		}
		nr := c.Row
		if nr > r {
			nr--
		}
		ns[Coord{Row: nr, Col: c.Col}] = st
	}

	s.cells = nc
	s.formulas = nf
	s.styles = ns
	s.adjustMergeRanges(r, 0, true)
	s.adjustAllFormulas(func(c Coord) (Coord, bool) {
		if c.Row == r {
			return Coord{}, false
		}
		if c.Row > r {
			return Coord{Row: c.Row - 1, Col: c.Col}, true
		}
		return c, true
	})
	nhr := make(map[int]bool, len(s.hiddenRows))
	for row, v := range s.hiddenRows {
		if row == r {
			continue
		}
		if row > r {
			nhr[row-1] = v
		} else {
			nhr[row] = v
		}
	}
	s.hiddenRows = nhr
	if s.freezeRow > r {
		s.freezeRow--
		if s.freezeRow < 0 {
			s.freezeRow = 0
		}
	}
}

func (s *Sheet) InsertRow(r int) {
	s.invalidateMaxCache()
	nc := make(map[Coord]string, len(s.cells))
	for coord, v := range s.cells {
		nr := coord.Row
		if nr >= r {
			nr++
		}
		key := Coord{Row: nr, Col: coord.Col}
		if _, isFormula := s.formulas[coord]; !isFormula {
			nc[key] = v
		}
	}
	nf := make(map[Coord]string, len(s.formulas))
	for coord, f := range s.formulas {
		nr := coord.Row
		if nr >= r {
			nr++
		}
		nf[Coord{Row: nr, Col: coord.Col}] = f
	}
	ns := make(map[Coord]Style, len(s.styles))
	for coord, st := range s.styles {
		nr := coord.Row
		if nr >= r {
			nr++
		}
		ns[Coord{Row: nr, Col: coord.Col}] = st
	}
	s.cells = nc
	s.formulas = nf
	s.styles = ns
	var newMerges []Range
	for _, m := range s.merges {
		if m.Start.Row >= r {
			m.Start.Row++
			m.End.Row++
		} else if m.End.Row >= r {
			m.End.Row++
		}
		if m.Valid() && (m.RowSpan() > 1 || m.ColSpan() > 1) {
			newMerges = append(newMerges, m)
		}
	}
	s.merges = newMerges
	s.rebuildMergeMap()
	s.adjustAllFormulas(func(c Coord) (Coord, bool) {
		if c.Row >= r {
			return Coord{Row: c.Row + 1, Col: c.Col}, true
		}
		return c, true
	})
	nhr := make(map[int]bool, len(s.hiddenRows))
	for row, v := range s.hiddenRows {
		nr := row
		if row >= r {
			nr++
		}
		nhr[nr] = v
	}
	s.hiddenRows = nhr
	if r < s.freezeRow {
		s.freezeRow++
	}
}

func (s *Sheet) DeleteColumn(c int) {
	s.invalidateMaxCache()
	nc := make(map[Coord]string, len(s.cells))
	for coord, v := range s.cells {
		if coord.Col == c {
			continue
		}
		nc2 := coord.Col
		if nc2 > c {
			nc2--
		}
		key := Coord{Row: coord.Row, Col: nc2}
		if _, isFormula := s.formulas[coord]; !isFormula {
			nc[key] = v
		}
	}
	nf := make(map[Coord]string, len(s.formulas))
	for coord, f := range s.formulas {
		if coord.Col == c {
			continue
		}
		nc2 := coord.Col
		if nc2 > c {
			nc2--
		}
		nf[Coord{Row: coord.Row, Col: nc2}] = f
	}
	ns := make(map[Coord]Style, len(s.styles))
	for coord, st := range s.styles {
		if coord.Col == c {
			continue
		}
		nc2 := coord.Col
		if nc2 > c {
			nc2--
		}
		ns[Coord{Row: coord.Row, Col: nc2}] = st
	}

	s.cells = nc
	s.formulas = nf
	s.styles = ns
	s.adjustMergeRanges(0, c, false)
	s.adjustAllFormulas(func(coord Coord) (Coord, bool) {
		if coord.Col == c {
			return Coord{}, false
		}
		if coord.Col > c {
			return Coord{Row: coord.Row, Col: coord.Col - 1}, true
		}
		return coord, true
	})

	nhc := make(map[int]bool, len(s.hiddenCols))
	for col, v := range s.hiddenCols {
		if col == c {
			continue
		}
		if col > c {
			nhc[col-1] = v
		} else {
			nhc[col] = v
		}
	}
	s.hiddenCols = nhc
	ncw := make(map[int]int, len(s.colWidths))
	for col, w := range s.colWidths {
		if col == c {
			continue
		}
		if col > c {
			ncw[col-1] = w
		} else {
			ncw[col] = w
		}
	}
	s.colWidths = ncw
	if s.freezeCol > c {
		s.freezeCol--
		if s.freezeCol < 0 {
			s.freezeCol = 0
		}
	}
}

func (s *Sheet) InsertColumn(c int) {
	s.invalidateMaxCache()
	nc := make(map[Coord]string, len(s.cells))
	for coord, v := range s.cells {
		nc2 := coord.Col
		if nc2 >= c {
			nc2++
		}
		key := Coord{Row: coord.Row, Col: nc2}
		if _, isFormula := s.formulas[coord]; !isFormula {
			nc[key] = v
		}
	}
	nf := make(map[Coord]string, len(s.formulas))
	for coord, f := range s.formulas {
		nc2 := coord.Col
		if nc2 >= c {
			nc2++
		}
		nf[Coord{Row: coord.Row, Col: nc2}] = f
	}
	ns := make(map[Coord]Style, len(s.styles))
	for coord, st := range s.styles {
		nc2 := coord.Col
		if nc2 >= c {
			nc2++
		}
		ns[Coord{Row: coord.Row, Col: nc2}] = st
	}

	s.cells = nc
	s.formulas = nf
	s.styles = ns
	// Shift merges
	var newMerges []Range
	for _, m := range s.merges {
		if m.Start.Col >= c {
			m.Start.Col++
			m.End.Col++
		} else if m.End.Col >= c {
			m.End.Col++
		}
		if m.Valid() && (m.RowSpan() > 1 || m.ColSpan() > 1) {
			newMerges = append(newMerges, m)
		}
	}
	s.merges = newMerges
	s.rebuildMergeMap()
	s.adjustAllFormulas(func(coord Coord) (Coord, bool) {
		if coord.Col >= c {
			return Coord{Row: coord.Row, Col: coord.Col + 1}, true
		}
		return coord, true
	})

	nhc := make(map[int]bool, len(s.hiddenCols))
	for col, v := range s.hiddenCols {
		nc2 := col
		if col >= c {
			nc2++
		}
		nhc[nc2] = v
	}
	s.hiddenCols = nhc
	ncw := make(map[int]int, len(s.colWidths))
	for col, w := range s.colWidths {
		nc2 := col
		if col >= c {
			nc2++
		}
		ncw[nc2] = w
	}
	s.colWidths = ncw
	if c < s.freezeCol {
		s.freezeCol++
	}
}

func (s *Sheet) DeleteCellShiftUp(del Coord) {
	s.invalidateMaxCache()
	nc := make(map[Coord]string, len(s.cells))
	for c, v := range s.cells {
		if c == del {
			continue
		}
		nr := c.Row
		if c.Col == del.Col && nr > del.Row {
			nr--
		}
		key := Coord{Row: nr, Col: c.Col}
		if _, isFormula := s.formulas[c]; !isFormula {
			nc[key] = v
		}
	}
	nf := make(map[Coord]string, len(s.formulas))
	for c, f := range s.formulas {
		if c == del {
			continue
		}
		nr := c.Row
		if c.Col == del.Col && nr > del.Row {
			nr--
		}
		nf[Coord{Row: nr, Col: c.Col}] = f
	}
	ns := make(map[Coord]Style, len(s.styles))
	for c, st := range s.styles {
		if c == del {
			continue
		}
		nr := c.Row
		if c.Col == del.Col && nr > del.Row {
			nr--
		}
		ns[Coord{Row: nr, Col: c.Col}] = st
	}

	s.cells = nc
	s.formulas = nf
	s.styles = ns
	s.adjustAllFormulas(func(c Coord) (Coord, bool) {
		if c == del {
			return Coord{}, false
		}
		if c.Col == del.Col && c.Row > del.Row {
			return Coord{Row: c.Row - 1, Col: c.Col}, true
		}
		return c, true
	})
}

func (s *Sheet) DeleteCellShiftLeft(del Coord) {
	s.invalidateMaxCache()
	nc := make(map[Coord]string, len(s.cells))
	for c, v := range s.cells {
		if c == del {
			continue
		}
		nc2 := c.Col
		if c.Row == del.Row && nc2 > del.Col {
			nc2--
		}
		key := Coord{Row: c.Row, Col: nc2}
		if _, isFormula := s.formulas[c]; !isFormula {
			nc[key] = v
		}
	}
	nf := make(map[Coord]string, len(s.formulas))
	for c, f := range s.formulas {
		if c == del {
			continue
		}
		nc2 := c.Col
		if c.Row == del.Row && nc2 > del.Col {
			nc2--
		}
		nf[Coord{Row: c.Row, Col: nc2}] = f
	}
	ns := make(map[Coord]Style, len(s.styles))
	for c, st := range s.styles {
		if c == del {
			continue
		}
		nc2 := c.Col
		if c.Row == del.Row && nc2 > del.Col {
			nc2--
		}
		ns[Coord{Row: c.Row, Col: nc2}] = st
	}

	s.cells = nc
	s.formulas = nf
	s.styles = ns
	s.adjustAllFormulas(func(c Coord) (Coord, bool) {
		if c == del {
			return Coord{}, false
		}
		if c.Row == del.Row && c.Col > del.Col {
			return Coord{Row: c.Row, Col: c.Col - 1}, true
		}
		return c, true
	})
}

func (s *Sheet) adjustMergeRanges(delRow, delCol int, isRow bool) {
	var newMerges []Range
	for _, m := range s.merges {
		if isRow {
			if delRow >= m.Start.Row && delRow <= m.End.Row {
				continue
			}
			if delRow < m.Start.Row {
				m.Start.Row--
				m.End.Row--
			}
		} else {
			if delCol >= m.Start.Col && delCol <= m.End.Col {
				continue
			}
			if delCol < m.Start.Col {
				m.Start.Col--
				m.End.Col--
			}
		}
		if m.Valid() && (m.RowSpan() > 1 || m.ColSpan() > 1) {
			newMerges = append(newMerges, m)
		}
	}
	s.merges = newMerges
	s.rebuildMergeMap()
}

// ---------- Sheet name validation & quoting (ECMA-376) ----------

// ValidateSheetName checks ECMA-376 constraints: 1..31 runes, not blank,
// must not contain : \ / ? * [ ]. Single quote ' is allowed (quoted as ”
// in formulas). Returns error message suitable for UI.
func ValidateSheetName(name string) error {
	trim := strings.TrimSpace(name)
	if trim == "" {
		return stringError("Sheet name cannot be blank")
	}
	if strings.TrimSpace(name) != name {
		// forbid leading/trailing spaces (Excel trims, but we treat as invalid)
		return stringError("Sheet name cannot begin or end with space")
	}
	if utf8.RuneCountInString(name) > 31 {
		return stringError("Sheet name cannot exceed 31 characters")
	}
	if strings.ContainsAny(name, `:\/?*[]`) {
		return stringError("Sheet name cannot contain : \\ / ? * [ ]")
	}
	// Control chars 0x00-0x1F not allowed
	for _, r := range name {
		if r < 0x20 {
			return stringError("Sheet name cannot contain control characters")
		}
	}
	return nil
}

type stringError string

func (e stringError) Error() string { return string(e) }

// NeedsSheetNameQuote reports whether name must be quoted as 'Name'! in formulas.
func NeedsSheetNameQuote(name string) bool {
	if name == "" {
		return true
	}
	// Unquoted form is [A-Za-z_][A-Za-z0-9_.]* (Excel allows _ and . after first char)
	if len(name) == 0 {
		return true
	}
	for i, r := range name {
		if i == 0 {
			if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')) {
				return true
			}
		} else {
			if !(r == '_' || r == '.' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
				return true
			}
		}
	}
	return false
}

// QuoteSheetName returns the formula representation of name (quoted if needed, ' doubled).
func QuoteSheetName(name string) string {
	if !NeedsSheetNameQuote(name) {
		return name
	}
	return "'" + strings.ReplaceAll(name, "'", "''") + "'"
}

// RenameSheetInFormulas updates cross-sheet references from oldName to newName
// in all formulas of this sheet. It handles both unquoted Old!A1 and quoted 'Old'!/'Old”s'! forms
// case-insensitively, preserving the target's required quoting. It skips string literals.
func (s *Sheet) RenameSheetInFormulas(oldName, newName string) {
	if strings.EqualFold(oldName, newName) {
		return
	}
	newQuoted := QuoteSheetName(newName)
	for coord, f := range s.formulas {
		patched := renameSheetInFormulaText(f, oldName, newQuoted)
		if patched != f {
			s.formulas[coord] = patched
		}
	}
	// Also patch defined names map content that may contain sheet-qualified refs
	if s.definedNames != nil {
		for k, v := range s.definedNames {
			pv := renameSheetInFormulaText(v, oldName, newQuoted)
			if pv != v {
				s.definedNames[k] = pv
			}
		}
	}
}

func renameSheetInFormulaText(formula, oldName, newQuoted string) string {
	var out strings.Builder
	out.Grow(len(formula) + 8)
	i := 0
	inStr := false
	oldLower := strings.ToLower(oldName)
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
		if inStr {
			out.WriteByte(ch)
			i++
			continue
		}
		// Try quoted form: ' ... '' ... '!
		if ch == '\'' {
			// Scan quoted sheet name
			j := i + 1
			for j < len(formula) {
				if formula[j] == '\'' {
					if j+1 < len(formula) && formula[j+1] == '\'' {
						j += 2
						continue
					}
					j++ // closing '
					break
				}
				j++
			}
			if j < len(formula) && formula[j-1] == '\'' && formula[j] == '!' {
				// Extract inner name (doubled '' -> ')
				inner := formula[i+1 : j-1]
				unquoted := strings.ReplaceAll(inner, "''", "'")
				if strings.EqualFold(unquoted, oldName) || strings.ToLower(unquoted) == oldLower {
					out.WriteString(newQuoted)
					out.WriteByte('!')
					i = j + 1
					continue
				}
			}
			// Not a match, copy one char and continue
			out.WriteByte(ch)
			i++
			continue
		}
		// Try unquoted identifier before '!'
		if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '_' {
			start := i
			j := i
			for j < len(formula) && ((formula[j] >= 'A' && formula[j] <= 'Z') || (formula[j] >= 'a' && formula[j] <= 'z') || (formula[j] >= '0' && formula[j] <= '9') || formula[j] == '_' || formula[j] == '.') {
				j++
			}
			if j < len(formula) && formula[j] == '!' {
				cand := formula[start:j]
				if strings.EqualFold(cand, oldName) {
					out.WriteString(newQuoted)
					out.WriteByte('!')
					i = j + 1
					continue
				}
			}
		}
		out.WriteByte(ch)
		i++
	}
	return out.String()
}
