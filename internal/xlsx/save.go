package xlsx

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"tuisheet/internal/buildinfo"
	"tuisheet/internal/sheet"
)

//go:embed theme.xml
var themeXML []byte

func Save(filename, originalPath string, sheets []*sheet.Sheet, sheetNames []string) error {
	return SaveWithProps(filename, originalPath, sheets, sheetNames, nil)
}

func SaveWithProps(filename, originalPath string, sheets []*sheet.Sheet, sheetNames []string, props *DocProps) error {
	if len(sheets) != len(sheetNames) {
		return fmt.Errorf("sheet count (%d) does not match sheet-name count (%d)", len(sheets), len(sheetNames))
	}
	for i, s := range sheets {
		if s == nil {
			return fmt.Errorf("sheet %d is nil", i)
		}
	}

	var originalFiles map[string][]byte
	if originalPath != "" {
		var err error
		originalFiles, err = readZipEntries(originalPath)
		if err != nil {
			return fmt.Errorf("read original: %w", err)
		}
	}

	styles := newStyleRegistry(sheets)
	sharedStrings, stringIndex := collectSharedStrings(sheets)
	sheetData := make([][]byte, len(sheets))
	for i, s := range sheets {
		sheetData[i] = generateWorksheetXML(s, stringIndex, styles.index)
	}

	dir := filepath.Dir(filename)
	if dir == "" {
		dir = "."
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(filename)+"-*")
	if err != nil {
		return fmt.Errorf("create temporary workbook: %w", err)
	}
	tempName := f.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempName)
		}
	}()

	zw := zip.NewWriter(f)
	if err := writeWorkbook(zw, originalFiles, sheets, sheetNames, sharedStrings, sheetData, styles, props); err != nil {
		_ = zw.Close()
		_ = f.Close()
		return err
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return fmt.Errorf("finish workbook archive: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("sync workbook: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close workbook: %w", err)
	}
	if err := os.Rename(tempName, filename); err != nil {
		return fmt.Errorf("replace workbook: %w", err)
	}
	committed = true
	return nil
}

func writeWorkbook(zw *zip.Writer, originalFiles map[string][]byte, sheets []*sheet.Sheet, sheetNames, sharedStrings []string, sheetData [][]byte, styles *styleRegistry, props *DocProps) error {
	docPropsDirty := props != nil
	isNewFile := needsDocPropsStamp(originalFiles)
	needDocProps := docPropsDirty || isNewFile
	themeMissing := originalFiles == nil || originalFiles["xl/theme/theme1.xml"] == nil

	// Determine which docProps parts to preserve vs regenerate
	// If needDocProps, we will regenerate core/app and must not copy original docProps parts
	excludeDocProps := needDocProps
	if originalFiles != nil {
		for name, data := range originalFiles {
			if isReplacedEntry(name) {
				continue
			}
			if excludeDocProps && (name == "docProps/core.xml" || name == "docProps/app.xml") {
				continue
			}
			if (needDocProps || themeMissing) && name == "_rels/.rels" {
				continue
			}
			if (needDocProps || themeMissing) && name == "[Content_Types].xml" {
				continue
			}
			if themeMissing && name == "xl/_rels/workbook.xml.rels" {
				continue
			}
			if err := writeZipEntry(zw, name, data); err != nil {
				return fmt.Errorf("write %s: %w", name, err)
			}
		}
	}
	// Handle docProps generation
	if needDocProps {
		// Prepare maps from original
		origCoreMap := map[string]string{}
		origAppMap := map[string]string{}
		if originalFiles != nil {
			if data, ok := originalFiles["docProps/core.xml"]; ok {
				origCoreMap = parseGenericPropsMap(data)
			}
			if data, ok := originalFiles["docProps/app.xml"]; ok {
				origAppMap = parseGenericPropsMap(data)
			}
		}
		var p DocProps
		if props != nil {
			p = *props
		} else if isNewFile {
			// stamp without user edits
			p = DocProps{}
		} else if originalFiles != nil {
			p = docPropsFromFiles(originalFiles)
		}
		var coreXML, appXML []byte
		if data, ok := originalFiles["docProps/core.xml"]; ok {
			coreXML = patchCoreXML(data, p, isNewFile)
		} else {
			coreMap := corePropsMap(p, origCoreMap, isNewFile)
			coreXML = generateCoreXML(coreMap)
		}
		if data, ok := originalFiles["docProps/app.xml"]; ok {
			appXML = patchAppXML(data, p, isNewFile, sheetNames)
		} else {
			appMap := appPropsMap(p, origAppMap, isNewFile)
			appXML = generateAppXML(appMap)
			if !strings.Contains(string(appXML), "<HeadingPairs>") {
				// Ensure HeadingPairs/TitlesOfParts for new files without original
				headingName := "Worksheets"
				sheetCount := len(sheetNames)
				if sheetCount == 0 {
					sheetCount = 1
				}
				// Append proper vectors if generateAppXML omitted them (it does omit)
				extra := fmt.Sprintf(`<HeadingPairs><vt:vector size="2" baseType="variant"><vt:variant><vt:lpstr>%s</vt:lpstr></vt:variant><vt:variant><vt:i4>%d</vt:i4></vt:variant></vt:vector></HeadingPairs>`, xmlEscape(headingName), sheetCount)
				var tb strings.Builder
				tb.WriteString(fmt.Sprintf(`<TitlesOfParts><vt:vector size="%d" baseType="lpstr">`, sheetCount))
				if len(sheetNames) == 0 {
					tb.WriteString(`<vt:lpstr>Sheet1</vt:lpstr>`)
				} else {
					for _, n := range sheetNames {
						tb.WriteString(`<vt:lpstr>`)
						tb.WriteString(xmlEscape(n))
						tb.WriteString(`</vt:lpstr>`)
					}
				}
				tb.WriteString(`</vt:vector></TitlesOfParts>`)
				s := string(appXML)
				s = strings.Replace(s, "</Properties>", extra+tb.String()+"</Properties>", 1)
				appXML = []byte(s)
			}
		}
		if err := writeZipEntry(zw, "docProps/core.xml", coreXML); err != nil {
			return err
		}
		if err := writeZipEntry(zw, "docProps/app.xml", appXML); err != nil {
			return err
		}
		_ = buildinfo.AppName
	}
	// Preserve original [Content_Types].xml when available to keep theme/docProps etc.
	// Generating a minimal version would miss required overrides and cause Excel repair.
	needCT := needDocProps || themeMissing
	if needCT {
		if originalFiles != nil {
			if data, ok := originalFiles["[Content_Types].xml"]; ok {
				patched := data
				if needDocProps {
					patched = ensureContentTypesHasDocProps(patched)
				}
				if themeMissing {
					patched = ensureContentTypesHasTheme(patched)
				}
				if err := writeZipEntry(zw, "[Content_Types].xml", patched); err != nil {
					return err
				}
			} else {
				if err := writeZipEntry(zw, "[Content_Types].xml", generateContentTypesXMLWithProps(len(sheets), true)); err != nil {
					return err
				}
			}
		} else {
			if err := writeZipEntry(zw, "[Content_Types].xml", generateContentTypesXMLWithProps(len(sheets), true)); err != nil {
				return err
			}
		}
	} else {
		if originalFiles != nil {
			if data, ok := originalFiles["[Content_Types].xml"]; ok {
				if err := writeZipEntry(zw, "[Content_Types].xml", data); err != nil {
					return err
				}
			} else {
				if err := writeZipEntry(zw, "[Content_Types].xml", generateContentTypesXML(len(sheets))); err != nil {
					return err
				}
			}
		} else {
			if err := writeZipEntry(zw, "[Content_Types].xml", generateContentTypesXML(len(sheets))); err != nil {
				return err
			}
		}
	}
	if err := writeZipEntry(zw, "xl/workbook.xml", generateWorkbookXML(sheetNames)); err != nil {
		return err
	}
	if themeMissing {
		if data, ok := originalFiles["xl/_rels/workbook.xml.rels"]; ok {
			patched := ensureWorkbookRelsHasTheme(data)
			if err := writeZipEntry(zw, "xl/_rels/workbook.xml.rels", patched); err != nil {
				return err
			}
		} else {
			data := generateWorkbookRelsXML(len(sheets))
			patched := ensureWorkbookRelsHasTheme(data)
			if err := writeZipEntry(zw, "xl/_rels/workbook.xml.rels", patched); err != nil {
				return err
			}
		}
	} else {
		if err := writeZipEntry(zw, "xl/_rels/workbook.xml.rels", generateWorkbookRelsXML(len(sheets))); err != nil {
			return err
		}
	}
	if err := writeZipEntry(zw, "xl/styles.xml", generateStylesXML(styles)); err != nil {
		return err
	}
	if originalFiles == nil {
		if err := writeZipEntry(zw, "_rels/.rels", generateRelsXMLWithProps(needDocProps)); err != nil {
			return err
		}
	} else if needDocProps {
		if data, ok := originalFiles["_rels/.rels"]; ok {
			patched := ensureRelsHasDocProps(data)
			if err := writeZipEntry(zw, "_rels/.rels", patched); err != nil {
				return err
			}
		} else {
			if err := writeZipEntry(zw, "_rels/.rels", generateRelsXMLWithProps(true)); err != nil {
				return err
			}
		}
	}
	if themeMissing {
		if err := writeZipEntry(zw, "xl/theme/theme1.xml", themeXML); err != nil {
			return err
		}
	}

	if err := writeZipEntry(zw, "xl/sharedStrings.xml", generateSharedStringsXML(sharedStrings)); err != nil {
		return err
	}
	for i := range sheets {
		wsPath := fmt.Sprintf("xl/worksheets/sheet%d.xml", i+1)
		if err := writeZipEntry(zw, wsPath, sheetData[i]); err != nil {
			return err
		}
	}
	return nil
}

func isReplacedEntry(name string) bool {
	if name == "[Content_Types].xml" {
		return true
	}
	if name == "xl/sharedStrings.xml" {
		return true
	}
	if name == "xl/workbook.xml" {
		return true
	}
	if name == "xl/_rels/workbook.xml.rels" {
		return true
	}
	if name == "xl/styles.xml" {
		return true
	}
	if strings.HasPrefix(name, "xl/worksheets/sheet") && strings.HasSuffix(name, ".xml") {
		return true
	}
	return false
}

func readZipEntries(path string) (map[string][]byte, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if err := validateZipFiles(r.File); err != nil {
		return nil, err
	}
	out := make(map[string][]byte, len(r.File))
	for _, f := range r.File {
		data, err := readZipFile(f)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		out[f.Name] = data
	}
	return out, nil
}

func writeZipEntry(zw *zip.Writer, name string, data []byte) error {
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

type fontKey struct {
	bold          bool
	italic        bool
	underline     bool
	strikethrough bool
}

type styleRegistry struct {
	styles  []sheet.Style
	index   map[sheet.Style]int
	fonts   []fontKey
	numFmts []numFmtEntry // custom formats needing <numFmts> entries
}

type numFmtEntry struct {
	id   int
	code string
}

func newStyleRegistry(sheets []*sheet.Sheet) *styleRegistry {
	unique := map[sheet.Style]bool{}
	for _, sh := range sheets {
		sh.ForEachCell(func(_ sheet.Coord, _, _ string, st sheet.Style) bool {
			unique[st] = true
			return true
		})
		for _, st := range sh.Styles() {
			unique[st] = true
		}
	}

	styles := make([]sheet.Style, 0, len(unique)+1)
	styles = append(styles, sheet.Style{})
	delete(unique, sheet.Style{})
	for st := range unique {
		styles = append(styles, st)
	}
	sort.Slice(styles[1:], func(i, j int) bool {
		a, b := styles[i+1], styles[j+1]
		if a.NumFmt != b.NumFmt {
			return a.NumFmt < b.NumFmt
		}
		if a.Align != b.Align {
			return a.Align < b.Align
		}
		if a.Wrap != b.Wrap {
			return !a.Wrap
		}
		if a.Bold != b.Bold {
			return !a.Bold
		}
		if a.Italic != b.Italic {
			return !a.Italic
		}
		if a.Underline != b.Underline {
			return !a.Underline
		}
		return !a.Strikethrough && b.Strikethrough
	})

	index := make(map[sheet.Style]int, len(styles))
	fonts := []fontKey{{}}
	seenFonts := map[fontKey]bool{{}: true}
	var numFmts []numFmtEntry
	nextNumFmtID := 164 // custom format IDs start at 164 (ISO/IEC 29500-1 §18.8.30)
	for i, st := range styles {
		index[st] = i
		font := fontKey{bold: st.Bold, italic: st.Italic, underline: st.Underline, strikethrough: st.Strikethrough}
		if !seenFonts[font] {
			seenFonts[font] = true
			fonts = append(fonts, font)
		}
		if st.NumFmt != "" && !isBuiltinNumFmt(st.NumFmt) {
			if findStringIndex(numFmtCodes(numFmts), st.NumFmt) < 0 {
				numFmts = append(numFmts, numFmtEntry{id: nextNumFmtID, code: st.NumFmt})
				nextNumFmtID++
			}
		}
	}
	return &styleRegistry{styles: styles, index: index, fonts: fonts, numFmts: numFmts}
}

// builtinNumFmtCodes is the reverse of builtinNumFmts, lazily derived.
var builtinNumFmtCodes map[string]int

func ensureBuiltinReverse() {
	if builtinNumFmtCodes != nil {
		return
	}
	builtinNumFmtCodes = make(map[string]int, len(builtinNumFmts))
	for id, code := range builtinNumFmts {
		builtinNumFmtCodes[code] = id
	}
}

func isBuiltinNumFmt(code string) bool {
	ensureBuiltinReverse()
	_, ok := builtinNumFmtCodes[code]
	return ok
}

func numFmtCodes(entries []numFmtEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.code
	}
	return out
}

// numFmtIDFor returns the ID a style's format code should carry in the xf.
func (r *styleRegistry) numFmtIDFor(code string) int {
	ensureBuiltinReverse()
	if code == "" {
		return 0
	}
	if id, ok := builtinNumFmtCodes[code]; ok {
		return id
	}
	for _, e := range r.numFmts {
		if e.code == code {
			return e.id
		}
	}
	return 0
}

func isNumber(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.ParseFloat(s, 64)
	return err == nil
}

// collectSharedStrings returns the unique strings plus a value->index
// map so worksheet generation can intern strings in O(1) instead of
// scanning the slice per cell.
func collectSharedStrings(sheets []*sheet.Sheet) ([]string, map[string]int) {
	seen := map[string]int{}
	var out []string
	for _, s := range sheets {
		s.ForEachCell(func(c sheet.Coord, value, formula string, style sheet.Style) bool {
			if formula != "" {
				// Formula cell: cached value might be a string
				cached := value
				if strings.HasPrefix(cached, "=") {
					v, err := evaluateForSave(s, c)
					if err == nil {
						cached = v
					} else {
						cached = ""
					}
				}
				if cached != "" && !isNumber(cached) {
					if _, ok := seen[cached]; !ok {
						seen[cached] = len(out)
						out = append(out, cached)
					}
				}
				return true
			}
			// Non-formula: if it's a string, add to shared strings
			if value == "" {
				return true
			}
			if !isNumber(value) {
				if _, ok := seen[value]; !ok {
					seen[value] = len(out)
					out = append(out, value)
				}
			}
			return true
		})
	}
	return out, seen
}

func evaluateForSave(s *sheet.Sheet, c sheet.Coord) (string, error) {
	return s.Display(c), nil
}

// unsanitizeForSave maps the display placeholder U+2424 (SYMBOL FOR NEWLINE)
// back to a true newline at the serialization boundary. Cell storage holds
// ␤ (import sanitizes \n for terminal display); the file must carry \n so
// Excel renders a line break.
func unsanitizeForSave(s string) string {
	return strings.ReplaceAll(s, "␤", "\n")
}

func generateSharedStringsXML(strs []string) []byte {
	pr := newPrinter()
	pr.w.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	pr.w.WriteString(`<sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" count="`)
	pr.writeInt(len(strs))
	pr.w.WriteString(`" uniqueCount="`)
	pr.writeInt(len(strs))
	pr.w.WriteString(`">`)
	for _, s := range strs {
		raw := unsanitizeForSave(s)
		pr.w.WriteString(`<si><t`)
		if strings.ContainsAny(s, "<>&") || raw != strings.TrimSpace(raw) {
			pr.w.WriteString(` xml:space="preserve"`)
		}
		pr.w.WriteString(`>`)
		xml.EscapeText(pr.w, []byte(raw))
		pr.w.WriteString(`</t></si>`)
	}
	pr.w.WriteString(`</sst>`)
	return pr.buf.Bytes()
}

type xmlPrinter struct {
	buf bytes.Buffer
	w   *bytes.Buffer
}

func newPrinter() *xmlPrinter {
	p := &xmlPrinter{}
	p.w = &p.buf
	return p
}

func (p *xmlPrinter) writeInt(v int) {
	p.w.WriteString(strconv.Itoa(v))
}

func generateWorksheetXML(s *sheet.Sheet, stringIndex map[string]int, styleIndexMap map[sheet.Style]int) []byte {
	pr := newPrinter()
	pr.w.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	pr.w.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" mc:Ignorable="x14ac xr xr2 xr3" xmlns:x14ac="http://schemas.microsoft.com/office/spreadsheetml/2009/9/ac" xmlns:xr="http://schemas.microsoft.com/office/spreadsheetml/2014/revision" xmlns:xr2="http://schemas.microsoft.com/office/spreadsheetml/2015/revision2" xmlns:xr3="http://schemas.microsoft.com/office/spreadsheetml/2016/revision3" xr:uid="{00000000-0001-0000-0000-000000000000}">`)
	writeColsXML(pr, s)
	// Collect all cells, group by row
	type cellInfo struct {
		ref     string
		col     int
		formula string
		value   string
		t       string
	}
	rows := map[int][]cellInfo{}

	s.ForEachCell(func(c sheet.Coord, value, formula string, style sheet.Style) bool {
		ref := c.String()
		if ref == "" {
			return true
		}
		fText := ""
		if formula != "" {
			fText = PrefixXLFN(strings.TrimPrefix(formula, "="))
		}

		ci := cellInfo{
			ref:     ref,
			col:     c.Col,
			formula: fText,
		}

		if formula != "" {
			cached := s.Display(c)
			if !isNumber(cached) {
				// A formatted cache (e.g. "2026-08-25" under a date
				// format) must be stored as its raw serial so Excel
				// sees a numeric cached value. EvalCached reuses the
				// raw value Display just computed instead of
				// re-parsing the formula.
				if v, err := s.EvalCached(c); err == nil {
					if n, ok := sheet.NumberOf(v); ok {
						cached = strconv.FormatFloat(n, 'f', -1, 64)
					}
				}
			}
			if cached == "" || cached == "#ERR" || cached == "#N/A" || cached == "#DIV/0!" {
				ci.value = ""
			} else if strings.EqualFold(cached, "TRUE") {
				ci.t = "b"
				ci.value = "1"
			} else if strings.EqualFold(cached, "FALSE") {
				ci.t = "b"
				ci.value = "0"
			} else if isNumber(cached) {
				ci.value = cached
			} else {
				// Excel stores string-valued formula results inline as
				// t="str", not via shared strings.
				ci.t = "str"
				ci.value = unsanitizeForSave(cached)
			}
		} else {
			if value == "" {
				return true
			}
			if isNumber(value) {
				ci.value = value
			} else if idx, ok := stringIndex[value]; ok {
				ci.value = strconv.Itoa(idx)
				ci.t = "s"
			}
		}

		if ci.value == "" && ci.formula == "" {
			return true
		}

		rn := c.Row
		rows[rn] = append(rows[rn], ci)
		return true
	})

	// Include empty cells that have a style (e.g. Range/Format on empty range)
	cellsMap := s.Cells()
	formulasMap := s.Formulas()
	for coord, st := range s.Styles() {
		if _, ok := cellsMap[coord]; ok {
			continue
		}
		if _, ok := formulasMap[coord]; ok {
			continue
		}
		ref := coord.String()
		if ref == "" {
			continue
		}
		ci := cellInfo{ref: ref, col: coord.Col}
		rn := coord.Row
		rows[rn] = append(rows[rn], ci)
		_ = st
	}
	for r := range s.HiddenRows() {
		if _, ok := rows[r]; !ok {
			rows[r] = nil
		}
	}

	dimRef := "A1"
	if len(rows) > 0 {
		minR, maxR := 1<<30, -1
		minC, maxC := 1<<30, -1
		for r, cols := range rows {
			if r < minR {
				minR = r
			}
			if r > maxR {
				maxR = r
			}
			for _, ci := range cols {
				if ci.col < minC {
					minC = ci.col
				}
				if ci.col > maxC {
					maxC = ci.col
				}
			}
		}
		if minR <= maxR && minC <= maxC {
			dimRef = sheet.Coord{Row: minR, Col: minC}.String() + ":" + sheet.Coord{Row: maxR, Col: maxC}.String()
		}
	}
	pr.w.WriteString(`<dimension ref="` + dimRef + `"/>`)
	if fr, fc := s.Freeze(); fr > 0 || fc > 0 {
		topLeft := sheet.Coord{Row: fr, Col: fc}.String()
		// activePane per ECMA: bottomRight if both, bottomLeft if only y, topRight if only x
		activePane := "bottomRight"
		if fr > 0 && fc == 0 {
			activePane = "bottomLeft"
		} else if fr == 0 && fc > 0 {
			activePane = "topRight"
		}
		pr.w.WriteString(`<sheetViews><sheetView tabSelected="1" workbookViewId="0">`)
		pr.w.WriteString(`<pane xSplit="` + strconv.Itoa(fc) + `" ySplit="` + strconv.Itoa(fr) + `" topLeftCell="` + topLeft + `" activePane="` + activePane + `" state="frozen"/>`)
		pr.w.WriteString(`</sheetView></sheetViews>`)
	} else {
		pr.w.WriteString(`<sheetViews><sheetView tabSelected="1" workbookViewId="0"/></sheetViews>`)
	}
	pr.w.WriteString(`<sheetFormatPr defaultRowHeight="15" x14ac:dyDescent="0.25"/>`)
	pr.w.WriteString(`<sheetData>`)

	rowNums := make([]int, 0, len(rows))
	for rn := range rows {
		rowNums = append(rowNums, rn)
	}
	sort.Ints(rowNums)

	for _, rn := range rowNums {
		cells := rows[rn]
		sort.Slice(cells, func(i, j int) bool {
			return cells[i].col < cells[j].col
		})

		pr.w.WriteString(`<row r="`)
		pr.writeInt(rn + 1)
		pr.w.WriteString(`" spans="1:1" x14ac:dyDescent="0.25"`)
		if s.IsRowHidden(rn) {
			pr.w.WriteString(` hidden="1"`)
		}
		pr.w.WriteString(`>`)
		for _, ci := range cells {
			pr.w.WriteString(`<c r="`)
			pr.w.WriteString(ci.ref)
			pr.w.WriteString(`"`)
			if ci.t != "" {
				pr.w.WriteString(` t="`)
				pr.w.WriteString(ci.t)
				pr.w.WriteString(`"`)
			}
			cellStyle := s.Style(sheet.Coord{Row: rn, Col: ci.col})
			if styleIdx, ok := styleIndexMap[cellStyle]; ok {
				pr.w.WriteString(` s="`)
				pr.writeInt(styleIdx)
				pr.w.WriteString(`"`)
			}
			pr.w.WriteString(`>`)
			if ci.formula != "" {
				pr.w.WriteString(`<f>`)
				xml.EscapeText(pr.w, []byte(ci.formula))
				pr.w.WriteString(`</f>`)
			}
			if ci.value != "" {
				pr.w.WriteString(`<v>`)
				// Escape unconditionally: numeric payloads (numbers,
				// shared-string indices, booleans) pass through byte-
				// identical, while t="str" formula results may contain
				// user text with &<>. A bare & would corrupt the file.
				xml.EscapeText(pr.w, []byte(ci.value))
				pr.w.WriteString(`</v>`)
			}
			pr.w.WriteString(`</c>`)
		}
		pr.w.WriteString(`</row>`)
	}

	pr.w.WriteString(`</sheetData>`)
	pr.w.WriteString(`<pageMargins left="0.7" right="0.7" top="0.75" bottom="0.75" header="0.3" footer="0.3"/>`)

	// Write merge ranges
	merges := s.Merges()
	if len(merges) > 0 {
		pr.w.WriteString(`<mergeCells count="`)
		pr.writeInt(len(merges))
		pr.w.WriteString(`">`)
		for _, m := range merges {
			pr.w.WriteString(`<mergeCell ref="`)
			pr.w.WriteString(m.Start.String())
			pr.w.WriteString(`:`)
			pr.w.WriteString(m.End.String())
			pr.w.WriteString(`"/>`)
		}
		pr.w.WriteString(`</mergeCells>`)
	}

	pr.w.WriteString(`</worksheet>`)
	return pr.buf.Bytes()
}

func findStringIndex(strs []string, s string) int {
	for i, v := range strs {
		if v == s {
			return i
		}
	}
	return -1
}

type colSpec struct {
	width  int
	hidden bool
}

// writeColsXML emits the <cols> element (ISO/IEC 29500-1 §18.3.1.13).
// Only columns with a custom width or hidden flag are written; adjacent
// columns sharing identical settings are merged into one <col> range.
func writeColsXML(pr *xmlPrinter, s *sheet.Sheet) {
	widths := s.GetAllColWidths()
	hidden := s.HiddenCols()
	if len(widths) == 0 && len(hidden) == 0 {
		return
	}

	spec := func(col int) (colSpec, bool) {
		w, hasW := widths[col]
		h, hasH := hidden[col]
		if !hasW && !hasH {
			return colSpec{}, false
		}
		return colSpec{width: w, hidden: h}, true
	}

	cols := make([]int, 0, len(widths)+len(hidden))
	seen := make(map[int]bool)
	for c := range widths {
		cols = append(cols, c)
		seen[c] = true
	}
	for c := range hidden {
		if !seen[c] {
			cols = append(cols, c)
		}
	}
	sort.Ints(cols)

	pr.w.WriteString(`<cols>`)
	runStart := 0
	for j := 1; j <= len(cols); j++ {
		extend := false
		if j < len(cols) {
			prev, _ := spec(cols[j-1])
			next, okNext := spec(cols[j])
			if okNext && next == prev && cols[j] == cols[j-1]+1 {
				extend = true
			}
		}
		if !extend {
			sp, _ := spec(cols[runStart])
			writeColRange(pr, cols[runStart], cols[j-1], sp)
			runStart = j
		}
	}
	pr.w.WriteString(`</cols>`)
}

func writeColRange(pr *xmlPrinter, minCol, maxCol int, sp colSpec) {
	pr.w.WriteString(`<col min="`)
	pr.writeInt(minCol + 1)
	pr.w.WriteString(`" max="`)
	pr.writeInt(maxCol + 1)
	pr.w.WriteString(`"`)
	if sp.width > 0 {
		pr.w.WriteString(` width="`)
		pr.writeInt(sp.width)
		pr.w.WriteString(`" customWidth="1"`)
	}
	if sp.hidden {
		pr.w.WriteString(` hidden="1"`)
	}
	pr.w.WriteString(`/>`)
}

func generateContentTypesXML(sheetCount int) []byte {
	pr := newPrinter()
	pr.w.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	pr.w.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	pr.w.WriteString(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`)
	pr.w.WriteString(`<Default Extension="xml" ContentType="application/xml"/>`)
	pr.w.WriteString(`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`)
	for i := 0; i < sheetCount; i++ {
		pr.w.WriteString(fmt.Sprintf(`<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i+1))
	}
	pr.w.WriteString(`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	pr.w.WriteString(`<Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>`)
	pr.w.WriteString(`</Types>`)
	return pr.buf.Bytes()
}

func generateRelsXML() []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`)
}

func generateWorkbookXML(sheetNames []string) []byte {
	pr := newPrinter()
	pr.w.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	pr.w.WriteString(`<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" mc:Ignorable="x15 xr xr6 xr10 xr2" xmlns:x15="http://schemas.microsoft.com/office/spreadsheetml/2010/11/main" xmlns:xr="http://schemas.microsoft.com/office/spreadsheetml/2014/revision" xmlns:xr6="http://schemas.microsoft.com/office/spreadsheetml/2016/revision6" xmlns:xr10="http://schemas.microsoft.com/office/spreadsheetml/2016/revision10" xmlns:xr2="http://schemas.microsoft.com/office/spreadsheetml/2015/revision2">`)
	pr.w.WriteString(`<fileVersion appName="xl" lastEdited="7" lowestEdited="7" rupBuild="29231"/><workbookPr defaultThemeVersion="202300"/>`)
	pr.w.WriteString(`<mc:AlternateContent xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006"><mc:Choice Requires="x15"><x15ac:absPath url="" xmlns:x15ac="http://schemas.microsoft.com/office/spreadsheetml/2010/11/ac"/></mc:Choice></mc:AlternateContent>`)
	pr.w.WriteString(`<xr:revisionPtr revIDLastSave="0" documentId="8_{00000000-0000-0000-0000-000000000000}" xr6:coauthVersionLast="47" xr6:coauthVersionMax="47" xr10:uidLastSave="{00000000-0000-0000-0000-000000000000}"/><bookViews><workbookView xWindow="0" yWindow="0" windowWidth="25600" windowHeight="15360" xr2:uid="{00000000-000D-0000-FFFF-FFFF00000000}"/></bookViews>`)
	pr.w.WriteString(`<sheets>`)
	for i, name := range sheetNames {
		pr.w.WriteString(fmt.Sprintf(`<sheet name="%s" sheetId="%d" r:id="rId%d"/>`, xmlEscaped(name), i+1, i+1))
	}
	pr.w.WriteString(`</sheets>`)
	// Force Excel/LibreOffice to recalculate on open so cached values we
	// wrote can never mask a discrepancy.
	pr.w.WriteString(`<calcPr calcId="191029" fullCalcOnLoad="1"/>`)
	pr.w.WriteString(`</workbook>`)
	return pr.buf.Bytes()
}

func xmlEscaped(s string) string {
	var buf bytes.Buffer
	xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func generateWorkbookRelsXML(sheetCount int) []byte {
	pr := newPrinter()
	pr.w.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	pr.w.WriteString(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := 0; i < sheetCount; i++ {
		pr.w.WriteString(fmt.Sprintf(`<Relationship Id="rId%d" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet%d.xml"/>`, i+1, i+1))
	}
	pr.w.WriteString(`<Relationship Id="rIdSt" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>`)
	pr.w.WriteString(`<Relationship Id="rIdSS" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/sharedStrings" Target="sharedStrings.xml"/>`)
	pr.w.WriteString(`</Relationships>`)
	return pr.buf.Bytes()
}

func generateStylesXML(registry *styleRegistry) []byte {
	pr := newPrinter()
	pr.w.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	pr.w.WriteString(`<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:mc="http://schemas.openxmlformats.org/markup-compatibility/2006" mc:Ignorable="x14ac x16r2 xr" xmlns:x14ac="http://schemas.microsoft.com/office/spreadsheetml/2009/9/ac" xmlns:x16r2="http://schemas.microsoft.com/office/spreadsheetml/2015/02/main" xmlns:xr="http://schemas.microsoft.com/office/spreadsheetml/2014/revision">`)
	// CT_Stylesheet (ISO/IEC 29500-1 §18.8.9) is a strict sequence with
	// numFmts first; Excel rejects the file otherwise.
	if len(registry.numFmts) > 0 {
		pr.w.WriteString(`<numFmts count="`)
		pr.writeInt(len(registry.numFmts))
		pr.w.WriteString(`">`)
		for _, nf := range registry.numFmts {
			pr.w.WriteString(`<numFmt numFmtId="`)
			pr.writeInt(nf.id)
			pr.w.WriteString(`" formatCode="`)
			pr.w.WriteString(xmlEscaped(nf.code))
			pr.w.WriteString(`"/>`)
		}
		pr.w.WriteString(`</numFmts>`)
	}

	pr.w.WriteString(`<fonts count="`)
	pr.writeInt(len(registry.fonts))
	pr.w.WriteString(`" x14ac:knownFonts="1">`)
	for _, fk := range registry.fonts {
		pr.w.WriteString(`<font>`)
		if fk.bold {
			pr.w.WriteString(`<b/>`)
		}
		if fk.italic {
			pr.w.WriteString(`<i/>`)
		}
		if fk.underline {
			pr.w.WriteString(`<u/>`)
		}
		if fk.strikethrough {
			pr.w.WriteString(`<strike/>`)
		}
		// Excel's own files always carry size and name; an empty <font/>
		// trips its styles repair.
		pr.w.WriteString(`<sz val="11"/><name val="Calibri"/>`)
		pr.w.WriteString(`</font>`)
	}
	pr.w.WriteString(`</fonts>`)

	// Excel expects the canonical fills/borders/cellStyleXfs/cellStyles
	// skeleton; minimal stylesheets without them trigger "Repaired
	// Records: Format from /xl/styles.xml" on open.
	pr.w.WriteString(`<fills count="2">`)
	pr.w.WriteString(`<fill><patternFill patternType="none"/></fill>`)
	pr.w.WriteString(`<fill><patternFill patternType="gray125"/></fill>`)
	pr.w.WriteString(`</fills>`)
	pr.w.WriteString(`<borders count="1">`)
	pr.w.WriteString(`<border><left/><right/><top/><bottom/><diagonal/></border>`)
	pr.w.WriteString(`</borders>`)
	pr.w.WriteString(`<cellStyleXfs count="1">`)
	pr.w.WriteString(`<xf numFmtId="0" fontId="0" fillId="0" borderId="0"/>`)
	pr.w.WriteString(`</cellStyleXfs>`)

	pr.w.WriteString(`<cellXfs count="`)
	pr.writeInt(len(registry.styles))
	pr.w.WriteString(`">`)
	for _, st := range registry.styles {
		fi := 0
		for i, fk := range registry.fonts {
			if fk.bold == st.Bold && fk.italic == st.Italic && fk.underline == st.Underline && fk.strikethrough == st.Strikethrough {
				fi = i
				break
			}
		}
		nfID := registry.numFmtIDFor(st.NumFmt)
		if nfID != 0 {
			pr.w.WriteString(`<xf numFmtId="`)
			pr.writeInt(nfID)
			pr.w.WriteString(`" fontId="`)
			pr.writeInt(fi)
			pr.w.WriteString(`" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"`)
		} else {
			pr.w.WriteString(`<xf numFmtId="0" fontId="`)
			pr.writeInt(fi)
			pr.w.WriteString(`" fillId="0" borderId="0" xfId="0"`)
		}
		if st.Bold || st.Italic || st.Underline || st.Strikethrough {
			pr.w.WriteString(` applyFont="1"`)
		}
		if st.Align != sheet.AlignGeneral || st.Wrap {
			pr.w.WriteString(` applyAlignment="1"><alignment`)
			if st.Align != sheet.AlignGeneral {
				alignStr := ""
				switch st.Align {
				case sheet.AlignLeft:
					alignStr = "left"
				case sheet.AlignCenter:
					alignStr = "center"
				case sheet.AlignRight:
					alignStr = "right"
				}
				pr.w.WriteString(` horizontal="`)
				pr.w.WriteString(alignStr)
				pr.w.WriteString(`"`)
			}
			if st.Wrap {
				pr.w.WriteString(` wrapText="1"`)
			}
			pr.w.WriteString(`/></xf>`)
		} else {
			pr.w.WriteString(`/>`)
		}
	}
	pr.w.WriteString(`</cellXfs>`)
	pr.w.WriteString(`<cellStyles count="1"><cellStyle name="Normal" xfId="0" builtinId="0"/></cellStyles>`)
	pr.w.WriteString(`<dxfs count="0"/><tableStyles count="0" defaultTableStyle="TableStyleMedium2" defaultPivotStyle="PivotStyleLight16"/><extLst><ext uri="{EB79DEF2-80B8-43e5-95BD-54CBDDF9020C}" xmlns:x14="http://schemas.microsoft.com/office/spreadsheetml/2009/9/main"><x14:slicerStyles defaultSlicerStyle="SlicerStyleLight1"/></ext><ext uri="{9260A510-F301-46a8-8635-F512D64BE5F5}" xmlns:x15="http://schemas.microsoft.com/office/spreadsheetml/2010/11/main"><x15:timelineStyles defaultTimelineStyle="TimeSlicerStyleLight1"/></ext></extLst></styleSheet>`)
	return pr.buf.Bytes()
}
