package main

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"tuisheet/internal/xlsx"
)

func (a *app) handlePropertiesKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "e", "E":
		a.propEditOrig = a.docProps
		a.propEditField = 0
		a.propEditInput = a.propFieldValue(0)
		a.propEditCursor = len([]rune(a.propEditInput))
		a.mode = modePropertiesEdit
		return nil
	case "esc", "q", "Q":
		a.mode = modeNormal
		return nil
	case "enter":
		a.mode = modeNormal
		return nil
	case "up":
		if a.propertiesScroll > 0 {
			a.propertiesScroll--
		}
	case "down":
		// scroll handled via renderScrollablePopup's own scroll? we manage manually
		// Just increment, popup will clamp
		a.propertiesScroll++
	case "pgup":
		a.propertiesScroll -= a.popupPageStep()
		if a.propertiesScroll < 0 {
			a.propertiesScroll = 0
		}
	case "pgdown":
		a.propertiesScroll += a.popupPageStep()
	case "home", "ctrl+a":
		a.propertiesScroll = 0
	case "end", "ctrl+e":
		a.propertiesScroll = len(a.propertiesLines)
	}
	return nil
}

var propFields = []struct {
	label string
	get   func(*xlsx.DocProps) string
	set   func(*xlsx.DocProps, string)
}{
	{"Title", func(p *xlsx.DocProps) string { return p.Title }, func(p *xlsx.DocProps, v string) { p.Title = v }},
	{"Subject", func(p *xlsx.DocProps) string { return p.Subject }, func(p *xlsx.DocProps, v string) { p.Subject = v }},
	{"Creator / Author", func(p *xlsx.DocProps) string { return p.Creator }, func(p *xlsx.DocProps, v string) { p.Creator = v }},
	{"Last Modified By", func(p *xlsx.DocProps) string { return p.LastModifiedBy }, func(p *xlsx.DocProps, v string) { p.LastModifiedBy = v }},
	{"Keywords", func(p *xlsx.DocProps) string { return p.Keywords }, func(p *xlsx.DocProps, v string) { p.Keywords = v }},
	{"Description", func(p *xlsx.DocProps) string { return p.Description }, func(p *xlsx.DocProps, v string) { p.Description = v }},
	{"Category", func(p *xlsx.DocProps) string { return p.Category }, func(p *xlsx.DocProps, v string) { p.Category = v }},
	{"Content Status", func(p *xlsx.DocProps) string { return p.ContentStatus }, func(p *xlsx.DocProps, v string) { p.ContentStatus = v }},
	{"Company", func(p *xlsx.DocProps) string { return p.Company }, func(p *xlsx.DocProps, v string) { p.Company = v }},
	{"Manager", func(p *xlsx.DocProps) string { return p.Manager }, func(p *xlsx.DocProps, v string) { p.Manager = v }},
}

var propFieldLabels = func() []string {
	out := make([]string, len(propFields))
	for i, f := range propFields {
		out[i] = f.label
	}
	return out
}()

func (a *app) propFieldValue(idx int) string {
	if idx < 0 || idx >= len(propFields) {
		return ""
	}
	return propFields[idx].get(&a.propEditOrig)
}

func (a *app) commitPropField() {
	a.setPropFieldValue(a.propEditField, a.propEditInput)
}

func (a *app) handlePropertiesEditKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		a.mode = modeProperties
		a.message = "Properties edit cancelled"
		return nil
	case "enter", "ctrl+s":
		a.commitPropField()
		// Save via snapshot/undo
		a.takeSnapshot("Edit properties")
		a.docProps = a.propEditOrig
		a.docPropsDirty = true
		a.dirty = true
		a.propertiesLines = a.buildPropertiesLines()
		a.propertiesScroll = 0
		a.mode = modeProperties
		a.message = "Properties updated (save to apply)"
		return nil
	case "tab", "down":
		a.commitPropField()
		a.propEditField = (a.propEditField + 1) % len(propFieldLabels)
		a.propEditInput = a.propFieldValue(a.propEditField)
		a.propEditCursor = len([]rune(a.propEditInput))
		return nil
	case "shift+tab", "up":
		a.commitPropField()
		a.propEditField = (a.propEditField - 1 + len(propFieldLabels)) % len(propFieldLabels)
		a.propEditInput = a.propFieldValue(a.propEditField)
		a.propEditCursor = len([]rune(a.propEditInput))
		return nil
	case "left":
		lineMoveLeft(&a.propEditCursor)
		return nil
	case "right":
		lineMoveRight(a.propEditInput, &a.propEditCursor)
		return nil
	case "home", "ctrl+a":
		lineHome(&a.propEditCursor)
		return nil
	case "end", "ctrl+e":
		lineEnd(a.propEditInput, &a.propEditCursor)
		return nil
	case "backspace", "ctrl+h":
		lineBackspace(&a.propEditInput, &a.propEditCursor)
		return nil
	case "delete":
		runes := []rune(a.propEditInput)
		if a.propEditCursor < len(runes) {
			a.propEditInput = string(append(runes[:a.propEditCursor], runes[a.propEditCursor+1:]...))
		}
		return nil
	default:
		if len(msg.Runes) > 0 {
			// printable
			runes := []rune(a.propEditInput)
			ins := msg.Runes
			newRunes := make([]rune, 0, len(runes)+len(ins))
			newRunes = append(newRunes, runes[:a.propEditCursor]...)
			newRunes = append(newRunes, ins...)
			newRunes = append(newRunes, runes[a.propEditCursor:]...)
			a.propEditInput = string(newRunes)
			a.propEditCursor += len(ins)
		}
		return nil
	}
}

func (a *app) buildPropertiesLines() []string {
	var lines []string
	// File info
	if a.currentFile != "" {
		fullPath := a.currentFile
		if abs, err := filepath.Abs(a.currentFile); err == nil {
			fullPath = abs
		}
		lines = append(lines, " File: "+fullPath)
		if fi, err := os.Stat(a.currentFile); err == nil {
			sz := fi.Size()
			var sizeStr string
			if sz >= 1024*1024 {
				sizeStr = fmt.Sprintf(" Size: %.2f MiB", float64(sz)/(1024*1024))
			} else if sz >= 1024 {
				sizeStr = fmt.Sprintf(" Size: %.2f KiB", float64(sz)/1024)
			} else {
				sizeStr = fmt.Sprintf(" Size: %d bytes", sz)
			}
			lines = append(lines, sizeStr)
			lines = append(lines, " Modified (fs): "+fi.ModTime().Local().Format("2006-01-02 15:04:05"))
		}
	} else {
		lines = append(lines, " File: (unsaved)")
	}
	lines = append(lines, fmt.Sprintf(" Sheets: %d", len(a.sheets)))
	lines = append(lines, "")
	if a.docPropsDirty {
		// Show pending edited props (manual values) plus stamp preview for new files
		lines = append(lines, a.formatDocPropsLines(a.docProps)...)
		lines = append(lines, "")
		lines = append(lines, style(sgrItalic, " (pending — save to apply)"))
	} else if a.currentFile != "" {
		props := readXlsxProperties(a.currentFile)
		if len(props) == 0 {
			lines = append(lines, " No extended properties found in docProps/core.xml or docProps/app.xml")
		} else {
			lines = append(lines, props...)
		}
	} else {
		lines = append(lines, " No file properties (file not saved)")
		if a.docPropsDirty {
			lines = append(lines, a.formatDocPropsLines(a.docProps)...)
		}
	}
	// Also show workbook-level info like calcPr? we already cover
	return lines
}

func (a *app) formatDocPropsLines(p xlsx.DocProps) []string {
	var out []string
	if strings.TrimSpace(p.Title) != "" {
		out = append(out, fmt.Sprintf(" Title: %s", strings.TrimSpace(p.Title)))
	}
	if strings.TrimSpace(p.Subject) != "" {
		out = append(out, fmt.Sprintf(" Subject: %s", strings.TrimSpace(p.Subject)))
	}
	if strings.TrimSpace(p.Creator) != "" {
		out = append(out, fmt.Sprintf(" Creator / Author: %s", strings.TrimSpace(p.Creator)))
	}
	if strings.TrimSpace(p.LastModifiedBy) != "" {
		out = append(out, fmt.Sprintf(" Last Modified By: %s", strings.TrimSpace(p.LastModifiedBy)))
	}
	if strings.TrimSpace(p.Keywords) != "" {
		out = append(out, fmt.Sprintf(" Keywords: %s", strings.TrimSpace(p.Keywords)))
	}
	if strings.TrimSpace(p.Description) != "" {
		out = append(out, fmt.Sprintf(" Description: %s", strings.TrimSpace(p.Description)))
	}
	if strings.TrimSpace(p.Category) != "" {
		out = append(out, fmt.Sprintf(" Category: %s", strings.TrimSpace(p.Category)))
	}
	if strings.TrimSpace(p.ContentStatus) != "" {
		out = append(out, fmt.Sprintf(" Content Status: %s", strings.TrimSpace(p.ContentStatus)))
	}
	if len(out) > 0 {
		out = append(out, "")
	}
	out = append(out, " [Extended Properties]")
	if strings.TrimSpace(p.Company) != "" {
		out = append(out, fmt.Sprintf(" Company: %s", strings.TrimSpace(p.Company)))
	}
	if strings.TrimSpace(p.Manager) != "" {
		out = append(out, fmt.Sprintf(" Manager: %s", strings.TrimSpace(p.Manager)))
	}
	return out
}

func readXlsxProperties(filename string) []string {
	zr, err := zip.OpenReader(filename)
	if err != nil {
		return nil
	}
	defer zr.Close()
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		files[f.Name] = f
	}
	var out []string
	// core.xml
	if f, ok := files["docProps/core.xml"]; ok {
		data, err := readZipFileBytes(f)
		if err == nil {
			m := parseGenericXMLProps(data)
			order := []string{"title", "subject", "creator", "lastModifiedBy", "keywords", "description", "category", "contentStatus", "contentType", "revision", "created", "modified", "lastPrinted", "identifier", "language", "version"}
			labels := map[string]string{
				"title": "Title", "subject": "Subject", "creator": "Creator / Author", "lastModifiedBy": "Last Modified By",
				"keywords": "Keywords", "description": "Description", "category": "Category", "contentStatus": "Content Status",
				"contentType": "Content Type", "revision": "Revision", "created": "Created", "modified": "Modified",
				"lastPrinted": "Last Printed", "identifier": "Identifier", "language": "Language", "version": "Version",
			}
			for _, k := range order {
				if v, ok := m[k]; ok && strings.TrimSpace(v) != "" {
					lbl := labels[k]
					if lbl == "" {
						lbl = k
					}
					out = append(out, fmt.Sprintf(" %s: %s", lbl, strings.TrimSpace(v)))
				}
			}
			// any other keys
			for k, v := range m {
				found := false
				for _, o := range order {
					if o == k {
						found = true
						break
					}
				}
				if !found && strings.TrimSpace(v) != "" {
					out = append(out, fmt.Sprintf(" %s: %s", k, strings.TrimSpace(v)))
				}
			}
		}
	}
	// app.xml
	if f, ok := files["docProps/app.xml"]; ok {
		data, err := readZipFileBytes(f)
		if err == nil {
			m := parseGenericXMLProps(data)
			order := []string{"Application", "AppVersion", "Company", "Manager", "HyperlinkBase", "HyperlinksChanged", "LinksUpToDate", "ScaleCrop", "SharedDoc", "Template", "TotalTime", "Words", "Characters", "Lines", "Paragraphs", "Pages", "Slides", "Notes", "HiddenSlides", "MMClips", "DocSecurity", "HeadingPairs", "TitlesOfParts"}
			labels := map[string]string{
				"Application": "Application", "AppVersion": "AppVersion", "Company": "Company", "Manager": "Manager",
				"TotalTime": "Total Editing Time (mins)", "Words": "Words", "Characters": "Characters",
			}
			if len(out) > 0 {
				out = append(out, "")
			}
			// Title separator
			out = append(out, " [Extended Properties]")
			for _, k := range order {
				if v, ok := m[k]; ok && strings.TrimSpace(v) != "" {
					lbl := labels[k]
					if lbl == "" {
						lbl = k
					}
					out = append(out, fmt.Sprintf(" %s: %s", lbl, strings.TrimSpace(v)))
				}
			}
			for k, v := range m {
				found := false
				for _, o := range order {
					if o == k {
						found = true
						break
					}
				}
				if !found && strings.TrimSpace(v) != "" {
					// skip HeadingPairs inner details which are already verbose
					if k == "HeadingPairs" || k == "TitlesOfParts" {
						continue
					}
					out = append(out, fmt.Sprintf(" %s: %s", k, strings.TrimSpace(v)))
				}
			}
		}
	}
	// custom.xml if present (custom properties)
	if f, ok := files["docProps/custom.xml"]; ok {
		data, err := readZipFileBytes(f)
		if err == nil {
			m := parseGenericXMLProps(data)
			if len(m) > 0 {
				if len(out) > 0 {
					out = append(out, "")
				}
				out = append(out, " [Custom Properties]")
				for k, v := range m {
					if strings.TrimSpace(v) != "" {
						out = append(out, fmt.Sprintf(" %s: %s", k, strings.TrimSpace(v)))
					}
				}
			}
		}
	}
	// docMetadata/LabelInfo.xml if present (MIP sensitivity label) – view only
	if f, ok := files["docMetadata/LabelInfo.xml"]; ok {
		data, err := readZipFileBytes(f)
		if err == nil && len(data) > 0 {
			m := parseLabelInfoProps(data)
			if len(m) > 0 {
				if len(out) > 0 {
					out = append(out, "")
				}
				out = append(out, " [Sensitivity Label]")
				// show in stable order
				for _, k := range []string{"labelId", "id", "enabled", "method", "siteId", "contentBits", "removed"} {
					if v, ok := m[k]; ok && strings.TrimSpace(v) != "" {
						out = append(out, fmt.Sprintf(" %s: %s", k, strings.TrimSpace(v)))
					}
				}
				for k, v := range m {
					found := false
					for _, o := range []string{"labelId", "id", "enabled", "method", "siteId", "contentBits", "removed"} {
						if o == k {
							found = true
							break
						}
					}
					if !found && strings.TrimSpace(v) != "" {
						out = append(out, fmt.Sprintf(" %s: %s", k, strings.TrimSpace(v)))
					}
				}
			}
		}
	}
	return out
}

func parseLabelInfoProps(data []byte) map[string]string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	out := make(map[string]string)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "label" {
			for _, a := range se.Attr {
				if strings.TrimSpace(a.Value) != "" {
					out[a.Name.Local] = a.Value
				}
			}
			// also capture inner text if any
		}
	}
	return out
}

func parseGenericXMLProps(data []byte) map[string]string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	out := make(map[string]string)
	var stack []string
	var curText string
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			// push local name
			stack = append(stack, t.Name.Local)
			curText = ""
			// also check attributes for custom properties: name attribute
			for _, a := range t.Attr {
				if a.Name.Local == "name" {
					// for custom.xml, we want to remember name
					// we store as pending key? Use attribute as key via stack top
					// We'll replace last stack entry with attribute value if this is property
					if len(stack) > 0 && (stack[len(stack)-1] == "property") {
						// use name attr as key, will be resolved on EndElement
						stack[len(stack)-1] = a.Value
					}
				}
			}
		case xml.CharData:
			curText += string(t)
		case xml.EndElement:
			if len(stack) == 0 {
				continue
			}
			name := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			txt := strings.TrimSpace(curText)
			if txt != "" && len(stack) <= 2 {
				// top-level properties: store directly
				// Heuristic: ignore container elements
				if _, ok := out[name]; !ok {
					out[name] = txt
				} else {
					// append if duplicate?
					out[name] = out[name] + ", " + txt
				}
			} else if txt != "" && name != "" {
				// also store if leaf
				if txt != "" && len(name) < 40 {
					if _, ok := out[name]; !ok {
						// only if not already and name looks like property
						// avoid storing large inner xml like HeadingPairs details split
						if len(txt) < 500 {
							out[name] = txt
						}
					}
				}
			}
			curText = ""
		}
	}
	return out
}

func (a *app) withPropertiesPopup(lines []string) []string {
	a.renderScrollablePopup(lines, " Properties - Press E to edit ", a.propertiesLines, &a.propertiesScroll)
	return lines
}

func (a *app) withPropertiesEditPopup(lines []string) []string {
	boxW := max(56, a.width*7/10)
	if boxW > a.width-2 {
		boxW = a.width - 2
	}
	boxX := (a.width - boxW) / 2
	innerW := boxW - 2
	title := " Edit Properties "
	var content []string
	content = append(content, "")
	for i, lbl := range propFieldLabels {
		val := a.propFieldValue(i)
		if i == a.propEditField {
			val = a.propEditInput
			runes := []rune(val)
			cur := a.propEditCursor
			if cur < 0 {
				cur = 0
			}
			if cur > len(runes) {
				cur = len(runes)
			}
			var b strings.Builder
			for idx, r := range runes {
				if idx == cur {
					b.WriteString(style(sgrReverseVideo, string(r)))
				} else {
					b.WriteString(string(r))
				}
			}
			if cur == len(runes) {
				b.WriteString(style(sgrReverseVideo, " "))
			}
			val = b.String()
			line := fmt.Sprintf(" %s: [%s]", lbl, val)
			content = append(content, fitDisplay(line, innerW))
		} else {
			line := fmt.Sprintf(" %s: [%s]", lbl, val)
			content = append(content, fitDisplay(line, innerW))
		}
	}
	content = append(content, "")
	content = append(content, fitDisplay(" Tab/Up/Down switch field  Enter/Ctrl+S save  Esc cancel", innerW))
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
			tw := displayWidth(title)
			dash := max(0, innerW-tw)
			boxLine = "┌" + style(sgrReverseVideo, title) + strings.Repeat("─", dash) + "┐"
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
