package xlsx

import (
	"strings"
)

// xlfnPrefixed lists functions that Excel stores with the "_xlfn." prefix
// in worksheet XML because they post-date the original SpreadsheetML
// function baseline (ISO/IEC 29500-1 §18.17). Writing them unprefixed
// makes Excel show #NAME?. Extend the list as new functions are added.
var xlfnPrefixed = map[string]bool{
	"XLOOKUP": true,
	"IFS":     true,
	"DAYS":    true, // Excel 2013+
	"CONCAT":  true, // Excel 2016+
}

const xlfnPrefix = "_xlfn."

// PrefixXLFN rewrites a formula for storage so modern function names carry
// Excel's _xlfn. prefix. String literals are skipped.
func PrefixXLFN(formula string) string {
	return mapFunctionNames(formula, func(name string, argsFollow bool) string {
		if argsFollow && xlfnPrefixed[strings.ToUpper(name)] {
			return xlfnPrefix + name
		}
		return name
	})
}

// StripXLFN removes _xlfn. prefixes when loading so the application's
// internal formula text stays clean and round-trips through the UI.
func StripXLFN(formula string) string {
	return mapFunctionNames(formula, func(name string, argsFollow bool) string {
		up := strings.ToUpper(name)
		if argsFollow && strings.HasPrefix(up, strings.ToUpper(xlfnPrefix)) &&
			xlfnPrefixed[up[len(xlfnPrefix):]] {
			return name[len(xlfnPrefix):]
		}
		return name
	})
}

// mapFunctionNames walks formula text outside string literals and lets fn
// rewrite each identifier that is immediately followed by '('. argsFollow
// reports whether the identifier is a call; non-call identifiers pass
// through untouched (they may be cell refs or TRUE/FALSE literals).
func mapFunctionNames(formula string, fn func(name string, argsFollow bool) string) string {
	if !strings.Contains(formula, "(") {
		return formula
	}
	var out strings.Builder
	i := 0
	for i < len(formula) {
		ch := formula[i]
		if ch == '"' {
			j := i + 1
			for j < len(formula) {
				if formula[j] == '\\' {
					j += 2
					continue
				}
				if formula[j] == '"' {
					j++
					break
				}
				j++
			}
			out.WriteString(formula[i:j])
			i = j
			continue
		}
		isIdentStart := ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch == '_' || ch >= 0x80
		if !isIdentStart {
			out.WriteByte(ch)
			i++
			continue
		}
		start := i
		for i < len(formula) {
			c := formula[i]
			if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' ||
				c == '_' || c == '.' || c >= 0x80 {
				i++
				continue
			}
			break
		}
		name := formula[start:i]
		argsFollow := i < len(formula) && formula[i] == '('
		out.WriteString(fn(name, argsFollow))
	}
	return out.String()
}
