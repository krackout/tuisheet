package sheet

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

var wildcardRegexCache sync.Map // pattern -> *regexp.Regexp

// evalKey identifies a cell being evaluated, qualified by its sheet so
// that cycle detection never confuses two different sheets holding the
// same coordinates (e.g. Checks!A3 vs Data!A3).
type evalKey struct {
	sheet *Sheet
	coord Coord
}

// ---------- Value type (number or string) ----------

type ValueType int

const (
	valNumber ValueType = iota
	valString
	valRange
	valArray
	valEmpty
)

type Value struct {
	Type      ValueType
	Num       float64
	Str       string
	R         Range
	SheetName string // for valRange from INDIRECT/OFFSET cross-sheet refs
	Arr       [][]Value
}

func parseValue(raw string) Value {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Value{Type: valNumber, Num: 0}
	}
	if n, err := strconv.ParseFloat(raw, 64); err == nil {
		return Value{Type: valNumber, Num: n}
	}
	return Value{Type: valString, Str: raw}
}

// ---------- AST node interface ----------

type exprNode interface {
	eval(s *Sheet, current Coord, seen map[evalKey]bool) (Value, error)
}

// ---------- AST node types ----------

type binaryNode struct {
	op    byte
	left  exprNode
	right exprNode
}

func derefSingle(s *Sheet, v Value, seen map[evalKey]bool) (Value, error) {
	if v.Type == valArray {
		if len(v.Arr) == 1 && len(v.Arr[0]) == 1 {
			return v.Arr[0][0], nil
		}
		return Value{}, fmt.Errorf("#VALUE! array used as scalar")
	}
	if v.Type != valRange {
		return v, nil
	}
	if v.R.Start != v.R.End {
		return Value{}, fmt.Errorf("#VALUE! range used as scalar")
	}
	t := s
	if v.SheetName != "" && s.resolveSheet != nil {
		if tgt := s.resolveSheet(v.SheetName); tgt != nil {
			t = tgt
		}
	}
	return t.evaluateCell(v.R.Start, seen)
}

func (n *binaryNode) eval(s *Sheet, current Coord, seen map[evalKey]bool) (Value, error) {
	lv, err := n.left.eval(s, current, seen)
	if err != nil {
		return Value{}, err
	}
	if lv.Type == valRange || lv.Type == valArray {
		lv, err = derefSingle(s, lv, seen)
		if err != nil {
			return Value{}, err
		}
	}
	rv, err := n.right.eval(s, current, seen)
	if err != nil {
		return Value{}, err
	}
	if rv.Type == valRange || rv.Type == valArray {
		rv, err = derefSingle(s, rv, seen)
		if err != nil {
			return Value{}, err
		}
	}
	if n.op == '&' {
		return Value{Type: valString, Str: valueText(lv) + valueText(rv)}, nil
	}
	ln, lok := valueAsNumber(lv)
	rn, rok := valueAsNumber(rv)
	if !lok || !rok {
		return Value{}, fmt.Errorf("non-numeric operand for %c", n.op)
	}
	switch n.op {
	case '+':
		return Value{Type: valNumber, Num: ln + rn}, nil
	case '-':
		return Value{Type: valNumber, Num: ln - rn}, nil
	case '*':
		return Value{Type: valNumber, Num: ln * rn}, nil
	case '/':
		if rn == 0 {
			return Value{}, fmt.Errorf("division by zero")
		}
		return Value{Type: valNumber, Num: ln / rn}, nil
	case '^':
		return Value{Type: valNumber, Num: math.Pow(ln, rn)}, nil
	}
	return Value{}, fmt.Errorf("unknown binary operator %c", n.op)
}

type unaryNode struct {
	op    byte
	child exprNode
}

func (n *unaryNode) eval(s *Sheet, current Coord, seen map[evalKey]bool) (Value, error) {
	v, err := n.child.eval(s, current, seen)
	if err != nil {
		return Value{}, err
	}
	if v.Type == valRange || v.Type == valArray {
		v, err = derefSingle(s, v, seen)
		if err != nil {
			return Value{}, err
		}
	}
	nv, ok := valueAsNumber(v)
	if !ok {
		return Value{}, fmt.Errorf("non-numeric operand for unary %c", n.op)
	}
	if n.op == '-' {
		return Value{Type: valNumber, Num: -nv}, nil
	}
	return Value{Type: valNumber, Num: nv}, nil
}

type percentNode struct {
	child exprNode
}

func (n *percentNode) eval(s *Sheet, current Coord, seen map[evalKey]bool) (Value, error) {
	v, err := n.child.eval(s, current, seen)
	if err != nil {
		return Value{}, err
	}
	if v.Type == valRange || v.Type == valArray {
		v, err = derefSingle(s, v, seen)
		if err != nil {
			return Value{}, err
		}
	}
	nv, ok := valueAsNumber(v)
	if !ok {
		return Value{}, fmt.Errorf("non-numeric operand for %%")
	}
	return Value{Type: valNumber, Num: nv / 100}, nil
}

type numNode struct {
	val float64
}

func (n *numNode) eval(_ *Sheet, _ Coord, _ map[evalKey]bool) (Value, error) {
	return Value{Type: valNumber, Num: n.val}, nil
}

type stringNode struct {
	val string
}

func (n *stringNode) eval(_ *Sheet, _ Coord, _ map[evalKey]bool) (Value, error) {
	return Value{Type: valString, Str: n.val}, nil
}

type cellNode struct {
	ref Coord
}

func (n *cellNode) eval(s *Sheet, _ Coord, seen map[evalKey]bool) (Value, error) {
	key := evalKey{sheet: s, coord: n.ref}
	if seen[key] {
		return Value{}, fmt.Errorf("circular reference at %s", n.ref)
	}
	if s.formulas[n.ref] != "" {
		if cached, ok := s.cells[n.ref]; ok && cached != "" && !strings.HasPrefix(cached, "=") {
			return parseValue(cached), nil
		}
	}
	raw := strings.TrimSpace(s.Raw(n.ref))
	if raw == "" {
		return Value{Type: valEmpty}, nil
	}
	if strings.HasPrefix(raw, "=") {
		// Share the cycle-detection map with backtracking instead of
		// copying it per level: a 39k-deep formula chain (=X+11 style)
		// otherwise costs O(n^2) map copies and freezes save.
		seen[key] = true
		defer delete(seen, key)
		v, err := s.evalExpr(strings.TrimSpace(raw[1:]), n.ref, seen)
		if err == nil {
			if v.Type == valNumber {
				s.cells[n.ref] = strconv.FormatFloat(v.Num, 'f', -1, 64)
			} else if v.Type == valString {
				s.cells[n.ref] = v.Str
			}
		}
		return v, err
	}
	return parseValue(raw), nil
}

type cmpNode struct {
	op    string
	left  exprNode
	right exprNode
}

func (n *cmpNode) eval(s *Sheet, current Coord, seen map[evalKey]bool) (Value, error) {
	lv, err := n.left.eval(s, current, seen)
	if err != nil {
		return Value{}, err
	}
	if lv.Type == valRange || lv.Type == valArray {
		lv, err = derefSingle(s, lv, seen)
		if err != nil {
			return Value{}, err
		}
	}
	rv, err := n.right.eval(s, current, seen)
	if err != nil {
		return Value{}, err
	}
	if rv.Type == valRange || rv.Type == valArray {
		rv, err = derefSingle(s, rv, seen)
		if err != nil {
			return Value{}, err
		}
	}

	// Empty cell handling: valEmpty equals "" and 0 per Excel
	if lv.Type == valEmpty || rv.Type == valEmpty {
		if n.op == "=" {
			// empty equals empty, "" , 0
			isEmptyStr := func(v Value) bool { return v.Type == valString && strings.TrimSpace(v.Str) == "" }
			isZeroNum := func(v Value) bool { return v.Type == valNumber && v.Num == 0 }
			if lv.Type == valEmpty && rv.Type == valEmpty {
				return boolValue(true), nil
			}
			if lv.Type == valEmpty && (isEmptyStr(rv) || isZeroNum(rv)) {
				return boolValue(true), nil
			}
			if rv.Type == valEmpty && (isEmptyStr(lv) || isZeroNum(lv)) {
				return boolValue(true), nil
			}
			return boolValue(false), nil
		}
		if n.op == "<>" {
			isEmptyStr := func(v Value) bool { return v.Type == valString && strings.TrimSpace(v.Str) == "" }
			isZeroNum := func(v Value) bool { return v.Type == valNumber && v.Num == 0 }
			eq := false
			if lv.Type == valEmpty && rv.Type == valEmpty {
				eq = true
			} else if lv.Type == valEmpty && (isEmptyStr(rv) || isZeroNum(rv)) {
				eq = true
			} else if rv.Type == valEmpty && (isEmptyStr(lv) || isZeroNum(lv)) {
				eq = true
			}
			if eq {
				return boolValue(false), nil
			}
			return boolValue(true), nil
		}
		// For other ops, treat empty as 0
		var ln, rn float64
		var lok, rok bool
		if lv.Type == valEmpty {
			ln, lok = 0, true
		} else {
			ln, lok = valueAsNumber(lv)
		}
		if rv.Type == valEmpty {
			rn, rok = 0, true
		} else {
			rn, rok = valueAsNumber(rv)
		}
		if !lok || !rok {
			return Value{}, fmt.Errorf("non-numeric operand for %s", n.op)
		}
		switch n.op {
		case "<":
			return boolValue(ln < rn), nil
		case ">":
			return boolValue(ln > rn), nil
		case "<=":
			return boolValue(ln <= rn), nil
		case ">=":
			return boolValue(ln >= rn), nil
		}
		return Value{}, fmt.Errorf("unknown comparison operator %s", n.op)
	}

	// String comparison only for = and <>
	if lv.Type == valString || rv.Type == valString {
		switch n.op {
		case "=":
			if lv.Type == rv.Type && lv.Str == rv.Str {
				return boolValue(true), nil
			}
			return boolValue(false), nil
		case "<>":
			if lv.Type != rv.Type || lv.Str != rv.Str {
				return boolValue(true), nil
			}
			return boolValue(false), nil
		default:
			return Value{}, fmt.Errorf("cannot compare strings with %s", n.op)
		}
	}

	switch n.op {
	case "=":
		if lv.Num == rv.Num {
			return boolValue(true), nil
		}
		return boolValue(false), nil
	case "<>":
		if lv.Num != rv.Num {
			return boolValue(true), nil
		}
		return boolValue(false), nil
	case "<":
		if lv.Num < rv.Num {
			return boolValue(true), nil
		}
		return boolValue(false), nil
	case ">":
		if lv.Num > rv.Num {
			return boolValue(true), nil
		}
		return boolValue(false), nil
	case "<=":
		if lv.Num <= rv.Num {
			return boolValue(true), nil
		}
		return boolValue(false), nil
	case ">=":
		if lv.Num >= rv.Num {
			return boolValue(true), nil
		}
		return boolValue(false), nil
	}
	return Value{}, fmt.Errorf("unknown comparison operator %s", n.op)
}

type definedNameNode struct {
	name string
}

func (n *definedNameNode) eval(s *Sheet, current Coord, seen map[evalKey]bool) (Value, error) {
	raw, ok := s.lookupDefinedName(n.name)
	if !ok {
		return Value{}, fmt.Errorf("unknown name %q", n.name)
	}
	text := strings.TrimSpace(raw)
	// Defined names from workbook.xml may include leading "=" (e.g. "=TODAY()").
	if strings.HasPrefix(text, "=") {
		text = strings.TrimSpace(text[1:])
	}
	if text == "" {
		return Value{Type: valNumber, Num: 0}, nil
	}
	// Try range forms first: handle quoted sheet prefix.
	if sh, rest, ok := parseQualifiedRangeText(text); ok {
		if r, err := ParseRange(rest); err == nil {
			return Value{Type: valRange, R: r, SheetName: sh}, nil
		}
		if c, err := ParseCoord(rest); err == nil {
			return Value{Type: valRange, R: Range{Start: c, End: c}, SheetName: sh}, nil
		}
	}
	if r, err := ParseRange(text); err == nil {
		return Value{Type: valRange, R: r}, nil
	}
	if c, err := ParseCoord(text); err == nil {
		return Value{Type: valRange, R: Range{Start: c, End: c}}, nil
	}
	// Fallback: evaluate as expression (scalar constant or formula like TODAY()).
	return s.parseFormula(text, current, seen)
}

type structRefNode struct {
	tableName string
	rowSpec   string
	col1      string // single column or range start
	col2      string // range end (empty if single column)
}

func (n *structRefNode) eval(s *Sheet, current Coord, seen map[evalKey]bool) (Value, error) {
	table := s.FindTable(n.tableName)
	if table == nil {
		return Value{}, fmt.Errorf("table %q not found", n.tableName)
	}
	// Bare table specifiers like Table[#Data], Table[#Headers], Table[#All],
	// Table[#Totals] refer to whole-table ranges, not a single cell.
	if n.col1 == "" {
		switch strings.ToLower(n.rowSpec) {
		case "#data":
			// Data body without header (and without totals if present — our table
			// ref includes header but no totals row in this workbook).
			if table.Ref.RowSpan() <= 1 {
				return Value{}, fmt.Errorf("table %q has no data rows", n.tableName)
			}
			r := Range{
				Start: Coord{Row: table.Ref.Start.Row + 1, Col: table.Ref.Start.Col},
				End:   table.Ref.End,
			}
			return Value{Type: valRange, R: r}, nil
		case "#headers":
			r := Range{
				Start: table.Ref.Start,
				End:   Coord{Row: table.Ref.Start.Row, Col: table.Ref.End.Col},
			}
			return Value{Type: valRange, R: r}, nil
		case "#totals":
			r := Range{
				Start: Coord{Row: table.Ref.End.Row, Col: table.Ref.Start.Col},
				End:   table.Ref.End,
			}
			return Value{Type: valRange, R: r}, nil
		case "#all", "", "#this row":
			// #This Row without column is ambiguous — fall through to scalar path
			// only if we have a current row inside the table; otherwise return whole table.
			if strings.ToLower(n.rowSpec) == "#this row" && current.Row >= table.Ref.Start.Row && current.Row <= table.Ref.End.Row {
				// Return whole row slice? For now return the current row's range.
				r := Range{
					Start: Coord{Row: current.Row, Col: table.Ref.Start.Col},
					End:   Coord{Row: current.Row, Col: table.Ref.End.Col},
				}
				return Value{Type: valRange, R: r}, nil
			}
			return Value{Type: valRange, R: table.Ref}, nil
		}
		return Value{}, fmt.Errorf("column %q not found in table %q", n.col1, n.tableName)
	}
	colIdx1 := table.ColumnIndex(n.col1)
	if colIdx1 < 0 {
		return Value{}, fmt.Errorf("column %q not found in table %q", n.col1, n.tableName)
	}

	row := current.Row
	c := Coord{Row: row, Col: colIdx1}

	if n.col2 != "" {
		return Value{}, fmt.Errorf("structured reference range cannot be used as scalar")
	}

	return s.evaluateCell(c, seen)
}

// --- Previous structRefNode approach changed to use evaluateCell ---

// ---------- Func Node ----------

type funcNode struct {
	name string
	args []funcArg
}

type funcArg struct {
	text      string
	isRange   bool
	r         Range
	sheetName string // non-empty for 'Other Sheet'!A1:B2 style arguments
}

// targetSheet resolves the sheet this argument refers to, falling back to
// the evaluating sheet when the reference is local or cannot be resolved.
func (a funcArg) targetSheet(s *Sheet) *Sheet {
	if a.sheetName != "" && s.resolveSheet != nil {
		if t := s.resolveSheet(a.sheetName); t != nil {
			return t
		}
	}
	return s
}

func (n *funcNode) eval(s *Sheet, current Coord, seen map[evalKey]bool) (Value, error) {
	switch n.name {
	case "SUM":
		return s.evalFuncSum(n.args, current, seen)
	case "AVERAGE":
		return s.evalFuncAvg(n.args, current, seen)
	case "COUNT":
		return s.evalFuncCount(n.args, current, seen)
	case "COUNTA":
		return s.evalFuncCountA(n.args, current, seen)
	case "MAX":
		return s.evalFuncMax(n.args, current, seen)
	case "MIN":
		return s.evalFuncMin(n.args, current, seen)
	case "ABS":
		return s.evalFuncAbs(n.args, current, seen)
	case "ROUND":
		return s.evalFuncRound(n.args, current, seen)
	case "IF":
		return s.evalFuncIf(n.args, current, seen)
	case "IFS":
		return s.evalFuncIfs(n.args, current, seen)
	case "ROW":
		return s.evalFuncRow(n.args, current)
	case "ROWS":
		return s.evalFuncRows(n.args, current, seen)
	case "COLUMN":
		return s.evalFuncColumn(n.args, current)
	case "AND":
		return s.evalFuncAnd(n.args, current, seen)
	case "NOT":
		return s.evalFuncNot(n.args, current, seen)
	case "INT":
		return s.evalUnaryMath(n.args, current, seen, math.Floor, "INT")
	case "SQRT":
		return s.evalUnaryMath(n.args, current, seen,
			func(v float64) float64 {
				if v < 0 {
					// handled via NaN -> error below
					return math.NaN()
				}
				return math.Sqrt(v)
			}, "SQRT")
	case "EXP":
		return s.evalUnaryMath(n.args, current, seen, math.Exp, "EXP")
	case "LN":
		return s.evalUnaryMath(n.args, current, seen,
			func(v float64) float64 {
				if v <= 0 {
					return math.NaN()
				}
				return math.Log(v)
			}, "LN")
	case "LOG10":
		return s.evalUnaryMath(n.args, current, seen,
			func(v float64) float64 {
				if v <= 0 {
					return math.NaN()
				}
				return math.Log10(v)
			}, "LOG10")
	case "PI":
		return Value{Type: valNumber, Num: math.Pi}, nil
	case "POWER":
		return s.evalFuncPower(n.args, current, seen)
	case "MOD":
		return s.evalFuncMod(n.args, current, seen)
	case "PRODUCT":
		return s.evalFuncProduct(n.args, current, seen)
	case "ROUNDUP":
		return s.evalRoundDir(n.args, current, seen, true)
	case "ROUNDDOWN", "TRUNC":
		return s.evalRoundDir(n.args, current, seen, false)
	case "EXACT":
		return s.evalFuncExact(n.args, current, seen)
	case "LEFT":
		return s.evalFuncLeftRight(n.args, current, seen, true)
	case "RIGHT":
		return s.evalFuncLeftRight(n.args, current, seen, false)
	case "UPPER":
		return s.evalTextTransform(n.args, current, seen, strings.ToUpper)
	case "LOWER":
		return s.evalTextTransform(n.args, current, seen, strings.ToLower)
	case "TRIM":
		return s.evalTextTransform(n.args, current, seen, excelTrim)
	case "CONCATENATE", "CONCAT":
		return s.evalFuncConcatenate(n.args, current, seen)
	case "ISBLANK":
		return s.evalFuncIsBlank(n.args, current)
	case "ISNUMBER":
		return s.evalFuncIsType(n.args, current, seen, valNumber)
	case "ISTEXT":
		return s.evalFuncIsType(n.args, current, seen, valString)
	case "ISERROR":
		return s.evalFuncIsError(n.args, current, seen)
	case "IFERROR", "IFNA":
		return s.evalFuncIfError(n.args, current, seen)
	case "SUMIF":
		return s.evalFuncSumIf(n.args, current, seen)
	case "AVERAGEIF":
		return s.evalFuncAverageIf(n.args, current, seen)
	case "COUNTIF":
		return s.evalFuncCountIf(n.args, current, seen)
	case "VLOOKUP":
		return s.evalFuncVLookup(n.args, current, seen)
	case "INDEX":
		return s.evalFuncIndex(n.args, current, seen)
	case "MATCH":
		return s.evalFuncMatch(n.args, current, seen)
	case "HLOOKUP":
		return s.evalFuncHLookup(n.args, current, seen)
	case "XLOOKUP":
		return s.evalFuncXLookup(n.args, current, seen)
	case "SUMIFS":
		return s.evalFuncSumIfs(n.args, current, seen)
	case "COUNTIFS":
		return s.evalFuncCountIfs(n.args, current, seen)
	case "OR":
		return s.evalFuncOr(n.args, current, seen)
	case "MID":
		return s.evalFuncMid(n.args, current, seen)
	case "FIND":
		return s.evalFuncFind(n.args, current, seen)
	case "LEN":
		return s.evalFuncLen(n.args, current, seen)
	case "SUBSTITUTE":
		return s.evalFuncSubstitute(n.args, current, seen)
	case "TODAY":
		return evalFuncToday()
	case "NOW":
		return evalFuncNow()
	case "DATE":
		return s.evalFuncDate(n.args, current, seen)
	case "YEAR":
		return s.evalDatePart(n.args, current, seen, func(t time.Time) float64 { return float64(t.Year()) })
	case "MONTH":
		return s.evalDatePart(n.args, current, seen, func(t time.Time) float64 { return float64(t.Month()) })
	case "DAY":
		return s.evalDatePart(n.args, current, seen, func(t time.Time) float64 { return float64(t.Day()) })
	case "WEEKDAY":
		return s.evalFuncWeekday(n.args, current, seen)
	case "DAYS":
		return s.evalFuncDays(n.args, current, seen)
	case "SUBTOTAL":
		return s.evalFuncSubtotal(n.args, current, seen)
	case "PMT":
		return s.evalFuncPMT(n.args, current, seen)
	case "IPMT":
		return s.evalFuncIPMT(n.args, current, seen)
	case "PPMT":
		return s.evalFuncPPMT(n.args, current, seen)
	case "OFFSET":
		return s.evalFuncOffset(n.args, current, seen)
	case "INDIRECT":
		return s.evalFuncIndirect(n.args, current, seen)
	case "LET":
		return s.evalFuncLet(n.args, current, seen)
	case "VALUE":
		return s.evalFuncValue(n.args, current, seen)
	case "TEXT":
		return s.evalFuncText(n.args, current, seen)
	case "FILTER":
		return s.evalFuncFilter(n.args, current, seen)
	case "TEXTJOIN":
		return s.evalFuncTextJoin(n.args, current, seen)
	case "SUMPRODUCT":
		return s.evalFuncSumProduct(n.args, current, seen)
	case "NA":
		return s.evalFuncNA(n.args)
	default:
		return Value{}, fmt.Errorf("unsupported function %s", n.name)
	}
}

// ---------- Recursive descent parser ----------

type parser struct {
	text string
	pos  int
}

func newParser(text string) *parser {
	return &parser{text: text}
}

func (p *parser) parse() (exprNode, error) {
	node, err := p.parseExpr()
	if err != nil {
		return nil, err
	}
	return node, nil
}

func (p *parser) skipWS() {
	for p.pos < len(p.text) && (p.text[p.pos] == ' ' || p.text[p.pos] == '\t') {
		p.pos++
	}
}

func (p *parser) peek() byte {
	p.skipWS()
	if p.pos >= len(p.text) {
		return 0
	}
	return p.text[p.pos]
}

func (p *parser) next() byte {
	p.skipWS()
	if p.pos >= len(p.text) {
		return 0
	}
	b := p.text[p.pos]
	p.pos++
	return b
}

func (p *parser) match(b byte) bool {
	if p.peek() == b {
		p.pos++
		p.skipWS()
		return true
	}
	return false
}

func (p *parser) matchStr(s string) bool {
	p.skipWS()
	if p.pos+len(s) <= len(p.text) && p.text[p.pos:p.pos+len(s)] == s {
		p.pos += len(s)
		return true
	}
	return false
}

// parseExpr = comparison
func (p *parser) parseExpr() (exprNode, error) {
	return p.parseComparison()
}

// parseComparison = additive { ("="|"<>"|"<"|">"|"<="|">=") additive }
func (p *parser) parseComparison() (exprNode, error) {
	left, err := p.parseConcat()
	if err != nil {
		return nil, err
	}
	for {
		p.skipWS()
		var op string
		if p.pos+1 < len(p.text) {
			two := p.text[p.pos : p.pos+2]
			switch two {
			case "<=", ">=", "<>":
				op = two
				p.pos += 2
			}
		}
		if op == "" {
			if p.match('=') {
				op = "="
			} else if p.match('<') {
				op = "<"
			} else if p.match('>') {
				op = ">"
			}
		}
		if op == "" {
			break
		}
		right, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		left = &cmpNode{op: op, left: left, right: right}
	}
	return left, nil
}

// parseConcat = additive { "&" additive }
func (p *parser) parseConcat() (exprNode, error) {
	left, err := p.parseAdditive()
	if err != nil {
		return nil, err
	}
	for p.match('&') {
		right, err := p.parseAdditive()
		if err != nil {
			return nil, err
		}
		left = &binaryNode{op: '&', left: left, right: right}
	}
	return left, nil
}

// parseAdditive = multiplicative { ("+"|"-") multiplicative }
func (p *parser) parseAdditive() (exprNode, error) {
	left, err := p.parseMult()
	if err != nil {
		return nil, err
	}
	for {
		if p.match('+') {
			right, err := p.parseMult()
			if err != nil {
				return nil, err
			}
			left = &binaryNode{op: '+', left: left, right: right}
		} else if p.match('-') {
			right, err := p.parseMult()
			if err != nil {
				return nil, err
			}
			left = &binaryNode{op: '-', left: left, right: right}
		} else {
			break
		}
	}
	return left, nil
}

// parseMult = pow { ("*"|"/") pow }
func (p *parser) parseMult() (exprNode, error) {
	left, err := p.parsePow()
	if err != nil {
		return nil, err
	}
	for {
		if p.match('*') {
			right, err := p.parsePow()
			if err != nil {
				return nil, err
			}
			left = &binaryNode{op: '*', left: left, right: right}
		} else if p.match('/') {
			right, err := p.parsePow()
			if err != nil {
				return nil, err
			}
			left = &binaryNode{op: '/', left: left, right: right}
		} else {
			break
		}
	}
	return left, nil
}

// parsePow = unary [ "^" parsePow ]   (^ right-assoc)
func (p *parser) parsePow() (exprNode, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	if p.match('^') {
		right, err := p.parsePow()
		if err != nil {
			return nil, err
		}
		left = &binaryNode{op: '^', left: left, right: right}
	}
	return left, nil
}

// parseUnary = ("-"|"+") parseUnary | parsePostfix
func (p *parser) parseUnary() (exprNode, error) {
	if p.match('-') {
		child, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &unaryNode{op: '-', child: child}, nil
	}
	if p.match('+') {
		child, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return child, nil
	}
	return p.parsePostfix()
}

// parsePostfix = primary [ "%" ]
func (p *parser) parsePostfix() (exprNode, error) {
	node, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	if p.match('%') {
		return &percentNode{child: node}, nil
	}
	return node, nil
}

// parsePrimary = number | string | cellref | structRef | "(" expr ")" | funcCall
func (p *parser) parsePrimary() (exprNode, error) {
	if p.match('(') {
		node, err := p.parseExpr()
		if err != nil {
			return nil, err
		}
		if !p.match(')') {
			return nil, fmt.Errorf("expected ')' at position %d", p.pos)
		}
		return node, nil
	}

	// String literal
	if p.match('"') {
		start := p.pos
		for p.pos < len(p.text) && p.text[p.pos] != '"' {
			p.pos++
		}
		if p.pos >= len(p.text) {
			return nil, fmt.Errorf("unterminated string literal")
		}
		s := p.text[start:p.pos]
		p.pos++ // consume closing "
		return &stringNode{val: s}, nil
	}

	// Number literal (digit or .)
	if p.pos < len(p.text) && (p.text[p.pos] == '.' || (p.text[p.pos] >= '0' && p.text[p.pos] <= '9')) {
		return p.scanNumber()
	}

	// Quoted cross-sheet reference: 'Sheet Name'!A1 or 'Sheet Name'!A1:B2
	if p.pos < len(p.text) && p.text[p.pos] == '\'' {
		end := strings.IndexByte(p.text[p.pos+1:], '\'')
		if end < 0 {
			return nil, fmt.Errorf("unterminated sheet name")
		}
		sheetName := p.text[p.pos+1 : p.pos+1+end]
		p.pos += end + 2
		if !p.match('!') {
			return nil, fmt.Errorf("expected '!' after sheet name %q", sheetName)
		}
		return p.scanSheetQualifiedCoord(sheetName)
	}

	// Name: could be function call, cell reference, or structured reference
	if p.pos < len(p.text) && (isLetter(p.text[p.pos]) || p.text[p.pos] == '$') {
		name := p.scanName()
		ch := p.peek()
		switch {
		case ch == '(':
			return p.parseFuncCall(name)
		case ch == '[':
			return p.parseStructRef(name)
		case ch == '!':
			p.match('!')
			return p.scanSheetQualifiedCoord(name)
		default:
			// Boolean literals (Excel allows TRUE/FALSE anywhere) — ECMA-376 booleans display as TRUE/FALSE.
			switch strings.ToUpper(name) {
			case "TRUE":
				return &stringNode{val: "TRUE"}, nil
			case "FALSE":
				return &stringNode{val: "FALSE"}, nil
			}
			ref, err := ParseCoord(name)
			if err == nil {
				return &cellNode{ref: ref}, nil
			}
			// Bare name that is not a cell reference — treat as potential
			// defined name; evaluation will resolve it or error as unknown name.
			return &definedNameNode{name: name}, nil
		}
	}

	if p.pos >= len(p.text) {
		return nil, fmt.Errorf("unexpected end of formula")
	}
	return nil, fmt.Errorf("unexpected character %c at position %d", p.text[p.pos], p.pos)
}

func isLetter(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func isDigit(b byte) bool {
	return b >= '0' && b <= '9'
}

func (p *parser) scanName() string {
	start := p.pos
	for p.pos < len(p.text) && (isLetter(p.text[p.pos]) || isDigit(p.text[p.pos]) || p.text[p.pos] == '_' || p.text[p.pos] == '$') {
		p.pos++
	}
	return p.text[start:p.pos]
}

func (p *parser) scanNumber() (exprNode, error) {
	start := p.pos
	sawDot := false

	if p.pos < len(p.text) && p.text[p.pos] == '.' {
		sawDot = true
		p.pos++
	}
	for p.pos < len(p.text) && isDigit(p.text[p.pos]) {
		p.pos++
	}
	if !sawDot && p.pos < len(p.text) && p.text[p.pos] == '.' {
		p.pos++
		for p.pos < len(p.text) && isDigit(p.text[p.pos]) {
			p.pos++
		}
	}
	if p.pos == start || (sawDot && p.pos == start+1 && p.text[start] == '.') {
		return nil, fmt.Errorf("invalid number at position %d", start)
	}
	v, err := strconv.ParseFloat(p.text[start:p.pos], 64)
	if err != nil {
		return nil, fmt.Errorf("invalid number %q", p.text[start:p.pos])
	}
	return &numNode{val: v}, nil
}

// ---------- Structured Reference Parsing ----------

// parseStructRef parses TableName[[spec1],[spec2]] or TableName[[spec1],[col1]:[col2]]
func (p *parser) parseStructRef(name string) (exprNode, error) {
	if !p.match('[') {
		return nil, fmt.Errorf("expected '[' after table name")
	}

	start := p.pos
	depth := 0
	for p.pos < len(p.text) {
		if p.text[p.pos] == '[' {
			depth++
		} else if p.text[p.pos] == ']' {
			if depth == 0 {
				break
			}
			depth--
		}
		p.pos++
	}
	inner := p.text[start:p.pos]
	if p.pos >= len(p.text) || p.text[p.pos] != ']' {
		return nil, fmt.Errorf("unterminated structured reference")
	}
	p.pos++

	// Extract bracket-delimited items. Excel allows both Table[#Data] and
	// Table[[#Data]]; the latter yields inner "[#Data]" while the former
	// yields inner "#Data" directly.
	var items []string
	for i := 0; i < len(inner); i++ {
		if inner[i] == '[' {
			j := i + 1
			for j < len(inner) && inner[j] != ']' {
				j++
			}
			if j < len(inner) {
				items = append(items, inner[i+1:j])
				i = j
			}
		}
	}
	if len(items) == 0 {
		trim := strings.TrimSpace(inner)
		if trim != "" {
			// Single-bracket form like [#Data] or [Column] without nesting
			items = []string{trim}
		}
	}

	var rowSpec, col1, col2 string
	hasColon := strings.Contains(inner, "]:[")

	for _, item := range items {
		item = strings.TrimSpace(item)
		if strings.HasPrefix(item, "#") {
			rowSpec = item
		} else if col1 == "" {
			col1 = item
		} else if col2 == "" {
			col2 = item
		}
	}

	if rowSpec == "" {
		rowSpec = "#This Row"
	}
	if col2 == "" && hasColon {
		if idx := strings.Index(inner, "]:["); idx >= 0 {
			endStart := idx + 3
			if endStart < len(inner) {
				endEnd := endStart
				for endEnd < len(inner) && inner[endEnd] != ']' {
					endEnd++
				}
				col2 = strings.TrimSpace(inner[endStart:endEnd])
			}
		}
	}

	return &structRefNode{
		tableName: name,
		rowSpec:   rowSpec,
		col1:      col1,
		col2:      col2,
	}, nil
}

// parseFuncCall = name "(" [ argList ] ")"
// scanSheetQualifiedCoord reads the A1 / $A$1:$B$2 part after SheetName!.
func (p *parser) scanSheetQualifiedCoord(sheetName string) (exprNode, error) {
	p.skipWS()
	start := p.pos
	for p.pos < len(p.text) {
		ch := p.text[p.pos]
		if isLetter(ch) || isDigit(ch) || ch == '$' || ch == ':' {
			p.pos++
			continue
		}
		break
	}
	token := p.text[start:p.pos]
	if token == "" {
		return nil, fmt.Errorf("expected cell reference after %q!", sheetName)
	}
	if r, ok := tryParseRange(token); ok {
		return &xrefNode{sheet: sheetName, isRange: true, r: r}, nil
	}
	c, err := ParseCoord(token)
	if err != nil {
		return nil, fmt.Errorf("invalid reference %q on sheet %q", token, sheetName)
	}
	return &xrefNode{sheet: sheetName, coord: c}, nil
}

func (p *parser) parseFuncCall(name string) (exprNode, error) {
	p.match('(')

	start := p.pos
	depth := 1
	inString := false
	for p.pos < len(p.text) && depth > 0 {
		ch := p.text[p.pos]
		if ch == '"' {
			inString = !inString
		}
		if !inString {
			switch ch {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth > 0 {
			p.pos++
		}
	}
	argText := p.text[start:p.pos]
	if p.pos >= len(p.text) && depth > 0 {
		return nil, fmt.Errorf("unterminated function call %s", name)
	}
	if p.pos < len(p.text) && p.text[p.pos] == ')' {
		p.pos++
	} else {
		return nil, fmt.Errorf("expected ')' after function %s", name)
	}

	argText = strings.TrimSpace(argText)
	argStrs := splitFormulaArgs(argText)
	if argText == "" {
		// No parentheses content at all: F() has zero arguments.
		return &funcNode{name: strings.ToUpper(name)}, nil
	}
	args := make([]funcArg, 0, len(argStrs))
	for _, as := range argStrs {
		as = strings.TrimSpace(as)
		if as == "" {
			// Preserve omitted arguments (e.g. XLOOKUP(a,b,c,,1)) so
			// positional parameters stay aligned; they evaluate to 0.
			args = append(args, funcArg{})
			continue
		}
		if sh, rest, ok := parseQualifiedRangeText(as); ok {
			// Accept explicit A1:A1 ranges too (Start == End is fine here).
			if r, err := ParseRange(rest); err == nil && strings.Contains(rest, ":") {
				args = append(args, funcArg{text: as, isRange: true, sheetName: sh, r: r})
				continue
			}
			if c, err := ParseCoord(rest); err == nil {
				args = append(args, funcArg{text: as, isRange: true, sheetName: sh, r: Range{Start: c, End: c}})
				continue
			}
		}
		if r, ok := tryParseRange(as); ok {
			args = append(args, funcArg{text: as, isRange: true, r: r})
		} else {
			args = append(args, funcArg{text: as})
		}
	}
	return &funcNode{name: strings.ToUpper(name), args: args}, nil
}

func tryParseRange(text string) (Range, bool) {
	if !strings.Contains(text, ":") {
		return Range{}, false
	}
	r, err := ParseRange(text)
	if err != nil {
		return Range{}, false
	}
	if r.Start == r.End {
		return Range{}, false
	}
	return r, true
}

// parseQualifiedRangeText splits "'Sheet Name'!A1:B2" or "Sheet2!C3" into
// the sheet name and the coordinate/range part.
func parseQualifiedRangeText(text string) (sheetName, rest string, ok bool) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "'") {
		end := strings.Index(text[1:], "'")
		if end < 0 {
			return "", "", false
		}
		sheetName = text[1 : 1+end]
		rest = text[end+2:]
		if !strings.HasPrefix(rest, "!") {
			return "", "", false
		}
		return sheetName, rest[1:], true
	}
	bang := strings.Index(text, "!")
	if bang <= 0 {
		return "", "", false
	}
	name := text[:bang]
	for _, r := range name {
		if r != '_' && r != '$' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return "", "", false
		}
	}
	return name, text[bang+1:], true
}

// xrefNode is a cross-sheet cell reference like 'Beer Prices'!B4 or Data!A1.
type xrefNode struct {
	sheet   string
	isRange bool
	coord   Coord
	r       Range
}

func (n *xrefNode) eval(s *Sheet, current Coord, seen map[evalKey]bool) (Value, error) {
	t := s.resolveTarget(n.sheet)
	if t == nil {
		return Value{}, fmt.Errorf("unknown sheet %q", n.sheet)
	}
	if n.isRange {
		return Value{}, fmt.Errorf("range %s!%s not allowed in this context", n.sheet, n.r.Start.String())
	}
	return t.evaluateCell(n.coord, seen)
}

// ---------- Cell evaluation helper ----------

func (s *Sheet) evaluateCell(c Coord, seen map[evalKey]bool) (Value, error) {
	key := evalKey{sheet: s, coord: c}
	if seen[key] {
		return Value{}, fmt.Errorf("circular reference at %s", c)
	}
	if s.formulas[c] != "" {
		if cached, ok := s.cells[c]; ok && cached != "" && !strings.HasPrefix(cached, "=") {
			return parseValue(cached), nil
		}
	}
	raw := strings.TrimSpace(s.Raw(c))
	if raw == "" {
		return Value{Type: valEmpty}, nil
	}
	if strings.HasPrefix(raw, "=") {
		// Shared-map backtracking, see cellNode.eval above.
		seen[key] = true
		defer delete(seen, key)
		v, err := s.evalExpr(strings.TrimSpace(raw[1:]), c, seen)
		if err == nil {
			if v.Type == valNumber {
				s.cells[c] = strconv.FormatFloat(v.Num, 'f', -1, 64)
			} else if v.Type == valString {
				s.cells[c] = v.Str
			}
		}
		return v, err
	}
	return parseValue(raw), nil
}

// valRange helpers — expand a Value that is a range (from OFFSET/INDIRECT/LET)
// into the underlying cell values. Sheet resolution respects cross-sheet refs.
func (s *Sheet) rangeValuesOf(v Value, current Coord, seen map[evalKey]bool) []float64 {
	if v.Type == valArray {
		return s.arrayNumericValues(v)
	}
	if v.Type != valRange || !v.R.Valid() {
		return nil
	}
	t := s
	if v.SheetName != "" && s.resolveSheet != nil {
		if tgt := s.resolveSheet(v.SheetName); tgt != nil {
			t = tgt
		}
	}
	return t.collectRangeValues(v.R, current, seen)
}

func (s *Sheet) rangeTextsOf(v Value) []string {
	if v.Type == valArray {
		return s.arrayTexts(v)
	}
	if v.Type != valRange || !v.R.Valid() {
		return nil
	}
	t := s
	if v.SheetName != "" && s.resolveSheet != nil {
		if tgt := s.resolveSheet(v.SheetName); tgt != nil {
			t = tgt
		}
	}
	// collectRangeTexts takes seen map; use empty for text-only expansion.
	return t.collectRangeTexts(v.R, map[evalKey]bool{})
}

func (s *Sheet) arrayNumericValues(v Value) []float64 {
	if v.Type != valArray {
		return nil
	}
	var out []float64
	for _, row := range v.Arr {
		for _, cell := range row {
			if cell.Type == valNumber {
				out = append(out, cell.Num)
			}
		}
	}
	return out
}

func (s *Sheet) arrayTexts(v Value) []string {
	if v.Type != valArray {
		return nil
	}
	var out []string
	for _, row := range v.Arr {
		for _, cell := range row {
			if cell.Type == valString {
				out = append(out, cell.Str)
			} else if cell.Type == valNumber {
				out = append(out, strconv.FormatFloat(cell.Num, 'f', -1, 64))
			}
		}
	}
	return out
}

func (s *Sheet) flatArrayValues(v Value) []Value {
	if v.Type != valArray {
		return nil
	}
	var out []Value
	for _, row := range v.Arr {
		out = append(out, row...)
	}
	return out
}

// ---------- Range / StructRef range helpers ----------

// clampToUsed shrinks a range's bottom row to the sheet's last used row;
// blank rows contribute nothing, so this is safe and keeps whole-column
// ranges like A:B from iterating a million empty cells.
func clampToUsed(t *Sheet, r Range) Range {
	if mr := t.maxUsedRow(); r.End.Row > mr {
		r.End.Row = mr
	}
	return r
}

func (s *Sheet) collectRangeValues(r Range, current Coord, seen map[evalKey]bool) []float64 {
	r = clampToUsed(s, r)
	var vals []float64
	for row := r.Start.Row; row <= r.End.Row; row++ {
		for col := r.Start.Col; col <= r.End.Col; col++ {
			c := Coord{Row: row, Col: col}
			if seen[evalKey{s, c}] {
				continue
			}
			raw := strings.TrimSpace(s.Raw(c))
			if raw == "" {
				continue
			}
			v, err := s.evaluateCell(c, seen)
			if err == nil && v.Type == valNumber {
				vals = append(vals, v.Num)
			}
		}
	}
	return vals
}

func (s *Sheet) collectRangeTexts(r Range, seen map[evalKey]bool) []string {
	r = clampToUsed(s, r)
	var texts []string
	for row := r.Start.Row; row <= r.End.Row; row++ {
		for col := r.Start.Col; col <= r.End.Col; col++ {
			c := Coord{Row: row, Col: col}
			if seen[evalKey{s, c}] {
				continue
			}
			raw := strings.TrimSpace(s.Raw(c))
			if raw == "" {
				continue
			}
			texts = append(texts, raw)
		}
	}
	return texts
}

// resolveStructRefArg checks if argText is a structured reference with a
// column range and resolves it to an absolute Range. Returns false otherwise.
func (s *Sheet) resolveStructRefRange(argText string, current Coord) (Range, bool) {
	argText = strings.TrimSpace(argText)
	bracketIdx := strings.Index(argText, "[")
	if bracketIdx < 1 {
		return Range{}, false
	}

	tableName := argText[:bracketIdx]
	for _, r := range tableName {
		if !isLetter(byte(r)) && r != '_' {
			return Range{}, false
		}
	}

	table := s.FindTable(tableName)
	if table == nil {
		return Range{}, false
	}

	inner := argText[bracketIdx:]
	if len(inner) < 2 || inner[0] != '[' {
		return Range{}, false
	}

	depth := 0
	closeIdx := -1
	for i, ch := range inner[1:] {
		switch ch {
		case '[':
			depth++
		case ']':
			if depth == 0 {
				closeIdx = i + 1
				goto foundClose
			}
			depth--
		}
	}
foundClose:
	if closeIdx < 0 {
		return Range{}, false
	}

	content := inner[1:closeIdx]

	var items []string
	for i := 0; i < len(content); i++ {
		if content[i] == '[' {
			j := i + 1
			for j < len(content) && content[j] != ']' {
				j++
			}
			if j < len(content) {
				items = append(items, content[i+1:j])
				i = j
			}
		}
	}

	hasColon := strings.Contains(content, "]:[")
	var col1, col2 string
	for _, item := range items {
		item = strings.TrimSpace(item)
		if !strings.HasPrefix(item, "#") {
			if col1 == "" {
				col1 = item
			} else if col2 == "" {
				col2 = item
			}
		}
	}
	if col2 == "" && hasColon {
		if idx := strings.Index(content, "]:["); idx >= 0 {
			endStart := idx + 3
			if endStart < len(content) {
				endEnd := endStart
				for endEnd < len(content) && content[endEnd] != ']' {
					endEnd++
				}
				col2 = strings.TrimSpace(content[endStart:endEnd])
			}
		}
	}
	if col1 == "" {
		return Range{}, false
	}
	if !hasColon && col2 == "" {
		return Range{}, false
	}

	colIdx1 := table.ColumnIndex(col1)
	if colIdx1 < 0 {
		return Range{}, false
	}
	colIdx2 := table.ColumnIndex(col2)
	if colIdx2 < 0 {
		return Range{}, false
	}

	row := current.Row
	return Range{
		Start: Coord{Row: row, Col: colIdx1},
		End:   Coord{Row: row, Col: colIdx2},
	}, true
}

// ---------- Function implementations ----------

func (s *Sheet) evalFuncSum(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	var total float64
	for _, arg := range args {
		if arg.isRange {
			for _, v := range arg.targetSheet(s).collectRangeValues(arg.r, current, seen) {
				total += v
			}
		} else if arg.text != "" {
			v, err := s.parseFormula(arg.text, current, seen)
			if err != nil {
				return Value{}, err
			}
			if v.Type == valRange || v.Type == valArray {
				for _, n := range s.rangeValuesOf(v, current, seen) {
					total += n
				}
			} else if v.Type == valNumber {
				total += v.Num
			}
		}
	}
	return Value{Type: valNumber, Num: total}, nil
}

func (s *Sheet) evalFuncAvg(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	var total float64
	var count int

	addValues := func(vals []float64) {
		for _, v := range vals {
			total += v
			count++
		}
	}

	for _, arg := range args {
		if arg.isRange {
			addValues(arg.targetSheet(s).collectRangeValues(arg.r, current, seen))
		} else if arg.text != "" {
			// Check for structured reference range
			if r, ok := s.resolveStructRefRange(arg.text, current); ok {
				addValues(s.collectRangeValues(r, current, seen))
			} else {
				v, err := s.parseFormula(arg.text, current, seen)
				if err != nil {
					return Value{}, err
				}
				if v.Type == valRange || v.Type == valArray {
					addValues(s.rangeValuesOf(v, current, seen))
				} else if v.Type == valNumber {
					total += v.Num
					count++
				}
			}
		}
	}

	if count == 0 {
		return Value{}, fmt.Errorf("AVERAGE of empty set")
	}
	return Value{Type: valNumber, Num: total / float64(count)}, nil
}

func (s *Sheet) evalFuncCount(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	var count int
	for _, arg := range args {
		if arg.isRange {
			count += len(arg.targetSheet(s).collectRangeValues(arg.r, current, seen))
		} else if arg.text != "" {
			v, err := s.parseFormula(arg.text, current, seen)
			if err == nil {
				if v.Type == valRange || v.Type == valArray {
					count += len(s.rangeValuesOf(v, current, seen))
				} else if v.Type == valNumber {
					count++
				}
			}
		}
	}
	return Value{Type: valNumber, Num: float64(count)}, nil
}

func (s *Sheet) evalFuncCountA(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	var count int
	for _, arg := range args {
		if arg.isRange {
			texts := arg.targetSheet(s).collectRangeTexts(arg.r, seen)
			count += len(texts)
		} else if arg.text != "" {
			v, err := s.parseFormula(arg.text, current, seen)
			if err == nil && v.Type == valRange {
				count += len(s.rangeTextsOf(v))
				continue
			}
			count++
		}
	}
	return Value{Type: valNumber, Num: float64(count)}, nil
}

func (s *Sheet) evalFuncMax(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	first := true
	var max float64
	for _, arg := range args {
		if arg.isRange {
			for _, v := range arg.targetSheet(s).collectRangeValues(arg.r, current, seen) {
				if first || v > max {
					first = false
					max = v
				}
			}
		} else if arg.text != "" {
			v, err := s.parseFormula(arg.text, current, seen)
			if err != nil {
				return Value{}, err
			}
			if v.Type == valRange || v.Type == valArray {
				for _, n := range s.rangeValuesOf(v, current, seen) {
					if first || n > max {
						first = false
						max = n
					}
				}
			} else if v.Type == valNumber && (first || v.Num > max) {
				first = false
				max = v.Num
			}
		}
	}
	if first {
		return Value{}, fmt.Errorf("MAX of empty set")
	}
	return Value{Type: valNumber, Num: max}, nil
}

func (s *Sheet) evalFuncMin(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	first := true
	var min float64
	for _, arg := range args {
		if arg.isRange {
			for _, v := range arg.targetSheet(s).collectRangeValues(arg.r, current, seen) {
				if first || v < min {
					first = false
					min = v
				}
			}
		} else if arg.text != "" {
			v, err := s.parseFormula(arg.text, current, seen)
			if err != nil {
				return Value{}, err
			}
			if v.Type == valRange || v.Type == valArray {
				for _, n := range s.rangeValuesOf(v, current, seen) {
					if first || n < min {
						first = false
						min = n
					}
				}
			} else if v.Type == valNumber && (first || v.Num < min) {
				first = false
				min = v.Num
			}
		}
	}
	if first {
		return Value{}, fmt.Errorf("MIN of empty set")
	}
	return Value{Type: valNumber, Num: min}, nil
}

func (s *Sheet) evalFuncAbs(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 1 {
		return Value{}, fmt.Errorf("ABS requires 1 argument")
	}
	v, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	if v.Type != valNumber {
		return Value{}, fmt.Errorf("ABS requires numeric argument")
	}
	if v.Num < 0 {
		return Value{Type: valNumber, Num: -v.Num}, nil
	}
	return v, nil
}

func (s *Sheet) evalFuncRound(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 2 {
		return Value{}, fmt.Errorf("ROUND requires 2 arguments")
	}
	v, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	if v.Type != valNumber {
		return Value{}, fmt.Errorf("ROUND requires numeric argument")
	}
	places, err := s.parseFormula(args[1].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	if places.Type != valNumber {
		return Value{}, fmt.Errorf("ROUND requires numeric places")
	}
	pow := math.Pow(10, math.Floor(places.Num))
	return Value{Type: valNumber, Num: math.Round(v.Num*pow) / pow}, nil
}

func (s *Sheet) evalFinancialValues(args []funcArg, current Coord, seen map[evalKey]bool) ([]float64, error) {
	values := make([]float64, len(args))
	for i, arg := range args {
		if arg.isRange {
			return nil, fmt.Errorf("financial function arguments must be scalar")
		}
		v, evalErr := s.parseFormula(arg.text, current, seen)
		if evalErr != nil {
			return nil, evalErr
		}
		if v.Type != valNumber {
			return nil, fmt.Errorf("financial function arguments must be numeric")
		}
		values[i] = v.Num
	}
	return values, nil
}

func (s *Sheet) evalFinancialArgs(args []funcArg, current Coord, seen map[evalKey]bool) (rate float64, per, nper int, pv, fv float64, paymentType int, err error) {
	if len(args) < 4 || len(args) > 6 {
		err = fmt.Errorf("financial function requires 4 to 6 arguments")
		return
	}
	values, err := s.evalFinancialValues(args, current, seen)
	if err != nil {
		return 0, 0, 0, 0, 0, 0, err
	}
	rate = values[0]
	if rate <= -1 {
		err = fmt.Errorf("financial function rate must be greater than -1")
		return
	}
	if math.Trunc(values[1]) != values[1] || math.Trunc(values[2]) != values[2] {
		err = fmt.Errorf("financial function period arguments must be integers")
		return
	}
	per, nper = int(values[1]), int(values[2])
	if per < 1 || nper < 1 || per > nper {
		err = fmt.Errorf("financial function period must be between 1 and nper")
		return
	}
	pv = values[3]
	if len(values) >= 5 {
		fv = values[4]
	}
	if len(values) == 6 {
		if math.Trunc(values[5]) != values[5] || (values[5] != 0 && values[5] != 1) {
			err = fmt.Errorf("financial function type must be 0 or 1")
			return
		}
		paymentType = int(values[5])
	}
	return
}

func financialPayment(rate float64, nper int, pv, fv float64, paymentType int) float64 {
	if rate == 0 {
		return -(pv + fv) / float64(nper)
	}
	factor := math.Pow(1+rate, float64(nper))
	return -rate * (pv*factor + fv) / ((1 + rate*float64(paymentType)) * (factor - 1))
}

func (s *Sheet) evalFuncPMT(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 3 || len(args) > 5 {
		return Value{}, fmt.Errorf("PMT requires 3 to 5 arguments")
	}
	values, err := s.evalFinancialValues(args, current, seen)
	if err != nil {
		return Value{}, err
	}
	rate := values[0]
	if rate <= -1 {
		return Value{}, fmt.Errorf("PMT rate must be greater than -1")
	}
	if math.Trunc(values[1]) != values[1] || values[1] < 1 {
		return Value{}, fmt.Errorf("PMT nper must be a positive integer")
	}
	nper := int(values[1])
	pv := values[2]
	fv := 0.0
	paymentType := 0
	if len(values) >= 4 {
		fv = values[3]
	}
	if len(values) == 5 {
		if math.Trunc(values[4]) != values[4] || (values[4] != 0 && values[4] != 1) {
			return Value{}, fmt.Errorf("PMT type must be 0 or 1")
		}
		paymentType = int(values[4])
	}
	return Value{Type: valNumber, Num: financialPayment(rate, nper, pv, fv, paymentType)}, nil
}

func (s *Sheet) evalFuncIPMT(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	rate, per, nper, pv, fv, paymentType, err := s.evalFinancialArgs(args, current, seen)
	if err != nil {
		return Value{}, err
	}
	return Value{Type: valNumber, Num: financialInterest(rate, per, nper, pv, fv, paymentType)}, nil
}

// financialInterest returns the interest portion of the payment for period
// `per` of an annuity, following the Excel IPMT convention.
func financialInterest(rate float64, per, nper int, pv, fv float64, paymentType int) float64 {
	if rate == 0 || (paymentType == 1 && per == 1) {
		return 0
	}
	payment := financialPayment(rate, nper, pv, fv, paymentType)
	balance := pv
	for period := 1; period < per; period++ {
		if paymentType == 1 && period == 1 {
			balance += payment
			continue
		}
		balance = balance*(1+rate) + payment
	}
	return -balance * rate
}

func (s *Sheet) evalFuncPPMT(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	rate, per, nper, pv, fv, paymentType, err := s.evalFinancialArgs(args, current, seen)
	if err != nil {
		return Value{}, err
	}
	payment := financialPayment(rate, nper, pv, fv, paymentType)
	ipmt := financialInterest(rate, per, nper, pv, fv, paymentType)
	return Value{Type: valNumber, Num: payment - ipmt}, nil
}

func (s *Sheet) evalFuncIf(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 2 {
		return Value{}, fmt.Errorf("IF requires at least 2 arguments")
	}
	cond, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	if cond.Type == valRange || cond.Type == valArray {
		cond, err = derefSingle(s, cond, seen)
		if err != nil {
			return Value{}, err
		}
	}
	isTrue := isTruthy(cond)

	if isTrue {
		return s.parseFormula(args[1].text, current, seen)
	}
	if len(args) >= 3 {
		return s.parseFormula(args[2].text, current, seen)
	}
	return Value{Type: valNumber, Num: 0}, nil
}

// refArgCoord resolves an optional single reference argument (Excel ROW/COLUMN
// convention): no argument means the formula's own cell.
func refArgCoord(args []funcArg, current Coord) (Coord, error) {
	switch len(args) {
	case 0:
		return current, nil
	case 1:
		if args[0].isRange {
			return args[0].r.Start, nil
		}
		c, err := ParseCoord(strings.TrimSpace(args[0].text))
		if err != nil {
			return Coord{}, fmt.Errorf("invalid reference %q", args[0].text)
		}
		return c, nil
	default:
		return Coord{}, fmt.Errorf("function requires at most 1 reference argument")
	}
}

func (s *Sheet) evalFuncRow(args []funcArg, current Coord) (Value, error) {
	c, err := refArgCoord(args, current)
	if err != nil {
		return Value{}, err
	}
	return Value{Type: valNumber, Num: float64(c.Row + 1)}, nil
}

func (s *Sheet) evalFuncColumn(args []funcArg, current Coord) (Value, error) {
	c, err := refArgCoord(args, current)
	if err != nil {
		return Value{}, err
	}
	return Value{Type: valNumber, Num: float64(c.Col + 1)}, nil
}

func (s *Sheet) evalFuncRows(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) != 1 {
		return Value{}, fmt.Errorf("ROWS requires 1 argument")
	}
	// Direct range argument like ROWS(A1:C5) — parser sets isRange.
	if args[0].isRange {
		return Value{Type: valNumber, Num: float64(args[0].r.RowSpan())}, nil
	}
	text := strings.TrimSpace(args[0].text)
	if text == "" {
		return Value{}, fmt.Errorf("ROWS requires a reference argument")
	}
	// Evaluate the argument; OFFSET/INDIRECT/LET may return a valRange.
	v, err := s.parseFormula(text, current, seen)
	if err == nil && v.Type == valRange {
		return Value{Type: valNumber, Num: float64(v.R.RowSpan())}, nil
	}
	// Try to interpret the literal text as a range (e.g. "A1:B5" passed via INDIRECT string).
	if r, err := ParseRange(text); err == nil {
		return Value{Type: valNumber, Num: float64(r.RowSpan())}, nil
	}
	// Single cell reference or scalar/array constant — counts as 1 row.
	if _, err := ParseCoord(text); err == nil {
		return Value{Type: valNumber, Num: 1}, nil
	}
	// Any other scalar (number, string, error-free expression) is 1 row per Excel.
	if err == nil {
		return Value{Type: valNumber, Num: 1}, nil
	}
	return Value{}, err
}

// ---------- shared helpers for the Tier-1/2 function batch ----------

// valueText renders a Value as text; numbers are formatted like MID/FIND do.
func valueText(v Value) string {
	if v.Type == valEmpty {
		return ""
	}
	if v.Type == valNumber {
		return strconv.FormatFloat(v.Num, 'f', -1, 64)
	}
	return v.Str
}

func boolValue(b bool) Value {
	if b {
		return Value{Type: valString, Str: "TRUE"}
	}
	return Value{Type: valString, Str: "FALSE"}
}

// isTruthy follows the IF/OR convention: non-zero numbers and non-empty
// strings are true, with special handling for boolean text TRUE/FALSE.
func isTruthy(v Value) bool {
	if v.Type == valEmpty {
		return false
	}
	if v.Type == valNumber && v.Num != 0 {
		return true
	}
	if v.Type == valString {
		s := strings.TrimSpace(v.Str)
		if s == "" {
			return false
		}
		low := strings.ToLower(s)
		if low == "false" {
			return false
		}
		if low == "true" {
			return true
		}
		return true
	}
	return false
}

func valueAsNumber(v Value) (float64, bool) {
	if v.Type == valEmpty {
		return 0, true
	}
	if v.Type == valNumber {
		return v.Num, true
	}
	if v.Type == valString {
		low := strings.ToLower(strings.TrimSpace(v.Str))
		if low == "true" {
			return 1, true
		}
		if low == "false" {
			return 0, true
		}
		if n, err := strconv.ParseFloat(strings.TrimSpace(v.Str), 64); err == nil {
			return n, true
		}
		if strings.TrimSpace(v.Str) == "" {
			return 0, true
		}
	}
	return 0, false
}

// evalScalarNumber evaluates one argument as a numeric scalar.
// Boolean text TRUE/FALSE is coerced to 1/0 (Excel behaviour).
func (s *Sheet) evalScalarNumber(text string, current Coord, seen map[evalKey]bool) (float64, error) {
	v, err := s.parseFormula(text, current, seen)
	if err != nil {
		return 0, err
	}
	if v.Type == valRange || v.Type == valArray {
		v, err = derefSingle(s, v, seen)
		if err != nil {
			return 0, err
		}
	}
	if v.Type == valString {
		low := strings.ToLower(strings.TrimSpace(v.Str))
		if low == "true" {
			return 1, nil
		}
		if low == "false" {
			return 0, nil
		}
	}
	if v.Type != valNumber {
		return 0, fmt.Errorf("numeric argument required")
	}
	return v.Num, nil
}

// evalUnaryMath applies fn to one numeric scalar argument; NaN results
// become evaluation errors.
func (s *Sheet) evalUnaryMath(args []funcArg, current Coord, seen map[evalKey]bool, fn func(float64) float64, name string) (Value, error) {
	if len(args) < 1 {
		return Value{}, fmt.Errorf("%s requires 1 argument", name)
	}
	v, err := s.evalScalarNumber(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	out := fn(v)
	if math.IsNaN(out) {
		return Value{}, fmt.Errorf("%s produced an invalid result", name)
	}
	return Value{Type: valNumber, Num: out}, nil
}

func (s *Sheet) evalFuncAnd(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) == 0 {
		return Value{}, fmt.Errorf("AND requires at least 1 argument")
	}
	for _, arg := range args {
		v, err := s.parseFormula(arg.text, current, seen)
		if err != nil {
			return Value{}, err
		}
		if !isTruthy(v) {
			return boolValue(false), nil
		}
	}
	return boolValue(true), nil
}

func (s *Sheet) evalFuncNot(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) != 1 {
		return Value{}, fmt.Errorf("NOT requires 1 argument")
	}
	v, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	return boolValue(!isTruthy(v)), nil
}

func (s *Sheet) evalFuncPower(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 2 {
		return Value{}, fmt.Errorf("POWER requires 2 arguments")
	}
	base, err := s.evalScalarNumber(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	exp, err := s.evalScalarNumber(args[1].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	out := math.Pow(base, exp)
	if math.IsNaN(out) {
		return Value{}, fmt.Errorf("POWER produced an invalid result")
	}
	return Value{Type: valNumber, Num: out}, nil
}

// evalFuncMod implements Excel MOD semantics: the result carries the sign
// of the divisor (a - b*Floor(a/b)).
func (s *Sheet) evalFuncMod(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 2 {
		return Value{}, fmt.Errorf("MOD requires 2 arguments")
	}
	a, err := s.evalScalarNumber(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	b, err := s.evalScalarNumber(args[1].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	if b == 0 {
		return Value{}, fmt.Errorf("MOD by zero")
	}
	return Value{Type: valNumber, Num: a - b*math.Floor(a/b)}, nil
}

func (s *Sheet) evalFuncProduct(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	product := 1.0
	any := false
	for _, arg := range args {
		if arg.isRange {
			for _, v := range arg.targetSheet(s).collectRangeValues(arg.r, current, seen) {
				product *= v
				any = true
			}
			continue
		}
		if arg.text == "" {
			continue
		}
		v, err := s.parseFormula(arg.text, current, seen)
		if err != nil {
			return Value{}, err
		}
		if v.Type == valRange || v.Type == valArray {
			for _, n := range s.rangeValuesOf(v, current, seen) {
				product *= n
				any = true
			}
		} else if v.Type == valNumber {
			product *= v.Num
			any = true
		}
	}
	if !any {
		return Value{Type: valNumber, Num: 0}, nil
	}
	return Value{Type: valNumber, Num: product}, nil
}

// evalRoundDir implements ROUNDUP (away from zero) and ROUNDDOWN/TRUNC
// (toward zero). The digits argument is optional and defaults to 0.
func (s *Sheet) evalRoundDir(args []funcArg, current Coord, seen map[evalKey]bool, up bool) (Value, error) {
	name := "ROUNDDOWN"
	if up {
		name = "ROUNDUP"
	}
	if len(args) < 1 {
		return Value{}, fmt.Errorf("%s requires at least 1 argument", name)
	}
	v, err := s.evalScalarNumber(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	digits := 0.0
	if len(args) >= 2 {
		digits, err = s.evalScalarNumber(args[1].text, current, seen)
		if err != nil {
			return Value{}, err
		}
	}
	pow := math.Pow(10, math.Floor(digits))
	scaled := v * pow
	var out float64
	switch {
	case up:
		sign := 1.0
		if scaled < 0 {
			sign = -1.0
		}
		out = sign * math.Ceil(math.Abs(scaled))
	default:
		out = math.Trunc(scaled)
	}
	return Value{Type: valNumber, Num: out / pow}, nil
}

func (s *Sheet) evalFuncExact(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 2 {
		return Value{}, fmt.Errorf("EXACT requires 2 arguments")
	}
	a, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	b, err := s.parseFormula(args[1].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	return boolValue(valueText(a) == valueText(b)), nil
}

// evalFuncLeftRight slices rune-wise so multi-byte text behaves like LEN.
func (s *Sheet) evalFuncLeftRight(args []funcArg, current Coord, seen map[evalKey]bool, fromLeft bool) (Value, error) {
	name := "RIGHT"
	count := 1
	if fromLeft {
		name = "LEFT"
	}
	if len(args) < 1 {
		return Value{}, fmt.Errorf("%s requires at least 1 argument", name)
	}
	tv, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	text := valueText(tv)
	runes := []rune(text)
	if len(args) >= 2 {
		n, err := s.evalScalarNumber(args[1].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		count = int(n)
	}
	if count < 0 {
		count = 0
	}
	if count > len(runes) {
		count = len(runes)
	}
	if fromLeft {
		return Value{Type: valString, Str: string(runes[:count])}, nil
	}
	return Value{Type: valString, Str: string(runes[len(runes)-count:])}, nil
}

// excelTrim strips leading/trailing spaces and collapses internal runs of
// whitespace to a single space, matching Excel's TRIM.
func excelTrim(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func (s *Sheet) evalTextTransform(args []funcArg, current Coord, seen map[evalKey]bool, fn func(string) string) (Value, error) {
	if len(args) < 1 {
		return Value{}, fmt.Errorf("text function requires 1 argument")
	}
	v, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	return Value{Type: valString, Str: fn(valueText(v))}, nil
}

func (s *Sheet) evalFuncConcatenate(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) == 0 {
		return Value{}, fmt.Errorf("CONCATENATE requires at least 1 argument")
	}
	var out strings.Builder
	for _, arg := range args {
		if arg.isRange {
			for _, t := range arg.targetSheet(s).collectRangeTexts(arg.r, seen) {
				out.WriteString(t)
			}
			continue
		}
		if arg.text == "" {
			continue
		}
		v, err := s.parseFormula(arg.text, current, seen)
		if err != nil {
			return Value{}, err
		}
		if v.Type == valRange || v.Type == valArray {
			for _, t := range s.rangeTextsOf(v) {
				out.WriteString(t)
			}
			continue
		}
		out.WriteString(valueText(v))
	}
	return Value{Type: valString, Str: out.String()}, nil
}

// evalFuncIsBlank checks whether a reference points at an empty cell without
// evaluating it. Non-reference arguments are never blank.
func (s *Sheet) evalFuncIsBlank(args []funcArg, current Coord) (Value, error) {
	if len(args) != 1 {
		return Value{}, fmt.Errorf("ISBLANK requires 1 argument")
	}
	arg := args[0]
	if !arg.isRange {
		if _, err := ParseCoord(strings.TrimSpace(arg.text)); err != nil {
			return boolValue(false), nil
		}
	}
	c, err := refArgCoord([]funcArg{arg}, current)
	if err != nil {
		return boolValue(false), nil
	}
	return boolValue(s.Raw(c) == ""), nil
}

// evalFuncIsType reports whether the evaluated argument holds the given type;
// evaluation errors count as neither number nor text.
func (s *Sheet) evalFuncIsType(args []funcArg, current Coord, seen map[evalKey]bool, want ValueType) (Value, error) {
	if len(args) != 1 {
		return Value{}, fmt.Errorf("IS function requires 1 argument")
	}
	v, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return boolValue(false), nil
	}
	return boolValue(v.Type == want), nil
}

// evalFuncIsError catches evaluation errors of its argument instead of
// propagating them, matching Excel's ISERROR behaviour.
func (s *Sheet) evalFuncIsError(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) != 1 {
		return Value{}, fmt.Errorf("ISERROR requires 1 argument")
	}
	_, err := s.parseFormula(args[0].text, current, seen)
	return boolValue(err != nil), nil
}

// evalFuncIfError returns the first argument's value, or the second
// argument's value when the first fails to evaluate (Excel IFERROR; IFNA
// behaves identically here because error kinds are not distinguished).
func (s *Sheet) evalFuncIfError(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 2 {
		return Value{}, fmt.Errorf("IFERROR requires 2 arguments")
	}
	v, err := s.parseFormula(args[0].text, current, seen)
	if err == nil {
		return v, nil
	}
	return s.parseFormula(args[1].text, current, seen)
}

// ---------- Tier 3: criteria functions and lookups ----------

// wildcardRegex converts an Excel pattern with * and ? into a case-
// insensitive anchored regexp; returns false when the text has no wildcards.
func wildcardRegex(pattern string) (*regexp.Regexp, bool) {
	if !strings.ContainsAny(pattern, "*?") {
		return nil, false
	}
	if v, ok := wildcardRegexCache.Load(pattern); ok {
		return v.(*regexp.Regexp), true
	}
	var b strings.Builder
	b.WriteString(`^(?i)`)
	for _, r := range pattern {
		switch r {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, false
	}
	wildcardRegexCache.Store(pattern, re)
	return re, true
}

// matchCriteria implements Excel criteria strings for the *IF family: an
// optional operator prefix (>=, <=, <>, >, <, =), numeric or textual targets,
// case-insensitive equality, and * / ? wildcards.
func matchCriteria(v Value, criteria string) bool {
	criteria = strings.TrimSpace(criteria)
	op := "="
	for _, cand := range []string{">=", "<=", "<>", ">", "<", "="} {
		if strings.HasPrefix(criteria, cand) {
			op = cand
			criteria = strings.TrimSpace(strings.TrimPrefix(criteria, cand))
			break
		}
	}

	if num, err := strconv.ParseFloat(criteria, 64); err == nil {
		if v.Type != valNumber {
			return op == "<>"
		}
		switch op {
		case ">":
			return v.Num > num
		case "<":
			return v.Num < num
		case ">=":
			return v.Num >= num
		case "<=":
			return v.Num <= num
		case "<>":
			return v.Num != num
		default:
			return v.Num == num
		}
	}

	text := strings.ToLower(valueText(v))
	target := strings.ToLower(criteria)
	if re, ok := wildcardRegex(criteria); ok {
		return re.MatchString(valueText(v)) == (op != "<>")
	}
	switch op {
	case ">":
		return text > target
	case "<":
		return text < target
	case ">=":
		return text >= target
	case "<=":
		return text <= target
	case "<>":
		return text != target
	default:
		return text == target
	}
}

// criteriaArgText evaluates the criteria argument to its text form so that
// references like =SUMIF(A:A,">"&B1,...)-style expressions work naturally.
func (s *Sheet) criteriaArgText(arg funcArg, current Coord, seen map[evalKey]bool) (string, error) {
	v, err := s.parseFormula(arg.text, current, seen)
	if err != nil {
		return "", err
	}
	return valueText(v), nil
}

// evalIfFamily walks the criteria range in order; for each matching cell it
// reports the corresponding cell of the target range. target==nil means the
// criteria range itself.
func (s *Sheet) evalIfFamily(args []funcArg, current Coord, seen map[evalKey]bool, mode string) (Value, error) {
	if len(args) < 2 || !args[0].isRange {
		return Value{}, fmt.Errorf("%s requires a range and a criteria", mode)
	}
	criteriaSheet := args[0].targetSheet(s)
	criteriaRange := clampToUsed(criteriaSheet, args[0].r)
	criteria, err := s.criteriaArgText(args[1], current, seen)
	if err != nil {
		return Value{}, err
	}
	var target *Range
	var targetSheet *Sheet
	if len(args) >= 3 {
		if !args[2].isRange {
			return Value{}, fmt.Errorf("%s requires a range as third argument", mode)
		}
		targetSheet = args[2].targetSheet(s)
		t := clampToUsed(targetSheet, args[2].r)
		target = &t
	}

	rows := criteriaRange.End.Row - criteriaRange.Start.Row + 1
	cols := criteriaRange.End.Col - criteriaRange.Start.Col + 1

	sum := 0.0
	count := 0
	matched := 0
	for i := 0; i < rows*cols; i++ {
		rOff, cOff := i/cols, i%cols
		cell := Coord{Row: criteriaRange.Start.Row + rOff, Col: criteriaRange.Start.Col + cOff}
		if strings.TrimSpace(criteriaSheet.Raw(cell)) == "" {
			continue
		}
		v, err := criteriaSheet.evaluateCell(cell, seen)
		if err != nil || !matchCriteria(v, criteria) {
			continue
		}
		matched++
		var tCell Coord
		if target != nil {
			tRows := target.End.Row - target.Start.Row + 1
			tCols := target.End.Col - target.Start.Col + 1
			trOff, tcOff := rOff, cOff
			if tRows == rows && tCols == cols {
				// same shape: direct mapping
			} else if tRows == 1 && cols == tCols {
				trOff, tcOff = 0, cOff
			} else if tCols == 1 && rows == tRows {
				trOff, tcOff = rOff, 0
			}
			if trOff >= tRows || tcOff >= tCols {
				return Value{}, fmt.Errorf("%s: target range shape mismatch", mode)
			}
			tCell = Coord{Row: target.Start.Row + trOff, Col: target.Start.Col + tcOff}
		} else {
			tCell = cell
		}
		tv, err := targetSheetOr(criteriaSheet, targetSheet).evaluateCell(tCell, seen)
		if err != nil || tv.Type != valNumber {
			continue
		}
		sum += tv.Num
		count++
	}

	switch mode {
	case "COUNTIF":
		return Value{Type: valNumber, Num: float64(matched)}, nil
	case "SUMIF":
		return Value{Type: valNumber, Num: sum}, nil
	default: // AVERAGEIF
		if count == 0 {
			return Value{}, fmt.Errorf("AVERAGEIF: no numeric matches (#DIV/0!)")
		}
		return Value{Type: valNumber, Num: sum / float64(count)}, nil
	}
}

// targetSheetOr falls back to the given sheet when the resolved one is nil.
func targetSheetOr(primary, fallback *Sheet) *Sheet {
	if primary != nil {
		return primary
	}
	return fallback
}

func (s *Sheet) evalFuncSumIf(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	return s.evalIfFamily(args, current, seen, "SUMIF")
}

func (s *Sheet) evalFuncAverageIf(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	return s.evalIfFamily(args, current, seen, "AVERAGEIF")
}

func (s *Sheet) evalFuncCountIf(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	return s.evalIfFamily(args, current, seen, "COUNTIF")
}

// lookupEqual compares lookup values: numbers numerically, everything else
// case-insensitively as text.
func lookupEqual(a, b Value) bool {
	if a.Type == valNumber && b.Type == valNumber {
		return a.Num == b.Num
	}
	return strings.EqualFold(valueText(a), valueText(b))
}

// valueLess / valueGreater order two values the way the approximate
// matchers do: numbers numerically, everything else by lowercased text.
func valueLess(a, b Value) bool {
	if a.Type == valNumber && b.Type == valNumber {
		return a.Num < b.Num
	}
	return strings.ToLower(valueText(a)) < strings.ToLower(valueText(b))
}

func valueGreater(a, b Value) bool {
	return valueLess(b, a)
}

// evalBoolArg evaluates a boolean-ish argument: TRUE/FALSE names are
// accepted literally since the expression parser has no boolean literals.
func (s *Sheet) evalBoolArg(arg funcArg, current Coord, seen map[evalKey]bool) (bool, error) {
	switch strings.ToUpper(strings.TrimSpace(arg.text)) {
	case "TRUE":
		return true, nil
	case "FALSE":
		return false, nil
	}
	v, err := s.parseFormula(arg.text, current, seen)
	if err != nil {
		return false, err
	}
	return !(v.Type == valNumber && v.Num == 0), nil
}

func (s *Sheet) evalFuncVLookup(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 3 || !args[1].isRange {
		return Value{}, fmt.Errorf("VLOOKUP requires lookup value, table range and column index")
	}
	lookup, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	tgt := args[1].targetSheet(s)
	table := clampToUsed(tgt, args[1].r)
	colIdx, err := s.evalScalarNumber(args[2].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	colNum := int(colIdx)
	if colNum < 1 {
		return Value{}, fmt.Errorf("VLOOKUP column index must be >= 1")
	}
	if table.Start.Col+colNum-1 > table.End.Col {
		return Value{}, fmt.Errorf("VLOOKUP column index outside table")
	}

	exact := true
	if len(args) >= 4 {
		// Excel's range_lookup: TRUE (default) is approximate, FALSE is exact.
		b, err := s.evalBoolArg(args[3], current, seen)
		if err != nil {
			return Value{}, err
		}
		exact = !b
	}

	resultCol := table.Start.Col + colNum - 1
	best := -1
	for row := table.Start.Row; row <= table.End.Row; row++ {
		keyCell := Coord{Row: row, Col: table.Start.Col}
		if strings.TrimSpace(tgt.Raw(keyCell)) == "" {
			continue
		}
		key, err := tgt.evaluateCell(keyCell, seen)
		if err != nil {
			continue
		}
		if exact {
			if lookupEqual(lookup, key) {
				best = row
				break
			}
			continue
		}
		// Approximate match: last key not greater than the lookup value,
		// assuming the first column is sorted ascending.
		le := false
		if lookup.Type == valNumber && key.Type == valNumber {
			le = key.Num <= lookup.Num
		} else {
			le = strings.ToLower(valueText(key)) <= strings.ToLower(valueText(lookup))
		}
		if le {
			best = row
		} else {
			break
		}
	}
	if best < 0 {
		return Value{}, fmt.Errorf("VLOOKUP: value not found (#N/A)")
	}
	return tgt.evaluateCell(Coord{Row: best, Col: resultCol}, seen)
}

func (s *Sheet) evalFuncIndex(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 2 || !args[0].isRange {
		return Value{}, fmt.Errorf("INDEX requires a range and an index")
	}
	r := args[0].r
	tgt := args[0].targetSheet(s)
	rowNum64, err := s.evalScalarNumber(args[1].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	rowNum, colNum := int(rowNum64), 0
	if len(args) >= 3 {
		col64, err := s.evalScalarNumber(args[2].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		colNum = int(col64)
	}
	rows := r.End.Row - r.Start.Row + 1
	cols := r.End.Col - r.Start.Col + 1
	// Single-row ranges allow INDEX(range, n) to address columns.
	if colNum == 0 && rowNum > 0 && rowNum <= cols && (rows == 1 || rowNum > rows) {
		rowNum, colNum = 1, rowNum
	}
	if colNum == 0 {
		colNum = 1
	}
	if rowNum < 1 || colNum < 1 || rowNum > rows || colNum > cols {
		return Value{}, fmt.Errorf("INDEX: index out of range (#REF!)")
	}
	return tgt.evaluateCell(Coord{Row: r.Start.Row + rowNum - 1, Col: r.Start.Col + colNum - 1}, seen)
}

func (s *Sheet) evalFuncMatch(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 2 || !args[1].isRange {
		return Value{}, fmt.Errorf("MATCH requires a lookup value and a range")
	}
	lookup, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	tgt := args[1].targetSheet(s)
	r := clampToUsed(tgt, args[1].r)
	matchType := 1.0
	if len(args) >= 3 {
		mt, err := s.evalScalarNumber(args[2].text, current, seen)
		if err != nil {
			// Fall back to boolean literals for the match type.
			b, berr := s.evalBoolArg(args[2], current, seen)
			if berr != nil {
				return Value{}, err
			}
			if b {
				mt = 1
			} else {
				mt = 0
			}
		} else {
			matchType = mt
		}
	}

	values := make([]Value, 0, (r.End.Row-r.Start.Row+1)*(r.End.Col-r.Start.Col+1))
	for row := r.Start.Row; row <= r.End.Row; row++ {
		for col := r.Start.Col; col <= r.End.Col; col++ {
			cell := Coord{Row: row, Col: col}
			if strings.TrimSpace(tgt.Raw(cell)) == "" {
				continue
			}
			v, err := tgt.evaluateCell(cell, seen)
			if err != nil {
				continue
			}
			values = append(values, v)
		}
	}

	switch {
	case matchType == 0:
		for i, v := range values {
			if lookupEqual(lookup, v) {
				return Value{Type: valNumber, Num: float64(i + 1)}, nil
			}
		}
	case matchType > 0:
		// Largest value <= lookup, assuming ascending order.
		best := -1
		for i, v := range values {
			le := false
			if lookup.Type == valNumber && v.Type == valNumber {
				le = v.Num <= lookup.Num
			} else {
				le = strings.ToLower(valueText(v)) <= strings.ToLower(valueText(lookup))
			}
			if le {
				best = i
			} else {
				break
			}
		}
		if best >= 0 {
			return Value{Type: valNumber, Num: float64(best + 1)}, nil
		}
	default:
		// Smallest value >= lookup, assuming descending order.
		best := -1
		for i, v := range values {
			ge := false
			if lookup.Type == valNumber && v.Type == valNumber {
				ge = v.Num >= lookup.Num
			} else {
				ge = strings.ToLower(valueText(v)) >= strings.ToLower(valueText(lookup))
			}
			if ge {
				best = i
			} else {
				break
			}
		}
		if best >= 0 {
			return Value{Type: valNumber, Num: float64(best + 1)}, nil
		}
	}
	return Value{}, fmt.Errorf("MATCH: value not found (#N/A)")
}

// evalFuncSubstitute implements SUBSTITUTE(text, old, new, [instance_num]):
// replaces old with new, either everywhere or only at the given 1-based
// occurrence. Case-sensitive per Excel.
func (s *Sheet) evalFuncSubstitute(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 3 || len(args) > 4 {
		return Value{}, fmt.Errorf("SUBSTITUTE requires text, old and new")
	}
	textV, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	oldV, err := s.parseFormula(args[1].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	newV, err := s.parseFormula(args[2].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	text, old, repl := valueText(textV), valueText(oldV), valueText(newV)
	if old == "" {
		return Value{Type: valString, Str: text}, nil
	}
	if len(args) == 4 {
		inst64, err := s.evalScalarNumber(args[3].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		instance := int(inst64)
		if instance < 1 {
			return Value{}, fmt.Errorf("SUBSTITUTE: instance_num must be >= 1")
		}
		count := strings.Count(text, old)
		if instance > count {
			return Value{Type: valString, Str: text}, nil
		}
		idx := -1
		for i := 0; i < instance; i++ {
			next := idx + 1
			off := strings.Index(text[next:], old)
			idx = next + off
		}
		out := text[:idx] + repl + text[idx+len(old):]
		return Value{Type: valString, Str: out}, nil
	}
	return Value{Type: valString, Str: strings.ReplaceAll(text, old, repl)}, nil
}

// ---------- Date and time functions (Excel 1900 serial system) ----------

func evalFuncToday() (Value, error) {
	now := time.Now()
	t := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	return Value{Type: valNumber, Num: timeToSerial(t)}, nil
}

func evalFuncNow() (Value, error) {
	return Value{Type: valNumber, Num: timeToSerial(time.Now())}, nil
}

// evalFuncDate implements DATE(year, month, day) with Excel's overflow
// normalisation: month/day values outside their usual ranges roll into
// neighbouring periods (month 13 of 2020 is January 2021).
func (s *Sheet) evalFuncDate(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 3 {
		return Value{}, fmt.Errorf("DATE requires year, month and day")
	}
	year, err := s.evalScalarNumber(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	month, err := s.evalScalarNumber(args[1].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	day, err := s.evalScalarNumber(args[2].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	y, m, d := int(year), int(month), int(day)
	// Excel treats years 0-1899 as offsets from 1900.
	if y >= 0 && y < 1900 {
		y += 1900
	}
	t := time.Date(y, time.Month(1), 1, 0, 0, 0, 0, time.UTC)
	t = t.AddDate(0, m-1, d-1)
	return Value{Type: valNumber, Num: timeToSerial(t)}, nil
}

// evalDatePart evaluates YEAR/MONTH/DAY on a serial number.
func (s *Sheet) evalDatePart(args []funcArg, current Coord, seen map[evalKey]bool, part func(time.Time) float64) (Value, error) {
	if len(args) < 1 {
		return Value{}, fmt.Errorf("date function requires a serial number")
	}
	serial, err := s.evalScalarNumber(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	t, err := serialToTime(serial)
	if err != nil {
		return Value{}, err
	}
	return Value{Type: valNumber, Num: part(t)}, nil
}

// evalFuncWeekday returns the day of the week using Excel return types:
// 1 (default) Sunday=1..Saturday=7, 2 Monday=1..Sunday=7,
// 3 Monday=0..Sunday=6. Other types are rejected.
func (s *Sheet) evalFuncWeekday(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 1 {
		return Value{}, fmt.Errorf("WEEKDAY requires a serial number")
	}
	serial, err := s.evalScalarNumber(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	t, err := serialToTime(serial)
	if err != nil {
		return Value{}, err
	}
	returnType := 1.0
	if len(args) >= 2 && args[1].text != "" {
		rt, err := s.evalScalarNumber(args[1].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		returnType = rt
	}
	// Go: Sunday=0..Saturday=6
	goDay := int(t.Weekday())
	switch returnType {
	case 1:
		return Value{Type: valNumber, Num: float64(goDay + 1)}, nil
	case 2:
		return Value{Type: valNumber, Num: float64((goDay+6)%7 + 1)}, nil
	case 3:
		return Value{Type: valNumber, Num: float64((goDay + 6) % 7)}, nil
	default:
		return Value{}, fmt.Errorf("WEEKDAY return type %g not supported", returnType)
	}
}

// evalFuncDays returns end - start in whole days between the date parts.
func (s *Sheet) evalFuncDays(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 2 {
		return Value{}, fmt.Errorf("DAYS requires end and start dates")
	}
	end, err := s.evalScalarNumber(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	start, err := s.evalScalarNumber(args[1].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	if _, err := serialToTime(end); err != nil {
		return Value{}, err
	}
	if _, err := serialToTime(start); err != nil {
		return Value{}, err
	}
	diff := math.Floor(end) - math.Floor(start)
	return Value{Type: valNumber, Num: diff}, nil
}

// evalFuncHLookup implements HLOOKUP(lookup, table, row_index,
// [range_lookup]): scans the first row of the table for the lookup key and
// returns the value from the given row of the matching column. Mirrors
// VLOOKUP semantics including approximate matching on sorted keys.
func (s *Sheet) evalFuncHLookup(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 3 || !args[1].isRange {
		return Value{}, fmt.Errorf("HLOOKUP requires lookup value, table range and row index")
	}
	lookup, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	tgt := args[1].targetSheet(s)
	table := clampToUsed(tgt, args[1].r)
	rowIdx, err := s.evalScalarNumber(args[2].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	rowNum := int(rowIdx)
	if rowNum < 1 {
		return Value{}, fmt.Errorf("HLOOKUP row index must be >= 1")
	}
	if table.Start.Row+rowNum-1 > table.End.Row {
		return Value{}, fmt.Errorf("HLOOKUP row index outside table")
	}

	exact := true
	if len(args) >= 4 {
		b, err := s.evalBoolArg(args[3], current, seen)
		if err != nil {
			return Value{}, err
		}
		exact = !b
	}

	resultRow := table.Start.Row + rowNum - 1
	best := -1
	for col := table.Start.Col; col <= table.End.Col; col++ {
		keyCell := Coord{Row: table.Start.Row, Col: col}
		if strings.TrimSpace(tgt.Raw(keyCell)) == "" {
			continue
		}
		key, err := tgt.evaluateCell(keyCell, seen)
		if err != nil {
			continue
		}
		if exact {
			if lookupEqual(lookup, key) {
				best = col
				break
			}
			continue
		}
		le := false
		if lookup.Type == valNumber && key.Type == valNumber {
			le = key.Num <= lookup.Num
		} else {
			le = strings.ToLower(valueText(key)) <= strings.ToLower(valueText(lookup))
		}
		if le {
			best = col
		} else {
			break
		}
	}
	if best < 0 {
		return Value{}, fmt.Errorf("HLOOKUP: value not found (#N/A)")
	}
	return tgt.evaluateCell(Coord{Row: resultRow, Col: best}, seen)
}

// evalFuncXLookup implements XLOOKUP(lookup, lookup_array, return_array,
// [if_not_found], [match_mode], [search_mode]). Both arrays must be one
// dimensional with equal length; binary search modes degrade to linear.
func (s *Sheet) evalFuncXLookup(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 3 || !args[1].isRange || !args[2].isRange {
		return Value{}, fmt.Errorf("XLOOKUP requires a lookup value and two ranges")
	}
	lookup, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	matchMode := 0
	if len(args) >= 5 && args[4].text != "" {
		mm, err := s.evalScalarNumber(args[4].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		matchMode = int(mm)
	}
	reverse := false
	if len(args) >= 6 && args[5].text != "" {
		sm, err := s.evalScalarNumber(args[5].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		reverse = sm < 0
	}

	lookupSheet := args[1].targetSheet(s)
	lk := clampToUsed(lookupSheet, args[1].r)
	retSheet := args[2].targetSheet(s)
	rt := clampToUsed(retSheet, args[2].r)

	type axis struct {
		start, count int
		vertical     bool // true when the array runs down a single column
	}
	mkAxis := func(r Range) (axis, bool) {
		rows := r.End.Row - r.Start.Row + 1
		cols := r.End.Col - r.Start.Col + 1
		switch {
		case rows == 1:
			return axis{r.Start.Col, cols, false}, true
		case cols == 1:
			return axis{r.Start.Row, rows, true}, true
		}
		return axis{}, false
	}
	la, ok := mkAxis(lk)
	if !ok {
		return Value{}, fmt.Errorf("XLOOKUP: lookup array must be one dimensional")
	}
	ra, ok := mkAxis(rt)
	if !ok {
		return Value{}, fmt.Errorf("XLOOKUP: return array must be one dimensional")
	}
	if la.count != ra.count {
		return Value{}, fmt.Errorf("XLOOKUP: array lengths differ")
	}
	retVertical := rt.End.Col == rt.Start.Col

	wild, haveWild := (*regexp.Regexp)(nil), matchMode == 2
	if haveWild {
		wild, haveWild = wildcardRegex(valueText(lookup))
	}

	matches := func(v Value) bool {
		if haveWild {
			if v.Type == valNumber {
				return false
			}
			return wild != nil && wild.MatchString(valueText(v))
		}
		return lookupEqual(lookup, v)
	}

	bestIdx, bestVal, found := -1, Value{}, false
	for i := 0; i < la.count; i++ {
		pos := i
		if reverse {
			pos = la.count - 1 - i
		}
		cell := Coord{Row: lk.Start.Row, Col: lk.Start.Col}
		if la.vertical {
			cell.Row += pos
		} else {
			cell.Col += pos
		}
		if strings.TrimSpace(lookupSheet.Raw(cell)) == "" {
			continue
		}
		v, err := lookupSheet.evaluateCell(cell, seen)
		if err != nil {
			continue
		}
		if matchMode == 0 || matchMode == 2 {
			if matches(v) {
				bestIdx = pos
				found = true
				break
			}
			continue
		}
		// matchMode -1: exact or next smaller; 1: exact or next larger.
		cmp := 99
		if lookup.Type == valNumber && v.Type == valNumber {
			switch {
			case v.Num == lookup.Num:
				cmp = 0
			case v.Num < lookup.Num:
				cmp = -1
			default:
				cmp = 1
			}
		} else {
			vt, lt := strings.ToLower(valueText(v)), strings.ToLower(valueText(lookup))
			switch {
			case vt == lt:
				cmp = 0
			case vt < lt:
				cmp = -1
			default:
				cmp = 1
			}
		}
		if cmp == 0 {
			bestIdx = pos
			found = true
			break
		}
		// matchMode -1 wants the largest value below the lookup; 1 the
		// smallest above. Keep the closest candidate seen so far.
		if cmp == matchMode {
			closer := !found
			if found {
				if matchMode == -1 {
					closer = valueGreater(v, bestVal)
				} else {
					closer = valueLess(v, bestVal)
				}
			}
			if closer {
				bestIdx, bestVal, found = pos, v, true
			}
		}
	}
	if !found {
		if len(args) >= 4 && args[3].text != "" {
			return s.parseFormula(args[3].text, current, seen)
		}
		return Value{}, fmt.Errorf("XLOOKUP: value not found (#N/A)")
	}
	rpos := bestIdx
	rcell := Coord{}
	if retVertical {
		rcell = Coord{Row: rt.Start.Row + rpos, Col: rt.Start.Col}
	} else {
		rcell = Coord{Row: rt.Start.Row, Col: rt.Start.Col + rpos}
	}
	return retSheet.evaluateCell(rcell, seen)
}

// evalMultiCriteria evaluates SUMIFS/COUNTIFS criteria pairs. pairStart is
// 0 for COUNTIFS (all args are pairs) and 1 for SUMIFS (leading sum range).
// It returns the sum-range sheet plus the list of positions where every
// criterion matches, expressed as offsets into the first criteria range.
func (s *Sheet) evalMultiCriteria(args []funcArg, current Coord, seen map[evalKey]bool, mode string) (*Range, *Sheet, map[int]Coord, error) {
	var sumR *Range
	var sumSheet *Sheet
	if mode == "SUMIFS" {
		if len(args) < 3 || !args[0].isRange {
			return nil, nil, nil, fmt.Errorf("SUMIFS requires a sum range and criteria pairs")
		}
		r := clampToUsed(args[0].targetSheet(s), args[0].r)
		sumSheet = args[0].targetSheet(s)
		sumR = &r
		args = args[1:]
	}
	if len(args) < 2 || len(args)%2 != 0 {
		return nil, nil, nil, fmt.Errorf("%s requires criteria range/criteria pairs", mode)
	}
	first := args[0]
	if !first.isRange {
		return nil, nil, nil, fmt.Errorf("%s requires a range as first criteria", mode)
	}
	baseSheet := first.targetSheet(s)
	base := clampToUsed(baseSheet, first.r)
	baseRows := base.End.Row - base.Start.Row + 1
	baseCols := base.End.Col - base.Start.Col + 1

	criteriaTexts := make([]string, len(args)/2)
	sheets := make([]*Sheet, len(args)/2)
	ranges := make([]Range, len(args)/2)
	for p := 0; p*2 < len(args); p++ {
		cr := args[p*2]
		if !cr.isRange {
			return nil, nil, nil, fmt.Errorf("%s requires a range for each criteria", mode)
		}
		txt, err := s.criteriaArgText(args[p*2+1], current, seen)
		if err != nil {
			return nil, nil, nil, err
		}
		sh := cr.targetSheet(s)
		rng := clampToUsed(sh, cr.r)
		if rng.End.Row-rng.Start.Row+1 != baseRows || rng.End.Col-rng.Start.Col+1 != baseCols {
			return nil, nil, nil, fmt.Errorf("%s: criteria range shapes differ", mode)
		}
		criteriaTexts[p] = txt
		sheets[p] = sh
		ranges[p] = rng
	}

	hits := make(map[int]Coord)
	for i := 0; i < baseRows*baseCols; i++ {
		rOff, cOff := i/baseCols, i%baseCols
		okPos := true
		for p := range ranges {
			cell := Coord{Row: ranges[p].Start.Row + rOff, Col: ranges[p].Start.Col + cOff}
			if strings.TrimSpace(sheets[p].Raw(cell)) == "" {
				okPos = false
				break
			}
			v, err := sheets[p].evaluateCell(cell, seen)
			if err != nil || !matchCriteria(v, criteriaTexts[p]) {
				okPos = false
				break
			}
		}
		if okPos {
			hits[i] = Coord{Row: base.Start.Row + rOff, Col: base.Start.Col + cOff}
		}
	}
	return sumR, sumSheet, hits, nil
}

func (s *Sheet) evalFuncCountIfs(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	_, _, hits, err := s.evalMultiCriteria(args, current, seen, "COUNTIFS")
	if err != nil {
		return Value{}, err
	}
	return Value{Type: valNumber, Num: float64(len(hits))}, nil
}

func (s *Sheet) evalFuncSumIfs(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	sumR, sumSheet, hits, err := s.evalMultiCriteria(args, current, seen, "SUMIFS")
	if err != nil {
		return Value{}, err
	}
	sum := 0.0
	for i, baseCell := range hits {
		tCell := baseCell
		if sumR != nil {
			tCols := sumR.End.Col - sumR.Start.Col + 1
			trOff, tcOff := i/tCols, i%tCols
			tCell = Coord{Row: sumR.Start.Row + trOff, Col: sumR.Start.Col + tcOff}
		}
		tv, err := sumSheet.evaluateCell(tCell, seen)
		if err != nil || tv.Type != valNumber {
			continue
		}
		sum += tv.Num
	}
	return Value{Type: valNumber, Num: sum}, nil
}

// evalFuncIfs evaluates condition/value pairs (Excel IFS, ISO/IEC 29500-1
// §18.17.7.3): returns the value of the first pair whose condition is true,
// or #N/A when no condition matches.
func (s *Sheet) evalFuncIfs(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 2 || len(args)%2 != 0 {
		return Value{}, fmt.Errorf("IFS requires an even number of arguments")
	}
	for i := 0; i < len(args); i += 2 {
		cond, err := s.parseFormula(args[i].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		if cond.Type == valRange || cond.Type == valArray {
			cond, err = derefSingle(s, cond, seen)
			if err != nil {
				return Value{}, err
			}
		}
		if !isTruthy(cond) {
			continue
		}
		return s.parseFormula(args[i+1].text, current, seen)
	}
	return Value{}, fmt.Errorf("IFS: no condition matched (#N/A)")
}

func (s *Sheet) evalFuncOr(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	for _, arg := range args {
		if arg.text == "" {
			continue
		}
		v, err := s.parseFormula(arg.text, current, seen)
		if err != nil {
			return Value{}, err
		}
		if v.Type == valRange || v.Type == valArray {
			v, err = derefSingle(s, v, seen)
			if err != nil {
				return Value{}, err
			}
		}
		if isTruthy(v) {
			return boolValue(true), nil
		}
	}
	return boolValue(false), nil
}

func (s *Sheet) evalFuncMid(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 3 {
		return Value{}, fmt.Errorf("MID requires 3 arguments")
	}
	textVal, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	text := textVal.Str
	if textVal.Type == valNumber {
		text = strconv.FormatFloat(textVal.Num, 'f', -1, 64)
	}

	startNum, err := s.parseFormula(args[1].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	if startNum.Type != valNumber {
		return Value{}, fmt.Errorf("MID requires numeric start")
	}
	start := int(startNum.Num) - 1 // 1-indexed to 0-indexed
	if start < 0 {
		start = 0
	}

	countNum, err := s.parseFormula(args[2].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	if countNum.Type != valNumber {
		return Value{}, fmt.Errorf("MID requires numeric count")
	}
	count := int(countNum.Num)
	if count < 0 {
		count = 0
	}

	if start >= len(text) {
		return Value{Type: valString, Str: ""}, nil
	}
	if start+count > len(text) {
		count = len(text) - start
	}
	return Value{Type: valString, Str: text[start : start+count]}, nil
}

func (s *Sheet) evalFuncFind(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 2 {
		return Value{}, fmt.Errorf("FIND requires at least 2 arguments")
	}
	findVal, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	findText := findVal.Str
	if findVal.Type == valNumber {
		findText = strconv.FormatFloat(findVal.Num, 'f', -1, 64)
	}

	withinVal, err := s.parseFormula(args[1].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	withinText := withinVal.Str
	if withinVal.Type == valNumber {
		withinText = strconv.FormatFloat(withinVal.Num, 'f', -1, 64)
	}

	startPos := 0
	if len(args) >= 3 {
		sp, err := s.parseFormula(args[2].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		if sp.Type == valNumber {
			startPos = int(sp.Num) - 1
			if startPos < 0 {
				startPos = 0
			}
		}
	}

	idx := strings.Index(withinText[startPos:], findText)
	if idx < 0 {
		return Value{}, fmt.Errorf("FIND: text not found")
	}
	return Value{Type: valNumber, Num: float64(startPos + idx + 1)}, nil
}

func (s *Sheet) evalFuncLen(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 1 {
		return Value{}, fmt.Errorf("LEN requires 1 argument")
	}
	textVal, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	text := textVal.Str
	if textVal.Type == valNumber {
		text = strconv.FormatFloat(textVal.Num, 'f', -1, 64)
	}
	return Value{Type: valNumber, Num: float64(utf8.RuneCountInString(text))}, nil
}

func (s *Sheet) evalFuncSubtotal(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 2 {
		return Value{}, fmt.Errorf("SUBTOTAL requires at least 2 arguments")
	}

	funcNumVal, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	if funcNumVal.Type != valNumber {
		return Value{}, fmt.Errorf("SUBTOTAL requires numeric function number")
	}
	funcNum := int(funcNumVal.Num)

	var result float64
	switch funcNum {
	case 1:
		result = s.evalSubtotalAvg(args[1:], current, seen)
	case 2:
		result = s.evalSubtotalCount(args[1:], current, seen)
	case 3:
		result = s.evalSubtotalCountA(args[1:], current, seen)
	case 4:
		result = s.evalSubtotalMax(args[1:], current, seen)
	case 5:
		result = s.evalSubtotalMin(args[1:], current, seen)
	case 9:
		result = s.evalSubtotalSum(args[1:], current, seen)
	default:
		return Value{}, fmt.Errorf("SUBTOTAL function %d not supported", funcNum)
	}
	return Value{Type: valNumber, Num: result}, nil
}

// ---------- SUBTOTAL helpers ----------

func (s *Sheet) evalSubtotalSum(args []funcArg, current Coord, seen map[evalKey]bool) float64 {
	var total float64
	for _, arg := range args {
		if arg.isRange {
			for _, v := range arg.targetSheet(s).collectRangeValues(arg.r, current, seen) {
				total += v
			}
		} else if arg.text != "" {
			v, err := s.parseFormula(arg.text, current, seen)
			if err == nil && v.Type == valNumber {
				total += v.Num
			}
		}
	}
	return total
}

func (s *Sheet) evalSubtotalAvg(args []funcArg, current Coord, seen map[evalKey]bool) float64 {
	var total float64
	var count int
	for _, arg := range args {
		if arg.isRange {
			for _, v := range arg.targetSheet(s).collectRangeValues(arg.r, current, seen) {
				total += v
				count++
			}
		} else if arg.text != "" {
			v, err := s.parseFormula(arg.text, current, seen)
			if err == nil && v.Type == valNumber {
				total += v.Num
				count++
			}
		}
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func (s *Sheet) evalSubtotalCount(args []funcArg, current Coord, seen map[evalKey]bool) float64 {
	var count int
	for _, arg := range args {
		if arg.isRange {
			count += len(arg.targetSheet(s).collectRangeValues(arg.r, current, seen))
		} else if arg.text != "" {
			v, err := s.parseFormula(arg.text, current, seen)
			if err == nil && v.Type == valNumber {
				count++
			}
		}
	}
	return float64(count)
}

func (s *Sheet) evalSubtotalCountA(args []funcArg, current Coord, seen map[evalKey]bool) float64 {
	var count int
	for _, arg := range args {
		if arg.isRange {
			for row := arg.r.Start.Row; row <= arg.r.End.Row; row++ {
				for col := arg.r.Start.Col; col <= arg.r.End.Col; col++ {
					c := Coord{Row: row, Col: col}
					if !seen[evalKey{s, c}] && strings.TrimSpace(s.Raw(c)) != "" {
						count++
					}
				}
			}
		} else if arg.text != "" {
			count++
		}
	}
	return float64(count)
}

func (s *Sheet) evalSubtotalMax(args []funcArg, current Coord, seen map[evalKey]bool) float64 {
	first := true
	var max float64
	for _, arg := range args {
		if arg.isRange {
			for _, v := range arg.targetSheet(s).collectRangeValues(arg.r, current, seen) {
				if first || v > max {
					first = false
					max = v
				}
			}
		} else if arg.text != "" {
			v, err := s.parseFormula(arg.text, current, seen)
			if err == nil && v.Type == valNumber && (first || v.Num > max) {
				first = false
				max = v.Num
			}
		}
	}
	return max
}

func (s *Sheet) evalSubtotalMin(args []funcArg, current Coord, seen map[evalKey]bool) float64 {
	first := true
	var min float64
	for _, arg := range args {
		if arg.isRange {
			for _, v := range arg.targetSheet(s).collectRangeValues(arg.r, current, seen) {
				if first || v < min {
					first = false
					min = v
				}
			}
		} else if arg.text != "" {
			v, err := s.parseFormula(arg.text, current, seen)
			if err == nil && v.Type == valNumber && (first || v.Num < min) {
				first = false
				min = v.Num
			}
		}
	}
	return min
}

// ---------- Entry points ----------

// parseFormula parses and evaluates a formula expression (text after '=')
func (s *Sheet) parseFormula(expr string, current Coord, seen map[evalKey]bool) (Value, error) {
	if strings.TrimSpace(expr) == "" {
		// Omitted function arguments behave like blank cells (numeric 0).
		return Value{Type: valNumber, Num: 0}, nil
	}
	p := newParser(expr)
	node, err := p.parse()
	if err != nil {
		return Value{}, err
	}
	return node.eval(s, current, seen)
}

// ---------- Formula adjustment after delete/shift operations ----------

func (s *Sheet) adjustAllFormulas(adjustFn func(Coord) (Coord, bool)) {
	for c, f := range s.formulas {
		adjusted := adjustFormulaText(f, adjustFn)
		if adjusted != f {
			s.formulas[c] = adjusted
		}
	}
}

func adjustFormulaText(text string, adjustFn func(Coord) (Coord, bool)) string {
	var out strings.Builder
	inString := false
	i := 0
	for i < len(text) {
		ch := text[i]

		if ch == '"' {
			inString = !inString
			out.WriteByte(ch)
			i++
			continue
		}

		if inString {
			out.WriteByte(ch)
			i++
			continue
		}

		// Skip bracketed content (structured references like Table[[col]])
		if ch == '[' {
			close := strings.IndexByte(text[i:], ']')
			if close >= 0 {
				out.WriteString(text[i : i+close+1])
				i += close + 1
				continue
			}
			out.WriteByte(ch)
			i++
			continue
		}

		if isLetter(ch) {
			start := i
			for i < len(text) && isLetter(text[i]) {
				i++
			}

			rowStart := i
			for i < len(text) && isDigit(text[i]) {
				i++
			}
			rowStr := text[rowStart:i]

			ident := text[start:i]

			if rowStr == "" {
				out.WriteString(ident)
				continue
			}

			// Followed by '(' means it's a function call (e.g. ROW(A1))
			if i < len(text) && text[i] == '(' {
				out.WriteString(ident)
				continue
			}

			if ref, err := ParseCoord(ident); err == nil {
				newRef, ok := adjustFn(ref)
				if ok {
					out.WriteString(newRef.String())
				} else {
					out.WriteString("#REF!")
				}
			} else {
				out.WriteString(ident)
			}
			continue
		}

		out.WriteByte(ch)
		i++
	}
	return out.String()
}

// NumberOf reports whether a Value holds a number, returning it; it lets
// other packages (e.g. xlsx) inspect results without importing the
// unexported type constants.
func NumberOf(v Value) (float64, bool) {
	if v.Type == valNumber {
		return v.Num, true
	}
	return 0, false
}

// evalFuncOffset evaluates OFFSET(reference, rows, cols, [height], [width]).
// reference may be a single cell or a range (funcArg.isRange). Returns a
// valRange Value so that SUM(OFFSET(...)) etc. can expand it. When height/
// width are omitted the size of the base reference is preserved; when
// present they override it. Out-of-bounds produces #REF! style error.
func (s *Sheet) evalFuncOffset(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 3 || len(args) > 5 {
		return Value{}, fmt.Errorf("OFFSET requires 3 to 5 arguments")
	}
	var base Range
	var sheetName string
	if args[0].isRange {
		base = args[0].r
		sheetName = args[0].sheetName
	} else {
		c, err := ParseCoord(strings.TrimSpace(args[0].text))
		if err != nil {
			return Value{}, fmt.Errorf("OFFSET reference %q is not a valid cell reference", args[0].text)
		}
		base = Range{Start: c, End: c}
	}
	rows, err := s.evalScalarNumber(args[1].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	cols, err := s.evalScalarNumber(args[2].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	ri, ci := int(math.Trunc(rows)), int(math.Trunc(cols))
	height := base.RowSpan()
	width := base.ColSpan()
	if len(args) >= 4 {
		h, err := s.evalScalarNumber(args[3].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		height = int(math.Trunc(h))
		if height <= 0 {
			return Value{}, fmt.Errorf("OFFSET height must be positive")
		}
	}
	if len(args) >= 5 {
		w, err := s.evalScalarNumber(args[4].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		width = int(math.Trunc(w))
		if width <= 0 {
			return Value{}, fmt.Errorf("OFFSET width must be positive")
		}
	}
	startRow := base.Start.Row + ri
	startCol := base.Start.Col + ci
	endRow := startRow + height - 1
	endCol := startCol + width - 1
	if startRow < 0 || startCol < 0 || endRow >= MaxRows || endCol >= MaxColumns {
		return Value{}, fmt.Errorf("#REF! OFFSET result outside grid")
	}
	r := Range{Start: Coord{Row: startRow, Col: startCol}, End: Coord{Row: endRow, Col: endCol}}
	if r.RowSpan() == 1 && r.ColSpan() == 1 {
		// Single cell: allow scalar context to dereference directly, but
		// return as range so SUM(OFFSET(...)) still works. The caller
		// decides: parseFormula on a valRange scalar will be caught by
		// Display handling — we keep the range and let valueConsumers
		// expand. However plain "=OFFSET(A1,1,0)" should show the cell's
		// value, not "#VALUE!". So return the range; the Display/Eval
		// scalar unwrapping below handles it.
		// For scalar display we dereference here if the surrounding
		// evaluation expects a scalar: s.evaluateCell will be used by
		// callers that detect valRange of size 1. To make "=OFFSET(...)"
		// display the cell value, we keep as range and handle in
		// eval's scalar fallback? For now return range; see parseFormula
		// scalar handling — if the top-level formula is just OFFSET,
		// Eval will return valRange and Display will dereference.
	}
	return Value{Type: valRange, R: r, SheetName: sheetName}, nil
}

// evalFuncIndirect evaluates INDIRECT(ref_text, [a1]).
// ref_text is a string like "A1", "Sheet2!B3", "A1:C3" (A1-style, default)
// or R1C1-style when a1 is FALSE. Returns a valRange so it composes with
// SUM/AVERAGE/etc. Single-cell ranges will be dereferenced to scalar when
// used outside a range context (Display handles this).
func (s *Sheet) evalFuncIndirect(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 1 || len(args) > 2 {
		return Value{}, fmt.Errorf("INDIRECT requires 1 or 2 arguments")
	}
	rv, err := s.parseFormula(args[0].text, current, seen)
	if err != nil {
		return Value{}, err
	}
	text := valueText(rv)
	text = strings.TrimSpace(text)
	if text == "" {
		return Value{}, fmt.Errorf("INDIRECT requires a non-empty text argument")
	}
	a1 := true
	if len(args) == 2 {
		av, err := s.parseFormula(args[1].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		// Excel: any non-zero number / truthy is A1. Only explicit 0/FALSE is R1C1.
		if av.Type == valNumber {
			a1 = av.Num != 0
		} else {
			a1 = !strings.EqualFold(strings.TrimSpace(av.Str), "false") && av.Str != "0" && av.Str != ""
		}
	}
	// Split off sheet qualifier.
	sheetName := ""
	rest := text
	if sh, r, ok := parseQualifiedRangeText(text); ok {
		sheetName = sh
		rest = r
	} else if idx := strings.Index(text, "!"); idx >= 0 {
		// Malformed sheet qualifier — treat as error.
		return Value{}, fmt.Errorf("INDIRECT invalid sheet qualifier %q", text)
	}
	var rng Range
	if a1 {
		var err error
		rng, err = ParseRange(rest)
		if err != nil {
			// Try single coord (ParseRange handles it, but give nicer error)
			c, cerr := ParseCoord(rest)
			if cerr != nil {
				return Value{}, fmt.Errorf("INDIRECT invalid A1 reference %q", rest)
			}
			rng = Range{Start: c, End: c}
		}
	} else {
		var err error
		rng, err = parseR1C1Range(rest, current)
		if err != nil {
			return Value{}, err
		}
	}
	if !rng.Valid() {
		return Value{}, fmt.Errorf("INDIRECT produced invalid range %q", text)
	}
	return Value{Type: valRange, R: rng, SheetName: sheetName}, nil
}

// parseR1C1Coord parses a single R1C1 token like R5C3, R[2]C[-1], R5C, RC, R[1]C etc.
// Relative bracket forms are offsets from current.
func parseR1C1Coord(tok string, current Coord) (Coord, error) {
	tok = strings.TrimSpace(strings.ToUpper(tok))
	if tok == "" {
		return Coord{}, fmt.Errorf("empty R1C1 token")
	}
	// Remove $ — Excel allows $ in R1C1 too but ignore.
	tok = strings.ReplaceAll(tok, "$", "")
	if !strings.HasPrefix(tok, "R") {
		return Coord{}, fmt.Errorf("R1C1 token must start with R: %q", tok)
	}
	// Find C
	cIdx := strings.Index(tok, "C")
	if cIdx < 0 {
		return Coord{}, fmt.Errorf("R1C1 token missing C: %q", tok)
	}
	rowPart := tok[1:cIdx]
	colPart := tok[cIdx+1:]
	parsePart := func(part string, cur int) (int, error) {
		part = strings.TrimSpace(part)
		if part == "" {
			return cur, nil
		}
		if strings.HasPrefix(part, "[") && strings.HasSuffix(part, "]") {
			inner := part[1 : len(part)-1]
			off, err := strconv.Atoi(strings.TrimSpace(inner))
			if err != nil {
				return 0, fmt.Errorf("invalid R1C1 offset %q", part)
			}
			v := cur + off
			// bounds checked outside
			return v, nil
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 {
			return 0, fmt.Errorf("invalid R1C1 absolute %q", part)
		}
		return n - 1, nil
	}
	r, err := parsePart(rowPart, current.Row)
	if err != nil {
		return Coord{}, err
	}
	c, err := parsePart(colPart, current.Col)
	if err != nil {
		return Coord{}, err
	}
	if r < 0 || r >= MaxRows || c < 0 || c >= MaxColumns {
		return Coord{}, fmt.Errorf("#REF! R1C1 address out of bounds %q", tok)
	}
	return Coord{Row: r, Col: c}, nil
}

func parseR1C1Range(text string, current Coord) (Range, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Range{}, fmt.Errorf("empty R1C1 reference")
	}
	parts := strings.Split(text, ":")
	if len(parts) == 1 {
		c, err := parseR1C1Coord(parts[0], current)
		if err != nil {
			return Range{}, err
		}
		return Range{Start: c, End: c}, nil
	}
	if len(parts) != 2 {
		return Range{}, fmt.Errorf("invalid R1C1 range %q", text)
	}
	a, err := parseR1C1Coord(strings.TrimSpace(parts[0]), current)
	if err != nil {
		return Range{}, err
	}
	b, err := parseR1C1Coord(strings.TrimSpace(parts[1]), current)
	if err != nil {
		return Range{}, err
	}
	// Normalize corners
	sr, er := a.Row, b.Row
	sc, ec := a.Col, b.Col
	if sr > er {
		sr, er = er, sr
	}
	if sc > ec {
		sc, ec = ec, sc
	}
	return Range{Start: Coord{Row: sr, Col: sc}, End: Coord{Row: er, Col: ec}}, nil
}

// evalFuncLet evaluates LET(name1, value1, [name2, value2], ..., calc).
// Names are bare identifiers, matched case-insensitively, evaluated
// sequentially so later bindings may reference earlier ones. The final calc
// is evaluated with those bindings via textual substitution (word-boundary,
// case-insensitive, outside string literals).
func (s *Sheet) evalFuncLet(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 3 || len(args)%2 == 0 {
		return Value{}, fmt.Errorf("LET requires an odd number of arguments (name/value pairs plus calc)")
	}
	letIdentRE := regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)
	bindings := make(map[string]Value, (len(args)-1)/2)
	order := make([]string, 0, (len(args)-1)/2)
	for i := 0; i < len(args)-1; i += 2 {
		rawName := strings.TrimSpace(args[i].text)
		if !letIdentRE.MatchString(rawName) {
			return Value{}, fmt.Errorf("LET invalid name %q", rawName)
		}
		if _, err := ParseCoord(strings.ToUpper(rawName)); err == nil {
			return Value{}, fmt.Errorf("LET name %q is a cell reference", rawName)
		}
		up := strings.ToUpper(rawName)
		for k := range bindings {
			if strings.EqualFold(k, rawName) {
				return Value{}, fmt.Errorf("LET duplicate name %q", rawName)
			}
		}
		// Evaluate value with current bindings substituted so sequential
		// binding works: LET(x,1,y,x+1, ...) .
		exprText := strings.TrimSpace(args[i+1].text)
		if len(bindings) > 0 {
			exprText = substituteLetBindings(exprText, bindings)
		}
		v, err := s.parseFormula(exprText, current, seen)
		if err != nil {
			return Value{}, fmt.Errorf("LET binding %q: %v", rawName, err)
		}
		// Normalize numeric strings? keep Value as is.
		bindings[up] = v
		// Keep original case for diagnostics but store upper for lookup.
		_ = up
		order = append(order, rawName)
		// Also store lower/upper agnostic — map already upper.
	}
	calcText := strings.TrimSpace(args[len(args)-1].text)
	if len(bindings) > 0 {
		calcText = substituteLetBindings(calcText, bindings)
	}
	// If calcText after substitution is still a bare LET-bound name, return it directly.
	if v, ok := bindings[strings.ToUpper(strings.TrimSpace(calcText))]; ok && isBareIdent(calcText) {
		return v, nil
	}
	return s.parseFormula(calcText, current, seen)
}

func isBareIdent(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for i, r := range s {
		if i == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
		} else {
			if r != '_' && r != '.' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				return false
			}
		}
	}
	return true
}

// substituteLetBindings replaces whole-word occurrences of LET names in expr,
// case-insensitively, skipping string literals ("..."). Replacement text is
// the Value rendered as a literal: numbers as decimal, strings quoted.
func substituteLetBindings(expr string, bindings map[string]Value) string {
	if len(bindings) == 0 || strings.TrimSpace(expr) == "" {
		return expr
	}
	// Build upper -> literal map.
	lit := make(map[string]string, len(bindings))
	for k, v := range bindings {
		up := strings.ToUpper(k)
		switch v.Type {
		case valNumber:
			lit[up] = strconv.FormatFloat(v.Num, 'f', -1, 64)
		case valString:
			lit[up] = strconv.Quote(v.Str)
		case valRange:
			// Ranges in LET bindings: inline as A1 text so outer parsing
			// can reconstitute it. Use absolute-ish A1.
			if v.SheetName != "" {
				lit[up] = fmt.Sprintf("'%s'!%s", v.SheetName, v.R.Start.String())
				if v.R.Start != v.R.End {
					lit[up] = fmt.Sprintf("'%s'!%s:%s", v.SheetName, v.R.Start.String(), v.R.End.String())
				}
			} else {
				lit[up] = v.R.Start.String()
				if v.R.Start != v.R.End {
					lit[up] = fmt.Sprintf("%s:%s", v.R.Start.String(), v.R.End.String())
				}
			}
		}
	}
	var out strings.Builder
	i := 0
	inStr := false
	for i < len(expr) {
		ch := expr[i]
		if ch == '"' {
			inStr = !inStr
			out.WriteByte(ch)
			i++
			continue
		}
		if inStr {
			out.WriteByte(ch)
			i++
			continue
		}
		if ch == '_' || unicode.IsLetter(rune(ch)) {
			start := i
			for i < len(expr) && (expr[i] == '_' || expr[i] == '.' || unicode.IsLetter(rune(expr[i])) || unicode.IsDigit(rune(expr[i]))) {
				i++
			}
			word := expr[start:i]
			up := strings.ToUpper(word)
			if rep, ok := lit[up]; ok {
				out.WriteString(rep)
			} else {
				out.WriteString(word)
			}
			continue
		}
		out.WriteByte(ch)
		i++
	}
	return out.String()
}

// evalFuncValue implements VALUE(text) — converts text that looks like a
// number to a number. It trims spaces, strips common currency symbols and
// thousands separators, handles trailing % and parenthesised negatives.
func (s *Sheet) evalFuncValue(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) != 1 {
		return Value{}, fmt.Errorf("VALUE requires 1 argument")
	}
	var v Value
	var err error
	if args[0].isRange {
		if args[0].r.Start != args[0].r.End {
			return Value{}, fmt.Errorf("VALUE range must be a single cell")
		}
		t := args[0].targetSheet(s)
		v, err = t.evaluateCell(args[0].r.Start, seen)
		if err != nil {
			return Value{}, err
		}
	} else {
		v, err = s.parseFormula(args[0].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		if v.Type == valRange || v.Type == valArray {
			v, err = derefSingle(s, v, seen)
			if err != nil {
				return Value{}, err
			}
		}
	}
	if v.Type == valNumber {
		return v, nil
	}
	str := strings.TrimSpace(valueText(v))
	if str == "" {
		return Value{}, fmt.Errorf("#VALUE! VALUE of empty text")
	}
	// Handle parenthesised negatives: (123) -> -123
	negParen := false
	if strings.HasPrefix(str, "(") && strings.HasSuffix(str, ")") {
		negParen = true
		str = strings.TrimSpace(str[1 : len(str)-1])
	}
	// Trailing % → divide by 100 per percentage point
	pct := 0
	for strings.HasSuffix(str, "%") {
		pct++
		str = strings.TrimSpace(strings.TrimSuffix(str, "%"))
	}
	// Strip leading currency symbols ($, €, £, ¥) and signs
	str = strings.TrimSpace(str)
	str = strings.TrimPrefix(str, "$")
	str = strings.TrimPrefix(str, "€")
	str = strings.TrimPrefix(str, "£")
	str = strings.TrimPrefix(str, "¥")
	str = strings.TrimSpace(str)
	// Remove thousands separators (commas and spaces) — but keep decimal dot
	str = strings.ReplaceAll(str, ",", "")
	str = strings.ReplaceAll(str, " ", "")
	if str == "" {
		return Value{}, fmt.Errorf("#VALUE! VALUE could not parse %q", valueText(v))
	}
	num, err := strconv.ParseFloat(str, 64)
	if err != nil {
		return Value{}, fmt.Errorf("#VALUE! VALUE could not parse %q", valueText(v))
	}
	if negParen {
		num = -num
	}
	for i := 0; i < pct; i++ {
		num /= 100
	}
	return Value{Type: valNumber, Num: num}, nil
}

// evalFuncText implements TEXT(value, format_text) — formats a value using
// an Excel-like number format code and returns text. It reuses
// applyNumberFormat so date/time and numeric patterns behave like cell
// formatting.
func (s *Sheet) evalFuncText(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) != 2 {
		return Value{}, fmt.Errorf("TEXT requires 2 arguments")
	}
	var v Value
	var err error
	if args[0].isRange {
		if args[0].r.Start != args[0].r.End {
			return Value{}, fmt.Errorf("TEXT range must be a single cell")
		}
		t := args[0].targetSheet(s)
		v, err = t.evaluateCell(args[0].r.Start, seen)
		if err != nil {
			return Value{}, err
		}
	} else {
		v, err = s.parseFormula(args[0].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		if v.Type == valRange || v.Type == valArray {
			v, err = derefSingle(s, v, seen)
			if err != nil {
				return Value{}, err
			}
		}
	}
	var fv Value
	if args[1].isRange {
		if args[1].r.Start != args[1].r.End {
			return Value{}, fmt.Errorf("TEXT format must be a single cell")
		}
		t := args[1].targetSheet(s)
		fv, err = t.evaluateCell(args[1].r.Start, seen)
		if err != nil {
			return Value{}, err
		}
	} else {
		fv, err = s.parseFormula(args[1].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		if fv.Type == valRange {
			fv, err = derefSingle(s, fv, seen)
			if err != nil {
				return Value{}, err
			}
		}
	}
	format := valueText(fv)
	// applyNumberFormat already handles valString passthrough; ensure TEXT
	// always returns a string.
	formatted := applyNumberFormat(v, format)
	return Value{Type: valString, Str: formatted}, nil
}

// evalFuncFilter implements FILTER(array, include, [if_empty])
func (s *Sheet) evalFuncFilter(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 2 || len(args) > 3 {
		return Value{}, fmt.Errorf("FILTER requires 2 or 3 arguments")
	}
	// helper to materialize arg as 2D array
	toArray := func(arg funcArg) ([][]Value, error) {
		if arg.isRange {
			tgt := arg.targetSheet(s)
			r := arg.r
			var out [][]Value
			for rr := r.Start.Row; rr <= r.End.Row; rr++ {
				var row []Value
				for cc := r.Start.Col; cc <= r.End.Col; cc++ {
					v, err := tgt.evaluateCell(Coord{Row: rr, Col: cc}, seen)
					if err != nil {
						// treat error cells as empty 0
						row = append(row, Value{Type: valNumber, Num: 0})
						continue
					}
					row = append(row, v)
				}
				out = append(out, row)
			}
			return out, nil
		}
		if strings.TrimSpace(arg.text) == "" {
			return nil, fmt.Errorf("FILTER array argument empty")
		}
		v, err := s.parseFormula(arg.text, current, seen)
		if err != nil {
			return nil, err
		}
		if v.Type == valRange || v.Type == valArray {
			tgt := s
			if v.SheetName != "" && s.resolveSheet != nil {
				if t := s.resolveSheet(v.SheetName); t != nil {
					tgt = t
				}
			}
			r := v.R
			var out [][]Value
			for rr := r.Start.Row; rr <= r.End.Row; rr++ {
				var row []Value
				for cc := r.Start.Col; cc <= r.End.Col; cc++ {
					cv, _ := tgt.evaluateCell(Coord{Row: rr, Col: cc}, seen)
					row = append(row, cv)
				}
				out = append(out, row)
			}
			return out, nil
		}
		if v.Type == valArray {
			return v.Arr, nil
		}
		// scalar -> 1x1
		return [][]Value{{v}}, nil
	}
	arr, err := toArray(args[0])
	if err != nil {
		return Value{}, err
	}
	if len(arr) == 0 || len(arr[0]) == 0 {
		return Value{}, fmt.Errorf("#VALUE! FILTER array empty")
	}
	inc, err := toArray(args[1])
	if err != nil {
		return Value{}, err
	}
	arrRows, arrCols := len(arr), len(arr[0])
	incRows, incCols := len(inc), len(inc[0])

	// Decide filtering dimension
	var filtered [][]Value
	if incRows == arrRows && incCols == 1 {
		// filter rows
		for i := 0; i < arrRows; i++ {
			if isTruthy(inc[i][0]) {
				// copy row
				rowCopy := make([]Value, arrCols)
				copy(rowCopy, arr[i])
				filtered = append(filtered, rowCopy)
			}
		}
	} else if incRows == 1 && incCols == arrCols {
		// filter columns
		keepCols := []int{}
		for j := 0; j < arrCols; j++ {
			if isTruthy(inc[0][j]) {
				keepCols = append(keepCols, j)
			}
		}
		for i := 0; i < arrRows; i++ {
			var row []Value
			for _, j := range keepCols {
				row = append(row, arr[i][j])
			}
			if len(row) > 0 {
				filtered = append(filtered, row)
			}
		}
		// if no columns kept, filtered will be empty
		if len(keepCols) == 0 {
			filtered = nil
		}
	} else if incRows == arrRows && incCols == arrCols {
		// element-wise? For simplicity treat as row filter where any TRUE in row keeps row,
		// but spec expects include to be 1D. Return error for ambiguous.
		return Value{}, fmt.Errorf("#VALUE! FILTER include dimensions mismatch")
	} else {
		return Value{}, fmt.Errorf("#VALUE! FILTER include dimensions mismatch")
	}
	if len(filtered) == 0 {
		if len(args) == 3 {
			// evaluate if_empty
			if args[2].isRange {
				tgt := args[2].targetSheet(s)
				// if single cell, return its value; if multi, return as array
				if args[2].r.Start == args[2].r.End {
					v, _ := tgt.evaluateCell(args[2].r.Start, seen)
					if v.Type == valString {
						return Value{Type: valString, Str: v.Str}, nil
					}
					return v, nil
				}
				// multi-cell if_empty -> return as array
				arr2, _ := toArray(args[2])
				return Value{Type: valArray, Arr: arr2}, nil
			}
			v, err := s.parseFormula(args[2].text, current, seen)
			if err != nil {
				// treat raw text as string literal if parse fails
				return Value{Type: valString, Str: strings.Trim(args[2].text, "\"")}, nil
			}
			if v.Type == valRange || v.Type == valArray {
				// materialize
				a2, _ := toArray(args[2])
				if len(a2) == 1 && len(a2[0]) == 1 {
					return a2[0][0], nil
				}
				return Value{Type: valArray, Arr: a2}, nil
			}
			return v, nil
		}
		return Value{}, fmt.Errorf("#CALC!")
	}
	return Value{Type: valArray, Arr: filtered}, nil
}

func (s *Sheet) evalFuncTextJoin(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) < 3 {
		return Value{}, fmt.Errorf("TEXTJOIN requires at least 3 arguments")
	}
	// delimiter
	var delim string
	if args[0].isRange {
		tgt := args[0].targetSheet(s)
		v, _ := tgt.evaluateCell(args[0].r.Start, seen)
		delim = valueText(v)
	} else {
		v, err := s.parseFormula(args[0].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		if v.Type == valRange || v.Type == valArray {
			if v.Type == valArray {
				if len(v.Arr) > 0 && len(v.Arr[0]) > 0 {
					delim = valueText(v.Arr[0][0])
				}
			} else {
				tgt := s
				if v.SheetName != "" && s.resolveSheet != nil {
					if t := s.resolveSheet(v.SheetName); t != nil {
						tgt = t
					}
				}
				cv, _ := tgt.evaluateCell(v.R.Start, seen)
				delim = valueText(cv)
			}
		} else {
			delim = valueText(v)
		}
	}
	// ignore_empty
	var ignoreEmpty bool
	if args[1].isRange {
		tgt := args[1].targetSheet(s)
		v, _ := tgt.evaluateCell(args[1].r.Start, seen)
		ignoreEmpty = isTruthy(v)
	} else {
		v, err := s.parseFormula(args[1].text, current, seen)
		if err != nil {
			return Value{}, err
		}
		if v.Type == valRange || v.Type == valArray {
			v, _ = derefSingle(s, v, seen)
		}
		ignoreEmpty = isTruthy(v)
	}
	var parts []string
	for _, arg := range args[2:] {
		if arg.isRange {
			tgt := arg.targetSheet(s)
			r := arg.r
			for rr := r.Start.Row; rr <= r.End.Row; rr++ {
				for cc := r.Start.Col; cc <= r.End.Col; cc++ {
					raw := tgt.Raw(Coord{Row: rr, Col: cc})
					var txt string
					if strings.HasPrefix(strings.TrimSpace(raw), "=") {
						cv, _ := tgt.evaluateCell(Coord{Row: rr, Col: cc}, seen)
						txt = valueText(cv)
					} else {
						txt = SanitizeForDisplay(raw)
						// raw empty -> txt == "" ; keep as empty
					}
					if ignoreEmpty && txt == "" {
						continue
					}
					parts = append(parts, txt)
				}
			}
		} else if strings.TrimSpace(arg.text) != "" {
			v, err := s.parseFormula(arg.text, current, seen)
			if err != nil {
				return Value{}, err
			}
			if v.Type == valRange {
				tgt := s
				if v.SheetName != "" && s.resolveSheet != nil {
					if t := s.resolveSheet(v.SheetName); t != nil {
						tgt = t
					}
				}
				r := v.R
				for rr := r.Start.Row; rr <= r.End.Row; rr++ {
					for cc := r.Start.Col; cc <= r.End.Col; cc++ {
						raw := tgt.Raw(Coord{Row: rr, Col: cc})
						var txt string
						if strings.HasPrefix(strings.TrimSpace(raw), "=") {
							cv, _ := tgt.evaluateCell(Coord{Row: rr, Col: cc}, seen)
							txt = valueText(cv)
						} else {
							txt = SanitizeForDisplay(raw)
						}
						if ignoreEmpty && txt == "" {
							continue
						}
						parts = append(parts, txt)
					}
				}
			} else if v.Type == valArray {
				for _, row := range v.Arr {
					for _, cell := range row {
						txt := valueText(cell)
						if ignoreEmpty && txt == "" {
							continue
						}
						parts = append(parts, txt)
					}
				}
			} else {
				txt := valueText(v)
				if !(ignoreEmpty && txt == "") {
					parts = append(parts, txt)
				}
			}
		}
	}
	joined := strings.Join(parts, delim)
	return Value{Type: valString, Str: joined}, nil
}

func (s *Sheet) evalFuncSumProduct(args []funcArg, current Coord, seen map[evalKey]bool) (Value, error) {
	if len(args) == 0 {
		return Value{}, fmt.Errorf("SUMPRODUCT requires at least 1 argument")
	}
	toArray := func(arg funcArg) ([][]Value, error) {
		if arg.isRange {
			tgt := arg.targetSheet(s)
			r := arg.r
			var out [][]Value
			for rr := r.Start.Row; rr <= r.End.Row; rr++ {
				var row []Value
				for cc := r.Start.Col; cc <= r.End.Col; cc++ {
					v, _ := tgt.evaluateCell(Coord{Row: rr, Col: cc}, seen)
					row = append(row, v)
				}
				out = append(out, row)
			}
			return out, nil
		}
		if strings.TrimSpace(arg.text) == "" {
			return nil, fmt.Errorf("SUMPRODUCT argument empty")
		}
		v, err := s.parseFormula(arg.text, current, seen)
		if err != nil {
			return nil, err
		}
		if v.Type == valRange {
			tgt := s
			if v.SheetName != "" && s.resolveSheet != nil {
				if t := s.resolveSheet(v.SheetName); t != nil {
					tgt = t
				}
			}
			r := v.R
			var out [][]Value
			for rr := r.Start.Row; rr <= r.End.Row; rr++ {
				var row []Value
				for cc := r.Start.Col; cc <= r.End.Col; cc++ {
					cv, _ := tgt.evaluateCell(Coord{Row: rr, Col: cc}, seen)
					row = append(row, cv)
				}
				out = append(out, row)
			}
			return out, nil
		}
		if v.Type == valArray {
			return v.Arr, nil
		}
		return [][]Value{{v}}, nil
	}
	var arrays [][][]Value
	maxRows, maxCols := 0, 0
	for _, arg := range args {
		arr, err := toArray(arg)
		if err != nil {
			return Value{}, err
		}
		if len(arr) == 0 || len(arr[0]) == 0 {
			return Value{}, fmt.Errorf("#VALUE! SUMPRODUCT empty array")
		}
		arrays = append(arrays, arr)
		if len(arr) > maxRows {
			maxRows = len(arr)
		}
		if len(arr[0]) > maxCols {
			maxCols = len(arr[0])
		}
	}
	// validate dimensions: each array must be 1x1 or maxRows x maxCols
	for _, arr := range arrays {
		if !(len(arr) == maxRows && len(arr[0]) == maxCols) && !(len(arr) == 1 && len(arr[0]) == 1) {
			return Value{}, fmt.Errorf("#VALUE! SUMPRODUCT arrays must be same size")
		}
	}
	total := 0.0
	for r := 0; r < maxRows; r++ {
		for c := 0; c < maxCols; c++ {
			prod := 1.0
			for _, arr := range arrays {
				var v Value
				if len(arr) == 1 && len(arr[0]) == 1 {
					v = arr[0][0]
				} else {
					v = arr[r][c]
				}
				n, ok := valueAsNumber(v)
				if !ok {
					// text and errors treated as 0 in SUMPRODUCT per Excel
					n = 0
				}
				prod *= n
			}
			total += prod
		}
	}
	return Value{Type: valNumber, Num: total}, nil
}

func (s *Sheet) evalFuncNA(args []funcArg) (Value, error) {
	if len(args) != 0 {
		return Value{}, fmt.Errorf("NA requires 0 arguments")
	}
	return Value{}, fmt.Errorf("#N/A")
}
