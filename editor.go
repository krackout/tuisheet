package main

import "unicode/utf8"

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
