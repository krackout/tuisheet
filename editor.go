package main

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// filterControlRunes drops control characters from typed/pasted input,
// except tab/newline/CR which cells can legitimately hold. In particular
// it drops the NUL rune Windows delivers for bare modifier presses
// (coninput reports Char=0 for Ctrl/Alt alone), which would otherwise
// open edit mode with an invisible buffer or be stored in cells.
// Cf format chars (e.g. ZWJ in emoji) are kept for paste compatibility.
func filterControlRunes(s string) string {
	clean := strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return clean
}

// lineEditor helpers — unified rune-aware cursor handling for all
// text fields (edit, fileInput, rename, search, save, props, colWidth).
// Keeps handlers small and avoids 7× copy-paste of []rune logic.

func lineInsert(text *string, cursor *int, s string) {
	if s == "" {
		return
	}
	runes := []rune(*text)
	ins := []rune(s)
	if *cursor < 0 {
		*cursor = 0
	}
	if *cursor > len(runes) {
		*cursor = len(runes)
	}
	nr := make([]rune, 0, len(runes)+len(ins))
	nr = append(nr, runes[:*cursor]...)
	nr = append(nr, ins...)
	nr = append(nr, runes[*cursor:]...)
	*text = string(nr)
	*cursor += len(ins)
}

func lineBackspace(text *string, cursor *int) {
	if *cursor <= 0 {
		return
	}
	runes := []rune(*text)
	if *cursor > len(runes) {
		*cursor = len(runes)
	}
	*text = string(append(runes[:*cursor-1], runes[*cursor:]...))
	*cursor--
}

func lineMoveLeft(cursor *int) {
	if *cursor > 0 {
		*cursor--
	}
}

func lineMoveRight(text string, cursor *int) {
	if *cursor < utf8.RuneCountInString(text) {
		*cursor++
	}
}

func lineHome(cursor *int) { *cursor = 0 }

func lineEnd(text string, cursor *int) { *cursor = utf8.RuneCountInString(text) }
