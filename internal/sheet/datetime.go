package sheet

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"tuisheet/internal/locale"
)

// Excel 1900 date system (ISO/IEC 29500-1 §18.17.4.1): serial 1 is
// 1900-01-01 and serial 60 is the fictitious 1900-02-29 inherited from
// Lotus 1-2-3. Dates from serial 61 (1900-03-01) onward are mapped via the
// 1899-12-30 epoch; earlier ones via 1899-12-31 so January/February 1900
// stay correct.
const excelEpochPre60 = "1899-12-31"
const excelEpochPost60 = "1899-12-30"

var epochPre, epochPost time.Time

func init() {
	var err error
	epochPre, err = time.ParseInLocation("2006-01-02", excelEpochPre60, time.UTC)
	if err != nil {
		panic(err)
	}
	epochPost, err = time.ParseInLocation("2006-01-02", excelEpochPost60, time.UTC)
	if err != nil {
		panic(err)
	}
}

// serialToTime converts an Excel serial number to a UTC time.
func serialToTime(serial float64) (time.Time, error) {
	if serial < 0 || serial >= 2958466 { // 9999-12-31 is the last valid day
		return time.Time{}, fmt.Errorf("serial %.4f outside Excel date range", serial)
	}
	days := math.Floor(serial)
	frac := serial - days
	base := epochPost
	if days < 60 {
		base = epochPre
	}
	t := base.AddDate(0, 0, int(days))
	t = t.Add(time.Duration(frac * 24 * float64(time.Hour)))
	return t.Round(time.Second), nil
}

// timeToSerial converts a time to an Excel serial number using its
// wall-clock fields as-is — no timezone conversion — so a local
// midnight stays that calendar date instead of drifting a day.
func timeToSerial(t time.Time) float64 {
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	mar1900 := time.Date(1900, 3, 1, 0, 0, 0, 0, time.UTC)
	var days int
	if day.Before(mar1900) {
		days = int(day.Sub(epochPre).Hours() / 24)
	} else {
		days = int(day.Sub(epochPost).Hours() / 24)
	}
	frac := (float64(t.Hour()*3600+t.Minute()*60+t.Second()) + float64(t.Nanosecond())/1e9) / 86400.0
	return float64(days) + frac
}

// primaryFormatSection returns the section of a format code to use for
// positive numbers, dropping the negative/zero/text sections after ";" and
// unescaping Excel's backslash and quote literals.
func normalizeFormat(format string) string {
	if idx := strings.Index(format, ";"); idx >= 0 {
		format = format[:idx]
	}
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		switch format[i] {
		case '\\':
			if i+1 < len(format) {
				b.WriteByte(format[i+1])
				i++
			}
		case '"':
			for i++; i < len(format) && format[i] != '"'; i++ {
				b.WriteByte(format[i])
			}
		default:
			b.WriteByte(format[i])
		}
	}
	return b.String()
}

// formatDateSerial renders a serial using an Excel-style picture format.
// Recognised tokens: yyyy yy mmmm mmm mm m dd d hh h ss s am/pm, plus any
// literal separators. Unknown patterns fall back to the general number.
func formatDateSerial(serial float64, format string) string {
	t, err := serialToTime(serial)
	if err != nil {
		return strconv.FormatFloat(serial, 'f', -1, 64)
	}
	format = normalizeFormat(format)
	// Excel shows h/hh in 24-hour form unless the format carries an
	// AM/PM designator, which switches to 12-hour.
	use12 := strings.Contains(strings.ToLower(format), "am/pm")
	hour := func() int {
		if use12 {
			return hour12(t)
		}
		return t.Hour()
	}
	var b strings.Builder
	runes := []rune(format)
	for i := 0; i < len(runes); i++ {
		rest := string(runes[i:])
		switch {
		case strings.HasPrefix(rest, "yyyy"):
			b.WriteString(fmt.Sprintf("%04d", t.Year()))
			i += 3
		case strings.HasPrefix(rest, "yy"):
			b.WriteString(fmt.Sprintf("%02d", t.Year()%100))
			i++
		case strings.HasPrefix(rest, "mmmm"):
			b.WriteString(t.Month().String())
			i += 3
		case strings.HasPrefix(rest, "mmm"):
			b.WriteString(t.Format("Jan"))
			i += 2
		case runes[i] == 'm':
			// After h/hh this means minutes; otherwise it is the month.
			n := 2
			if i+1 >= len(runes) || runes[i+1] != 'm' {
				n = 1
			}
			if hasMonthToken(runes, i) {
				b.WriteString(pad(int(t.Month()), n))
			} else {
				b.WriteString(pad(t.Minute(), n))
			}
			i += n - 1
		case strings.HasPrefix(rest, "dd"):
			b.WriteString(pad(t.Day(), 2))
			i++
		case strings.HasPrefix(rest, "d"):
			b.WriteString(strconv.Itoa(t.Day()))
		case strings.HasPrefix(rest, "hh"):
			b.WriteString(pad(hour(), 2))
			i++
		case strings.HasPrefix(rest, "h"):
			b.WriteString(strconv.Itoa(hour()))
		case strings.HasPrefix(rest, "ss"):
			b.WriteString(pad(t.Second(), 2))
			i++
		case strings.HasPrefix(rest, "s"):
			b.WriteString(strconv.Itoa(t.Second()))
		case strings.HasPrefix(strings.ToLower(rest), "am/pm"):
			if t.Hour() < 12 {
				b.WriteString("AM")
			} else {
				b.WriteString("PM")
			}
			i += 4
		default:
			b.WriteRune(runes[i])
		}
	}
	return b.String()
}

// hasMonthToken reports whether the m/mm at runes[i] is a month token;
// directly following h/hh it means minutes instead.
func hasMonthToken(runes []rune, i int) bool {
	if runes[i] != 'm' {
		return false
	}
	for j := i - 1; j >= 0; j-- {
		switch runes[j] {
		case 'h':
			return false // minutes
		case 'y', 'd', 'm', 's':
			return true
		default:
			continue // skip separators
		}
	}
	return true
}

func hour12(t time.Time) int {
	h := t.Hour() % 12
	if h == 0 {
		h = 12
	}
	return h
}

func pad(n int, width int) string {
	s := strconv.Itoa(n)
	for len(s) < width {
		s = "0" + s
	}
	return s
}

// looksLikeDateFormat reports whether a number-format code should render
// its value as a date/time rather than a plain number.
func looksLikeDateFormat(format string) bool {
	lower := strings.ToLower(format)
	return strings.ContainsAny(lower, "ymdhs")
}

// applyNumberFormat renders a numeric value according to a format code.
// Date/time codes use formatDateSerial; numeric codes support fixed
// decimals ("0.00") and thousands separators ("#,##0(.00)"). It is locale-
// aware for Number/Currency/Percentage: grouping/decimal separators and
// currency symbol placement follow the user's locale (queried via env).
func applyNumberFormat(v Value, format string) string {
	if v.Type != valNumber {
		if v.Type == valString {
			return v.Str
		}
		return ""
	}
	format = normalizeFormat(format)
	if format == "" {
		return strconv.FormatFloat(v.Num, 'f', -1, 64)
	}
	if looksLikeDateFormat(format) {
		return formatDateSerial(v.Num, format)
	}
	// Percentage: locale-aware decimal
	switch format {
	case "", "General", "general":
		return strconv.FormatFloat(v.Num, 'f', -1, 64)
	case "0%", "0.00%", "#0%", "#0.00%":
		decimals := 0
		if strings.Contains(format, "0.00") {
			decimals = 2
		}
		dec, _ := localeSeparators()
		s := strconv.FormatFloat(v.Num*100, 'f', decimals, 64)
		if dec != "." {
			s = strings.ReplaceAll(s, ".", dec)
		}
		return s + "%"
	}
	// Detect currency: format contains $, €, £, ¥, etc.
	isCurrency := false
	currencySym := ""
	for _, sym := range []string{"R$", "$", "€", "£", "¥", "₩"} {
		if strings.Contains(format, sym) {
			isCurrency = true
			currencySym = sym
			break
		}
	}
	// Numeric pattern: count decimals and grouping. Handle both en "#,##0.00"
	// and de "#.##0,00" stored patterns by detecting decimal char as last '.' or ','.
	group := strings.Contains(format, "#,##0") || strings.Contains(format, ",##0") || strings.Contains(format, "#.##0") || strings.Contains(format, ".##0") || strings.Contains(format, "# ##0") || strings.Contains(format, " ##0")
	decSepInFormat := "."
	if strings.Contains(format, ",00") && strings.Contains(format, ".") {
		// ambiguous, but last separator before 0s is decimal
		lastDot := strings.LastIndex(format, ".")
		lastComma := strings.LastIndex(format, ",")
		if lastComma > lastDot {
			decSepInFormat = ","
		}
	} else if strings.Contains(format, ",00") || strings.Contains(format, ",0") {
		if !strings.Contains(format, ".") {
			decSepInFormat = ","
		}
	}
	decPart := ""
	if decSepInFormat == "." {
		if idx := strings.Index(format, "."); idx >= 0 {
			decPart = format[idx:]
		}
	} else {
		if idx := strings.LastIndex(format, ","); idx >= 0 {
			decPart = format[idx:]
		}
	}
	decimals := 0
	for _, r := range decPart {
		if r == '0' || r == '#' {
			decimals++
		}
	}
	s := strconv.FormatFloat(v.Num, 'f', decimals, 64)
	dec, grp := localeSeparators()
	if group {
		intPart, decTail := s, ""
		if idx := strings.Index(s, "."); idx >= 0 {
			intPart, decTail = s[:idx], s[idx:]
			// decTail includes '.'; replace with locale decimal
			if dec != "." {
				decTail = strings.ReplaceAll(decTail, ".", dec)
			}
		}
		neg := strings.HasPrefix(intPart, "-")
		intPart = strings.TrimPrefix(intPart, "-")
		var out []byte
		for i, d := range []byte(intPart) {
			if i > 0 && (len(intPart)-i)%3 == 0 {
				out = append(out, grp[0])
				if len(grp) > 1 {
					out = append(out, grp[1:]...)
				}
			}
			out = append(out, d)
		}
		s = string(out)
		if neg {
			s = "-" + s
		}
		s += decTail
	} else {
		// No grouping, just replace decimal if needed
		if dec != "." {
			s = strings.ReplaceAll(s, ".", dec)
		}
	}
	if isCurrency {
		// Use locale's currency symbol/placement, not necessarily format's symbol
		_, sym := localeCurrency()
		if sym == "" {
			sym = currencySym
		}
		// Heuristic: European decimal ',' -> suffix with space, else prefix
		if dec == "," {
			s = s + " " + sym
		} else {
			s = sym + s
		}
	}
	return s
}

func localeTag() string { return locale.Tag() }

func localeSeparators() (string, string) { return locale.Separators() }

func localeCurrency() (string, string) { return locale.Currency() }

// ---------- User input: show dates as dates, store them as serials ----------

// EditableText returns what a cell editor should display: formulas stay
// raw; plain numeric cells with a date format render as formatted dates
// so end users can read and edit them like mainstream spreadsheets do.
func (s *Sheet) EditableText(c Coord) string {
	raw := s.Raw(c)
	if strings.HasPrefix(raw, "=") {
		return raw
	}
	st := s.Style(c)
	if st.NumFmt != "" {
		v := parseValue(raw)
		if v.Type == valNumber {
			return applyNumberFormat(v, st.NumFmt)
		}
	}
	return raw
}

// SetUserInput interprets user-typed text in the context of the target
// cell before storing it:
//
//   - formulas and blanks are stored verbatim;
//   - a cell that already carries a date format gets its input parsed
//     according to that format's picture code (day/month order follows
//     the pattern);
//   - an unformatted cell receiving an obvious date (ISO yyyy-mm-dd or a
//     day-first dd/mm/yyyy) is stored as a serial and given a matching
//     date format automatically, mirroring Excel's behaviour;
//   - anything unparseable is stored as plain text.
func (s *Sheet) SetUserInput(c Coord, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		s.Set(c, text)
		return
	}
	if strings.HasPrefix(text, "=") {
		// Typing TODAY()/NOW() on a fresh cell gets a date format, like
		// mainstream spreadsheets do.
		if containsDateFunction(text) && s.Style(c).NumFmt == "" {
			st := s.Style(c)
			st.NumFmt = "yyyy-mm-dd"
			s.SetStyle(c, st)
		}
		s.Set(c, text)
		return
	}

	st := s.Style(c)
	if st.NumFmt != "" {
		if serial, ok := parseDateWithPattern(text, st.NumFmt); ok {
			s.Set(c, strconv.FormatFloat(serial, 'f', -1, 64))
			return
		}
		s.Set(c, text)
		return
	}

	if serial, fmtCode, ok := detectDateInput(text); ok {
		st.NumFmt = fmtCode
		s.SetStyle(c, st)
		s.Set(c, strconv.FormatFloat(serial, 'f', -1, 64))
		return
	}
	s.Set(c, text)
}

// detectDateInput recognises unambiguous date text without a cell format:
// ISO yyyy-mm-dd always wins; otherwise day-first dd/mm/yyyy is assumed.
// An optional trailing time (" 14:30", " 02:15:30 pm") is accepted.
func detectDateInput(text string) (float64, string, bool) {
	base, rest := splitDateTimeParts(text)
	tm, ok := parseOptionalTime(rest)
	if !ok {
		return 0, "", false
	}
	a, b, c, ok := scanThreeInts(base)
	if !ok || !isSepDate(base) {
		return 0, "", false
	}
	var y, m, d int
	if looksISO(base) {
		y, m, d = a, b, c
	} else {
		d, m, y = a, b, c // day-first for ambiguous separators
	}
	serial, err := buildSerial(expandYear(y), m, d, tm)
	if err != nil {
		return 0, "", false
	}
	return serial, "yyyy-mm-dd", true
}

func looksISO(s string) bool {
	parts := strings.Split(s, "-")
	return len(parts) == 3 && len(parts[0]) == 4
}

// isSepDate requires exactly two separators so plain numbers never
// become dates.
func isSepDate(s string) bool {
	seps := strings.Count(s, "-") + strings.Count(s, "/") + strings.Count(s, ".")
	return seps == 2
}

// scanThreeInts parses "a<sep>b<sep>c" with any single separator kind.
func scanThreeInts(s string) (int, int, int, bool) {
	sep := byte(0)
	fields := make([]string, 0, 3)
	start := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '/', '-', '.':
			if sep == 0 {
				sep = s[i]
			}
			if s[i] != sep {
				return 0, 0, 0, false
			}
			fields = append(fields, s[start:i])
			start = i + 1
		}
	}
	fields = append(fields, s[start:])
	if len(fields) != 3 {
		return 0, 0, 0, false
	}
	var nums []int
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil {
			return 0, 0, 0, false
		}
		nums = append(nums, n)
	}
	return nums[0], nums[1], nums[2], true
}

// expandYear maps two-digit years onto 2000..2029 / 1930..1999 following
// Excel's pivot.
func expandYear(y int) int {
	if y < 100 {
		if y < 30 {
			return 2000 + y
		}
		return 1900 + y
	}
	return y
}

// trailingTimeRe matches an optional time suffix like " 14:30",
// " 02:15:30 pm" or "T14:30" at the end of user input.
var trailingTimeRe = regexp.MustCompile(`(?i)(?:[ T])(\d{1,2}:\d{2}(?::\d{2})?(?:\s*(?:am|pm))?)$`)

// splitDateTimeParts separates a date from an optional trailing time,
// e.g. "5 Mar 24 14:30" -> "5 Mar 24", "14:30".
func splitDateTimeParts(text string) (string, string) {
	if m := trailingTimeRe.FindStringSubmatchIndex(text); m != nil {
		return strings.TrimSpace(text[:m[0]]), strings.ReplaceAll(text[m[2]:m[3]], " ", "")
	}
	return text, ""
}

type dayTime struct {
	h, min, sec int
	pm          bool
}

// parseOptionalTime parses "hh:mm[:ss] [am|pm]" returning ok=false when the
// text exists but does not parse; empty text yields midnight.
func parseOptionalTime(text string) (dayTime, bool) {
	if text == "" {
		return dayTime{}, true
	}
	pm := false
	lower := strings.ToLower(strings.TrimSpace(text))
	switch {
	case strings.HasSuffix(lower, "am"):
		lower = strings.TrimSpace(strings.TrimSuffix(lower, "am"))
	case strings.HasSuffix(lower, "pm"):
		pm = true
		lower = strings.TrimSpace(strings.TrimSuffix(lower, "pm"))
	}
	parts := strings.Split(lower, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return dayTime{}, false
	}
	var nums []int
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return dayTime{}, false
		}
		nums = append(nums, n)
	}
	dt := dayTime{h: nums[0], min: nums[1]}
	if len(nums) == 3 {
		dt.sec = nums[2]
	}
	if dt.h < 0 || dt.h > 23 || dt.min > 59 || dt.sec > 59 {
		return dayTime{}, false
	}
	if pm {
		dt.h %= 12
		dt.h += 12
	}
	return dt, true
}

func buildSerial(y, m, d int, tm dayTime) (float64, error) {
	if m < 1 || m > 12 || d < 1 || d > 31 {
		return 0, fmt.Errorf("invalid date %04d-%02d-%02d", y, m, d)
	}
	t := time.Date(y, time.Month(m), d, tm.h, tm.min, tm.sec, 0, time.UTC)
	if t.Day() != d || int(t.Month()) != m { // overflow rejected, unlike DATE()
		return 0, fmt.Errorf("date does not exist")
	}
	return timeToSerial(t), nil
}

// parseDateWithPattern parses text according to an Excel picture format,
// honouring its day/month/year order while staying flexible about the
// actual separators ("5 Mar 24" fits d-mmm-yy).
func parseDateWithPattern(text string, format string) (float64, bool) {
	pattern := strings.ReplaceAll(normalizeFormat(format), " ", "")
	if !looksLikeDateFormat(pattern) {
		return 0, false
	}
	order, nameMonths := patternFieldOrder(pattern)
	if len(order) == 0 || !orderHas(order, 'y', 'm', 'd') {
		return 0, false // need a complete calendar date to be unambiguous
	}

	base, rest := splitDateTimeParts(text)
	tm, ok := parseOptionalTime(rest)
	if !ok {
		return 0, false
	}
	base = strings.ReplaceAll(strings.TrimSpace(base), " ", "")

	var y, mo, da int
	pos := 0
	readDigits := func() (int, bool) {
		n := 0
		digits := 0
		for pos < len(base) && base[pos] >= '0' && base[pos] <= '9' && digits < 4 {
			n = n*10 + int(base[pos]-'0')
			pos++
			digits++
		}
		return n, digits > 0
	}
	for _, field := range order {
		for pos < len(base) && !isDigitByte(base[pos]) &&
			!(field == 'm' && nameMonths && isAlphaByte(base[pos])) {
			pos++ // skip separators
		}
		switch field {
		case 'm':
			if nameMonths && pos < len(base) && isAlphaByte(base[pos]) {
				name, ok := matchMonthName(base[pos:])
				if !ok {
					return 0, false
				}
				mo = name
				for pos < len(base) && isAlphaByte(base[pos]) {
					pos++
				}
				break
			}
			n, ok := readDigits()
			if !ok {
				return 0, false
			}
			mo = n
		case 'd':
			n, ok := readDigits()
			if !ok {
				return 0, false
			}
			da = n
		case 'y':
			n, ok := readDigits()
			if !ok {
				return 0, false
			}
			y = n
		}
	}
	if pos < len(base) {
		// Trailing junk (beyond separators) means this was not a date.
		for ; pos < len(base); pos++ {
			if isDigitByte(base[pos]) {
				return 0, false
			}
		}
	}
	if mo == 0 {
		mo = 1
	}
	if da == 0 {
		da = 1
	}
	serial, err := buildSerial(expandYear(y), mo, da, tm)
	if err != nil {
		return 0, false
	}
	return serial, true
}

// patternFieldOrder extracts the calendar-date component order from a
// normalized picture code ('y', 'm', 'd' in sequence). nameMonths reports
// whether the code spells months via mmm/mmmm. Time tokens terminate the
// scan; the caller handles times separately.
func patternFieldOrder(pattern string) ([]byte, bool) {
	runes := []rune(pattern)
	var order []byte
	nameMonths := false
	for i := 0; i < len(runes); i++ {
		rest := string(runes[i:])
		switch {
		case strings.HasPrefix(rest, "yyyy"):
			order = append(order, 'y')
			i += 3
		case strings.HasPrefix(rest, "yy"):
			order = append(order, 'y')
			i++
		case strings.HasPrefix(rest, "mmmm"), strings.HasPrefix(rest, "mmm"):
			order = append(order, 'm')
			nameMonths = true
			for j := 0; j < 2 && i+1 < len(runes) && runes[i+1] == 'm'; j++ {
				i++
			}
		case runes[i] == 'm':
			order = append(order, 'm')
			if i+1 < len(runes) && runes[i+1] == 'm' {
				i++
			}
		case strings.HasPrefix(rest, "dd"), runes[i] == 'd':
			order = append(order, 'd')
			if strings.HasPrefix(rest, "dd") {
				i++
			}
		case runes[i] == 'h' || runes[i] == 'H' || runes[i] == 's' || runes[i] == 'S':
			return order, nameMonths // time section: caller parses it
		}
	}
	return order, nameMonths
}

func orderHas(order []byte, want ...byte) bool {
	set := map[byte]bool{}
	for _, f := range order {
		set[f] = true
	}
	for _, w := range want {
		if !set[w] {
			return false
		}
	}
	return true
}

func isDigitByte(b byte) bool { return b >= '0' && b <= '9' }

func isAlphaByte(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

// matchMonthName matches an English month name or abbreviation at the
// start of text, case-insensitively.
func matchMonthName(text string) (int, bool) {
	lower := strings.ToLower(text)
	names := []string{
		"january", "february", "march", "april", "may", "june",
		"july", "august", "september", "october", "november", "december",
	}
	for idx, name := range names {
		if strings.HasPrefix(lower, name) {
			return idx + 1, true
		}
		if strings.HasPrefix(lower, name[:3]) {
			return idx + 1, true
		}
	}
	return 0, false
}

// containsDateFunction reports whether a formula calls TODAY() or NOW()
// outside of string literals, case-insensitively.
func containsDateFunction(formula string) bool {
	upper := strings.ToUpper(formula)
	inString := false
	for i := 0; i < len(upper); i++ {
		switch upper[i] {
		case '"':
			inString = !inString
		case 'T', 'N':
			if inString {
				continue
			}
			if strings.HasPrefix(upper[i:], "TODAY(") || strings.HasPrefix(upper[i:], "NOW(") {
				return true
			}
		}
	}
	return false
}
