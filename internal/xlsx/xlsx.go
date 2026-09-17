package xlsx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"tuisheet/internal/sheet"
)

const officeDocumentRelationship = "http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument"

type Workbook struct {
	Sheets []Worksheet
}

type Worksheet struct {
	Name  string
	Sheet *sheet.Sheet
}

type relsXML struct {
	Relationships []relationshipXML `xml:"Relationship"`
}

type relationshipXML struct {
	ID     string `xml:"Id,attr"`
	Type   string `xml:"Type,attr"`
	Target string `xml:"Target,attr"`
}

type workbookXML struct {
	Sheets       []workbookSheetXML `xml:"sheets>sheet"`
	DefinedNames []definedNameXML   `xml:"definedNames>definedName"`
}

type definedNameXML struct {
	Name string `xml:"name,attr"`
	Text string `xml:",chardata"`
}

type workbookSheetXML struct {
	Name string `xml:"name,attr"`
	RID  string `xml:"http://schemas.openxmlformats.org/officeDocument/2006/relationships id,attr"`
}

type sharedStringsXML struct {
	Items []sharedStringXML `xml:"si"`
}

type sharedStringXML struct {
	Text string       `xml:"t"`
	Runs []textRunXML `xml:"r"`
}

type textRunXML struct {
	Text string `xml:"t"`
}

type stylesXML struct {
	NumFmts numFmtsXML `xml:"numFmts"`
	Fonts   []fontXML  `xml:"fonts>font"`
	CellXfs []xfXML    `xml:"cellXfs>xf"`
}

type fontXML struct {
	Bold      *struct{}     `xml:"b"`
	Italic    *struct{}     `xml:"i"`
	Underline *underlineXML `xml:"u"`
	Strike    *struct{}     `xml:"strike"`
}

type underlineXML struct {
	Val string `xml:"val,attr"`
}

type alignXML struct {
	Horizontal string `xml:"horizontal,attr"`
	WrapText   bool   `xml:"wrapText,attr"`
}

type xfXML struct {
	FontID    int       `xml:"fontId,attr"`
	NumFmtID  int       `xml:"numFmtId,attr"`
	Alignment *alignXML `xml:"alignment"`
}

type numFmtsXML struct {
	Formats []numFmtXML `xml:"numFmt"`
}

type numFmtXML struct {
	ID         int    `xml:"numFmtId,attr"`
	FormatCode string `xml:"formatCode,attr"`
}

// colXML corresponds to the CT_Col element (ISO/IEC 29500-1 §18.3.1.13).
// Min/Max are 1-based inclusive column bounds; Width is in character units.
type colXML struct {
	Min         int     `xml:"min,attr"`
	Max         int     `xml:"max,attr"`
	Width       float64 `xml:"width,attr"`
	Hidden      bool    `xml:"hidden,attr"`
	CustomWidth bool    `xml:"customWidth,attr"`
}

type tableXML struct {
	Name    string           `xml:"name,attr"`
	Ref     string           `xml:"ref,attr"`
	Columns []tableColumnXML `xml:"tableColumns>tableColumn"`
}

type tableColumnXML struct {
	Name string `xml:"name,attr"`
}

type mergeCellXML struct {
	Ref string `xml:"ref,attr"`
}

type rowXML struct {
	Index  int       `xml:"r,attr"`
	Hidden bool      `xml:"hidden,attr"`
	Cells  []cellXML `xml:"c"`
}

type formulaXML struct {
	Type string `xml:"t,attr"`
	Ref  string `xml:"ref,attr"`
	SI   string `xml:"si,attr"`
	Text string `xml:",chardata"`
}

type cellXML struct {
	Ref       string       `xml:"r,attr"`
	Type      string       `xml:"t,attr"`
	Style     int          `xml:"s,attr"`
	Value     string       `xml:"v"`
	Formula   formulaXML   `xml:"f"`
	InlineStr inlineStrXML `xml:"is"`
}

type inlineStrXML struct {
	Text string       `xml:"t"`
	Runs []textRunXML `xml:"r"`
}

func Open(filename string) (*Workbook, error) {
	zr, err := zip.OpenReader(filename)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	if err := validateZipFiles(zr.File); err != nil {
		return nil, err
	}

	files := zipFiles(zr.File)
	workbookPath, err := findWorkbookPath(files)
	if err != nil {
		return nil, err
	}
	wb, err := readXML[workbookXML](files, workbookPath)
	if err != nil {
		return nil, fmt.Errorf("read workbook: %w", err)
	}
	if len(wb.Sheets) > maxWorkbookSheets {
		return nil, fmt.Errorf("workbook contains %d sheets; maximum is %d", len(wb.Sheets), maxWorkbookSheets)
	}
	rels, err := readWorkbookRels(files, workbookPath)
	if err != nil {
		return nil, err
	}
	sharedStrings, err := readSharedStrings(files)
	if err != nil {
		return nil, err
	}
	styles, err := readStyles(files)
	if err != nil {
		return nil, err
	}

	out := &Workbook{}
	for _, wbSheet := range wb.Sheets {
		target, ok := rels[wbSheet.RID]
		if !ok {
			return nil, fmt.Errorf("worksheet relationship %q not found", wbSheet.RID)
		}
		wsPath := resolvePartPath(path.Dir(workbookPath), target)
		ws, err := readWorksheet(files, wsPath, sharedStrings, styles)
		if err != nil {
			return nil, fmt.Errorf("read worksheet %q: %w", wbSheet.Name, err)
		}
		name := wbSheet.Name
		if name == "" {
			name = fmt.Sprintf("Sheet%d", len(out.Sheets)+1)
		}
		// Read tables associated with this worksheet
		if err := readSheetTables(files, wsPath, ws); err != nil {
			return nil, fmt.Errorf("read tables for %q: %w", name, err)
		}
		out.Sheets = append(out.Sheets, Worksheet{Name: name, Sheet: ws})
	}
	if len(out.Sheets) == 0 {
		return nil, fmt.Errorf("workbook contains no sheets")
	}
	wireSheetResolvers(out.Sheets)
	wireDefinedNames(out.Sheets, wb.DefinedNames)
	return out, nil
}

// wireSheetResolvers connects every sheet's cross-sheet reference lookup to
// the workbook's sheet list (case-insensitive by name).
func wireSheetResolvers(sheets []Worksheet) {
	byName := make(map[string]*sheet.Sheet, len(sheets))
	for i := range sheets {
		byName[strings.ToLower(sheets[i].Name)] = sheets[i].Sheet
	}
	// Global table lookup for structured refs (e.g. RawDataTable on "Raw Data"
	// referenced from "Expected Results").
	byTable := make(map[string]*sheet.TableDef)
	for i := range sheets {
		for _, t := range sheets[i].Sheet.Tables() {
			if _, ok := byTable[strings.ToLower(t.Name)]; !ok {
				cp := t
				byTable[strings.ToLower(t.Name)] = &cp
			}
		}
	}
	for i := range sheets {
		sheets[i].Sheet.SetSheetResolver(func(name string) *sheet.Sheet {
			return byName[strings.ToLower(name)]
		})
		sheets[i].Sheet.SetTableResolver(func(name string) *sheet.TableDef {
			return byTable[strings.ToLower(name)]
		})
	}
}

func wireDefinedNames(sheets []Worksheet, names []definedNameXML) {
	if len(names) == 0 {
		return
	}
	m := make(map[string]string, len(names))
	for _, dn := range names {
		// Defined names may be workbook-global or sheet-local (Sheet: dn.Name
		// contains scope prefix in some files). The spec stores scope via
		// localSheetId attribute; we treat all as workbook-global for now and
		// keep the last definition.
		text := strings.TrimSpace(dn.Text)
		if text == "" {
			continue
		}
		m[dn.Name] = text
	}
	for i := range sheets {
		sheets[i].Sheet.SetDefinedNames(m)
	}
}

// builtinNumFmts maps the predefined number-format IDs (ISO/IEC 29500-1
// §18.8.30) that this application renders distinctly.
var builtinNumFmts = map[int]string{
	2:  "0.00",
	3:  "#,##0",
	4:  "#,##0.00",
	9:  "0%",
	10: "0.00%",
	14: "mm/dd/yyyy",
	15: "d-mmm-yy",
	16: "d-mmm",
	17: "mmm-yy",
	18: "h:mm AM/PM",
	19: "h:mm:ss AM/PM",
	20: "hh:mm",
	21: "hh:mm:ss",
	22: "mm/dd/yyyy hh:mm",
	45: "mm:ss",
	46: "[h]:mm:ss",
	47: "mmss.0",
}

func readStyles(files map[string]*zip.File) ([]sheet.Style, error) {
	if _, ok := files["xl/styles.xml"]; !ok {
		return nil, nil
	}
	doc, err := readXML[stylesXML](files, "xl/styles.xml")
	if err != nil {
		return nil, err
	}
	custom := make(map[int]string)
	for _, nf := range doc.NumFmts.Formats {
		custom[nf.ID] = nf.FormatCode
	}
	fmtCode := func(id int) string {
		if code, ok := custom[id]; ok {
			return code
		}
		return builtinNumFmts[id]
	}
	out := make([]sheet.Style, len(doc.CellXfs))
	for i, xf := range doc.CellXfs {
		s := sheet.Style{}
		if xf.FontID >= 0 && xf.FontID < len(doc.Fonts) {
			font := doc.Fonts[xf.FontID]
			s.Bold = font.Bold != nil
			s.Italic = font.Italic != nil
			if font.Underline != nil && font.Underline.Val != "none" {
				s.Underline = true
			}
			s.Strikethrough = font.Strike != nil
		}
		s.NumFmt = fmtCode(xf.NumFmtID)
		if xf.Alignment != nil {
			switch xf.Alignment.Horizontal {
			case "left":
				s.Align = sheet.AlignLeft
			case "center":
				s.Align = sheet.AlignCenter
			case "right":
				s.Align = sheet.AlignRight
			}
			s.Wrap = xf.Alignment.WrapText
		}
		out[i] = s
	}
	return out, nil
}

func zipFiles(files []*zip.File) map[string]*zip.File {
	out := make(map[string]*zip.File, len(files))
	for _, f := range files {
		out[f.Name] = f
	}
	return out
}

func findWorkbookPath(files map[string]*zip.File) (string, error) {
	rels, err := readXML[relsXML](files, "_rels/.rels")
	if err != nil {
		if _, ok := files["xl/workbook.xml"]; ok {
			return "xl/workbook.xml", nil
		}
		return "", fmt.Errorf("read package relationships: %w", err)
	}
	for _, rel := range rels.Relationships {
		if rel.Type == officeDocumentRelationship {
			return strings.TrimPrefix(rel.Target, "/"), nil
		}
	}
	if _, ok := files["xl/workbook.xml"]; ok {
		return "xl/workbook.xml", nil
	}
	return "", fmt.Errorf("officeDocument relationship not found")
}

func readWorkbookRels(files map[string]*zip.File, workbookPath string) (map[string]string, error) {
	relsPath := path.Join(path.Dir(workbookPath), "_rels", path.Base(workbookPath)+".rels")
	return readRels(files, relsPath)
}

func readRels(files map[string]*zip.File, relsPath string) (map[string]string, error) {
	doc, err := readXML[relsXML](files, relsPath)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(doc.Relationships))
	for _, rel := range doc.Relationships {
		out[rel.ID] = rel.Target
	}
	return out, nil
}

func readSharedStrings(files map[string]*zip.File) ([]string, error) {
	if _, ok := files["xl/sharedStrings.xml"]; !ok {
		return nil, nil
	}
	doc, err := readXML[sharedStringsXML](files, "xl/sharedStrings.xml")
	if err != nil {
		return nil, err
	}
	if len(doc.Items) > maxSharedStrings {
		return nil, fmt.Errorf("shared strings contains %d items; maximum is %d", len(doc.Items), maxSharedStrings)
	}
	out := make([]string, 0, len(doc.Items))
	for _, item := range doc.Items {
		out = append(out, richText(item.Text, item.Runs))
	}
	return out, nil
}

type sharedFormulaDef struct {
	formula   string
	masterRef string
}

func adjustFormula(formula string, masterRef, targetRef string) (string, error) {
	master, err := sheet.ParseCoord(masterRef)
	if err != nil {
		return "", err
	}
	target, err := sheet.ParseCoord(targetRef)
	if err != nil {
		return "", err
	}
	rowDelta := target.Row - master.Row
	colDelta := target.Col - master.Col
	if rowDelta == 0 && colDelta == 0 {
		return formula, nil
	}

	var buf strings.Builder
	i := 0
	for i < len(formula) {
		ch := formula[i]
		if ch == '"' {
			j := i + 1
			for j < len(formula) {
				if formula[j] == '"' {
					j++
					break
				}
				if formula[j] == '\\' {
					j += 2
				} else {
					j++
				}
			}
			buf.WriteString(formula[i:j])
			i = j
			continue
		}
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch == '$' {
			colAbs := ch == '$'
			colStart := i
			if ch == '$' {
				i++
				if i >= len(formula) {
					buf.WriteByte(ch)
					break
				}
				ch = formula[i]
			}
			if ch < 'A' || ch > 'z' || (ch > 'Z' && ch < 'a') {
				buf.WriteByte(formula[colStart])
				continue
			}
			for i < len(formula) && (formula[i] >= 'A' && formula[i] <= 'Z' || formula[i] >= 'a' && formula[i] <= 'z') {
				i++
			}
			colStr := formula[colStart:i]
			if i >= len(formula) || formula[i] < '0' || formula[i] > '9' {
				buf.WriteString(colStr)
				continue
			}
			rowAbs := formula[i] == '$'
			rowStart := i
			if rowAbs {
				i++
				if i >= len(formula) || formula[i] < '0' || formula[i] > '9' {
					buf.WriteString(formula[rowStart:i])
					continue
				}
			}
			for i < len(formula) && formula[i] >= '0' && formula[i] <= '9' {
				i++
			}
			rowStr := formula[rowStart:i]

			if rowDelta == 0 && colDelta == 0 {
				buf.WriteString(colStr)
				buf.WriteString(rowStr)
				continue
			}

			colNum := 0
			colPart := colStr
			if colAbs {
				colPart = colStr[1:]
			}
			for _, r := range colPart {
				colNum = colNum*26 + int(r-'A'+1)
			}
			colNum--
			if !colAbs {
				colNum += colDelta
			}
			if colNum < 0 {
				colNum = 0
			}
			newCol := sheet.ColumnName(colNum)
			if colAbs {
				buf.WriteByte('$')
			}
			buf.WriteString(newCol)

			rowNum := 0
			rowPart := rowStr
			if rowAbs {
				rowPart = rowStr[1:]
			}
			fmt.Sscanf(rowPart, "%d", &rowNum)
			if !rowAbs {
				rowNum += rowDelta
			}
			if rowNum < 1 {
				rowNum = 1
			}
			if rowAbs {
				buf.WriteByte('$')
			}
			buf.WriteString(strconv.Itoa(rowNum))
			continue
		}
		buf.WriteByte(ch)
		i++
	}
	return buf.String(), nil
}

// xmlGuard enforces the token and depth budgets while streaming an XML
// part in a single pass, replacing the previous validate-then-parse double
// walk that cost extra seconds on large worksheets.
type xmlGuard struct {
	tokens int
	depth  int
}

func (g *xmlGuard) next(dec *xml.Decoder) (xml.Token, error) {
	if g.tokens >= maxXMLTokens {
		return nil, fmt.Errorf("XML exceeds the %d-token limit", maxXMLTokens)
	}
	g.tokens++
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tok.(type) {
	case xml.StartElement:
		g.depth++
		if g.depth > maxXMLDepth {
			return nil, fmt.Errorf("XML exceeds the %d-element nesting limit", maxXMLDepth)
		}
	case xml.EndElement:
		g.depth--
	}
	return tok, nil
}

// pendingSharedCell defers a shared-formula slave whose master has not
// been streamed yet.
type pendingSharedCell struct {
	coord sheet.Coord
	cell  cellXML
}

// readWorksheet streams the worksheet XML once, building the sheet as rows
// arrive so multi-million-cell parts never materialise as an object tree.
// Row/cell subtrees are decoded with DecodeElement, whose element-skipping
// is iterative, so hostile nesting cannot exhaust the stack.
func readWorksheet(files map[string]*zip.File, filename string, sharedStrings []string, styles []sheet.Style) (*sheet.Sheet, error) {
	f, ok := files[filename]
	if !ok {
		return nil, fmt.Errorf("part %q not found", filename)
	}
	data, err := readZipFile(f)
	if err != nil {
		return nil, err
	}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var g xmlGuard

	out := sheet.New()
	// Freeze pane from <pane xSplit/ySplit> if present (ECMA-376). Quick scan.
	if idx := bytes.Index(data, []byte("<pane")); idx >= 0 {
		end := bytes.Index(data[idx:], []byte(">"))
		if end >= 0 {
			tag := string(data[idx : idx+end])
			xs := extractPaneAttr(tag, "xSplit")
			ys := extractPaneAttr(tag, "ySplit")
			if xs >= 0 || ys >= 0 {
				if xs < 0 {
					xs = 0
				}
				if ys < 0 {
					ys = 0
				}
				out.SetFreeze(ys, xs)
			}
		}
	}
	sharedFormulas := make(map[string]sharedFormulaDef)
	var pending []pendingSharedCell
	var merges []mergeCellXML
	cellCount := 0

	storeCell := func(coord sheet.Coord, cell cellXML) error {
		value, formula, err := cellValue(cell, sharedStrings)
		if err != nil {
			return fmt.Errorf("%s: %w", coord, err)
		}
		isSharedSlave := formula == "" && cell.Formula.Type == "shared" && cell.Formula.SI != ""
		if isSharedSlave {
			if _, known := sharedFormulas[cell.Formula.SI]; !known {
				pending = append(pending, pendingSharedCell{coord: coord, cell: cell})
				return nil
			}
			def := sharedFormulas[cell.Formula.SI]
			adjusted, err := adjustFormula(def.formula, def.masterRef, cell.Ref)
			if err == nil && adjusted != "" {
				formula = "=" + adjusted
			}
		}
		if formula != "" {
			out.SetFormulaForImport(coord, formula, value)
		} else {
			out.SetForImport(coord, value)
		}
		if cell.Style >= 0 && cell.Style < len(styles) {
			out.SetStyle(coord, styles[cell.Style])
		}
		return nil
	}

	processRow := func(row rowXML) error {
		if row.Hidden && row.Index >= 1 && row.Index <= sheet.MaxRows {
			out.HideRow(row.Index - 1)
		}
		if len(row.Cells) == 0 {
			return nil
		}
		if cellCount > maxCellsPerSheet-len(row.Cells) {
			return fmt.Errorf("worksheet contains more than %d cells", maxCellsPerSheet)
		}
		cellCount += len(row.Cells)
		for i := range row.Cells {
			cell := row.Cells[i]
			if cell.Formula.Type == "shared" && strings.TrimSpace(cell.Formula.Text) != "" {
				sharedFormulas[cell.Formula.SI] = sharedFormulaDef{
					formula:   StripXLFN(strings.TrimSpace(cell.Formula.Text)),
					masterRef: cell.Ref,
				}
			}
			coord, err := cellCoord(row.Index, i, cell.Ref)
			if err != nil {
				return err
			}
			if err := storeCell(coord, cell); err != nil {
				return err
			}
		}
		return nil
	}

	applyCol := func(c colXML) error {
		if err := validateColumnDefinition(c); err != nil {
			return err
		}
		for i := c.Min - 1; i <= c.Max-1; i++ {
			if c.Hidden {
				out.HideCol(i)
			}
			if c.CustomWidth && c.Width > 0 {
				w := int(c.Width + 0.5)
				if w < 1 {
					w = 1
				}
				out.SetColWidth(i, w)
			}
		}
		return nil
	}

	for {
		tok, err := g.next(dec)
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "sheetData":
			for {
				tok, err := g.next(dec)
				if err != nil {
					return nil, err
				}
				if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "sheetData" {
					break
				}
				if rse, ok := tok.(xml.StartElement); ok && rse.Name.Local == "row" {
					var row rowXML
					if err := dec.DecodeElement(&row, &rse); err != nil {
						return nil, err
					}
					g.depth-- // DecodeElement consumed this element's end tag
					if err := processRow(row); err != nil {
						return nil, err
					}
				}
			}
		case "cols":
			for {
				tok, err := g.next(dec)
				if err != nil {
					return nil, err
				}
				if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "cols" {
					break
				}
				if cse, ok := tok.(xml.StartElement); ok && cse.Name.Local == "col" {
					var c colXML
					if err := dec.DecodeElement(&c, &cse); err != nil {
						return nil, err
					}
					g.depth-- // DecodeElement consumed this element's end tag
					if err := applyCol(c); err != nil {
						return nil, err
					}
				}
			}
		case "mergeCells":
			for {
				tok, err := g.next(dec)
				if err != nil {
					return nil, err
				}
				if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "mergeCells" {
					break
				}
				if mse, ok := tok.(xml.StartElement); ok && mse.Name.Local == "mergeCell" {
					var m mergeCellXML
					if err := dec.DecodeElement(&m, &mse); err != nil {
						return nil, err
					}
					g.depth-- // DecodeElement consumed this element's end tag
					merges = append(merges, m)
					if len(merges) > maxMergeCells {
						return nil, fmt.Errorf("worksheet contains more than %d merged ranges", maxMergeCells)
					}
				}
			}
		}
	}

	// Shared-formula slaves whose masters appeared later in the stream.
	for _, pc := range pending {
		def := sharedFormulas[pc.cell.Formula.SI]
		value, _, _ := cellValue(pc.cell, sharedStrings)
		adjusted, err := adjustFormula(def.formula, def.masterRef, pc.cell.Ref)
		if err == nil && adjusted != "" {
			out.SetFormulaForImport(pc.coord, "="+adjusted, value)
		}
	}

	for _, merge := range merges {
		r, err := sheet.ParseRange(merge.Ref)
		if err != nil {
			return nil, fmt.Errorf("invalid merge range %q: %w", merge.Ref, err)
		}
		out.AddMerge(r)
	}
	return out, nil
}

func validateColumnDefinition(c colXML) error {
	if c.Min < 1 || c.Max < c.Min || c.Max > sheet.MaxColumns {
		return fmt.Errorf("invalid column range %d:%d", c.Min, c.Max)
	}
	return nil
}

func cellCoord(rowIndex, cellIndex int, ref string) (sheet.Coord, error) {
	if ref != "" {
		return sheet.ParseCoord(ref)
	}
	if rowIndex < 1 || rowIndex > sheet.MaxRows {
		return sheet.Coord{}, fmt.Errorf("cell has no reference and row index")
	}
	if cellIndex < 0 || cellIndex >= sheet.MaxColumns {
		return sheet.Coord{}, fmt.Errorf("cell has no reference and column index")
	}
	return sheet.Coord{Row: rowIndex - 1, Col: cellIndex}, nil
}

func cleanNumber(s string) string {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func cellValue(cell cellXML, sharedStrings []string) (display string, formula string, err error) {
	if ft := strings.TrimSpace(cell.Formula.Text); ft != "" {
		formula = "=" + StripXLFN(ft)
		if strings.TrimSpace(cell.Value) != "" {
			display = strings.TrimSpace(cell.Value)
			if cell.Type == "b" {
				if display == "1" {
					display = "TRUE"
				} else if display == "0" {
					display = "FALSE"
				}
			}
			return
		}
		return
	}
	if cell.Formula.Type == "shared" && cell.Formula.SI != "" {
		if strings.TrimSpace(cell.Value) != "" {
			display = strings.TrimSpace(cell.Value)
		}
		return
	}
	var value string
	switch cell.Type {
	case "s":
		if strings.TrimSpace(cell.Value) == "" {
			return "", "", nil
		}
		i, e := strconv.Atoi(strings.TrimSpace(cell.Value))
		if e != nil {
			return "", "", fmt.Errorf("invalid shared string index %q", cell.Value)
		}
		if i < 0 || i >= len(sharedStrings) {
			return "", "", fmt.Errorf("shared string index %d out of range", i)
		}
		value = sharedStrings[i]
	case "inlineStr":
		value = richText(cell.InlineStr.Text, cell.InlineStr.Runs)
	case "b":
		if strings.TrimSpace(cell.Value) == "1" {
			value = "TRUE"
		} else {
			value = "FALSE"
		}
	case "":
		value = cleanNumber(strings.TrimSpace(cell.Value))
	case "str", "e":
		value = strings.TrimSpace(cell.Value)
	default:
		value = cleanNumber(strings.TrimSpace(cell.Value))
	}
	display = value
	return
}

func richText(text string, runs []textRunXML) string {
	var s string
	if len(runs) == 0 {
		s = text
	} else {
		var out strings.Builder
		for _, run := range runs {
			out.WriteString(run.Text)
		}
		s = out.String()
	}
	return sheet.SanitizeForDisplay(s)
}

func resolvePartPath(base, target string) string {
	if target == "" {
		return target
	}
	if strings.HasPrefix(target, "/") {
		return path.Clean(strings.TrimPrefix(target, "/"))
	}
	return path.Clean(path.Join(base, target))
}

func readSheetTables(files map[string]*zip.File, wsPath string, s *sheet.Sheet) error {
	relsPath := path.Join(path.Dir(wsPath), "_rels", path.Base(wsPath)+".rels")
	rels, err := readRels(files, relsPath)
	if err != nil {
		return nil
	}
	for _, target := range rels {
		if !strings.Contains(target, "tables/") {
			continue
		}
		tablePath := resolvePartPath(path.Dir(wsPath), target)
		tbl, err := readTableDef(files, tablePath)
		if err != nil {
			return err
		}
		s.AddTable(tbl)
	}
	return nil
}

func readTableDef(files map[string]*zip.File, path string) (sheet.TableDef, error) {
	doc, err := readXML[tableXML](files, path)
	if err != nil {
		return sheet.TableDef{}, err
	}
	ref, err := sheet.ParseRange(doc.Ref)
	if err != nil {
		return sheet.TableDef{}, fmt.Errorf("invalid table ref %q: %w", doc.Ref, err)
	}
	cols := make([]sheet.TableColumnDef, len(doc.Columns))
	for i, c := range doc.Columns {
		cols[i] = sheet.TableColumnDef{Name: c.Name}
	}
	return sheet.TableDef{
		Name:    doc.Name,
		Ref:     ref,
		Columns: cols,
	}, nil
}

func readXML[T any](files map[string]*zip.File, filename string) (T, error) {
	var out T
	f, ok := files[filename]
	if !ok {
		return out, fmt.Errorf("part %q not found", filename)
	}
	data, err := readZipFile(f)
	if err != nil {
		return out, err
	}
	if err := validateXML(data); err != nil {
		return out, err
	}
	if err := xml.Unmarshal(data, &out); err != nil {
		return out, err
	}
	return out, nil
}

func extractPaneAttr(tag, name string) int {
	needle := name + `="`
	idx := strings.Index(tag, needle)
	if idx < 0 {
		return -1
	}
	rest := tag[idx+len(needle):]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return -1
	}
	v, err := strconv.Atoi(rest[:end])
	if err != nil {
		return -1
	}
	return v
}

// readWorksheetFromBytes streams a worksheet part from raw XML bytes,
// used by tests to exercise the parser without a full package.
func readWorksheetFromBytes(data string) (*sheet.Sheet, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(data)); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "wsxlsx")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	path := filepath.Join(tmp, "ws.xlsx")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return nil, err
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	files := zipFiles(zr.File)
	return readWorksheet(files, "xl/worksheets/sheet1.xml", []string{""}, nil)
}
