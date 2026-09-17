package sheet

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Excel's worksheet grid is limited to XFD1048576.
const (
	MaxRows    = 1_048_576
	MaxColumns = 16_384
)

type Coord struct {
	Row int
	Col int
}

func (c Coord) Valid() bool {
	return c.Row >= 0 && c.Col >= 0
}

func (c Coord) String() string {
	if !c.Valid() {
		return ""
	}
	return ColumnName(c.Col) + strconv.Itoa(c.Row+1)
}

func ColumnName(col int) string {
	if col < 0 {
		return ""
	}
	var out []byte
	for col >= 0 {
		out = append([]byte{byte('A' + col%26)}, out...)
		col = col/26 - 1
	}
	return string(out)
}

func ParseCoord(addr string) (Coord, error) {
	addr = strings.TrimSpace(strings.ToUpper(addr))
	if addr == "" {
		return Coord{}, fmt.Errorf("empty address")
	}

	i := 0
	if addr[i] == '$' {
		i++
	}
	colStart := i
	for i < len(addr) && unicode.IsLetter(rune(addr[i])) {
		i++
	}
	if i == colStart || i == len(addr) {
		return Coord{}, fmt.Errorf("invalid address %q", addr)
	}

	col, err := ParseColumn(addr[colStart:i])
	if err != nil {
		return Coord{}, fmt.Errorf("invalid column in %q", addr)
	}
	if addr[i] == '$' {
		i++
	}

	row, err := strconv.Atoi(addr[i:])
	if err != nil || row < 1 || row > MaxRows {
		return Coord{}, fmt.Errorf("invalid row in %q", addr)
	}
	return Coord{Row: row - 1, Col: col}, nil
}

func ParseColumn(s string) (int, error) {
	col := 0
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return -1, fmt.Errorf("invalid column %q", s)
		}
		digit := int(r - 'A' + 1)
		if col > (MaxColumns-digit)/26 {
			return -1, fmt.Errorf("column %q is outside the Excel grid", s)
		}
		col = col*26 + digit
	}
	if col == 0 {
		return -1, fmt.Errorf("invalid column %q", s)
	}
	return col - 1, nil
}

type Range struct {
	Start Coord
	End   Coord
}

func (r Range) Valid() bool {
	return r.Start.Valid() && r.End.Valid() && r.Start.Row <= r.End.Row && r.Start.Col <= r.End.Col
}

func (r Range) Contains(c Coord) bool {
	return r.Valid() && c.Row >= r.Start.Row && c.Row <= r.End.Row && c.Col >= r.Start.Col && c.Col <= r.End.Col
}

func (r Range) RowSpan() int {
	if !r.Valid() {
		return 0
	}
	return r.End.Row - r.Start.Row + 1
}

func (r Range) ColSpan() int {
	if !r.Valid() {
		return 0
	}
	return r.End.Col - r.Start.Col + 1
}

// parseColumnToken reports whether the token is a bare column like "A" or
// "XFD" (letters only).
func parseColumnToken(s string) (int, bool) {
	if s == "" {
		return -1, false
	}
	for _, r := range s {
		if r < 'A' || r > 'Z' {
			return -1, false
		}
	}
	col, err := ParseColumn(s)
	if err != nil {
		return -1, false
	}
	return col, true
}

func ParseRange(ref string) (Range, error) {
	parts := strings.Split(strings.TrimSpace(ref), ":")
	if len(parts) == 1 {
		c, err := ParseCoord(parts[0])
		if err != nil {
			return Range{}, err
		}
		return Range{Start: c, End: c}, nil
	}
	if len(parts) != 2 {
		return Range{}, fmt.Errorf("invalid range %q", ref)
	}

	// Whole-column ranges like A:B span the entire grid height.
	colA, aCols := parseColumnToken(strings.ToUpper(strings.TrimSpace(parts[0])))
	colB, bCols := parseColumnToken(strings.ToUpper(strings.TrimSpace(parts[1])))
	if aCols && bCols {
		start, end := colA, colB
		if start > end {
			start, end = end, start
		}
		return Range{
			Start: Coord{Row: 0, Col: start},
			End:   Coord{Row: MaxRows - 1, Col: end},
		}, nil
	}

	start, err := ParseCoord(parts[0])
	if err != nil {
		return Range{}, err
	}
	end, err := ParseCoord(parts[1])
	if err != nil {
		return Range{}, err
	}
	if start.Row > end.Row {
		start.Row, end.Row = end.Row, start.Row
	}
	if start.Col > end.Col {
		start.Col, end.Col = end.Col, start.Col
	}
	return Range{Start: start, End: end}, nil
}
