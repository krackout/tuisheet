package xlsx

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"regexp"
	"strings"
	"time"

	"tuisheet/internal/buildinfo"
)

type DocProps struct {
	Title          string
	Subject        string
	Creator        string
	LastModifiedBy string
	Keywords       string
	Description    string
	Category       string
	ContentStatus  string
	Company        string
	Manager        string
}

// isNewFileProps indicates whether to stamp Application/AppVersion.
// Called when originalFiles has no docProps or file is brand new.
func needsDocPropsStamp(originalFiles map[string][]byte) bool {
	if originalFiles == nil {
		return true
	}
	_, hasCore := originalFiles["docProps/core.xml"]
	_, hasApp := originalFiles["docProps/app.xml"]
	return !hasCore && !hasApp
}

func parseGenericPropsMap(data []byte) map[string]string {
	if len(data) == 0 {
		return nil
	}
	m := map[string]string{}
	dec := xml.NewDecoder(bytes.NewReader(data))
	var stack []string
	var curText string
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			// custom.xml stores name in property/@name
			if name == "property" {
				for _, a := range t.Attr {
					if a.Name.Local == "name" {
						stack = append(stack, "property")
						// push name as pseudo element
						// store attr for later end
						curText = ""
						stack[len(stack)-1] = a.Value
						break
					}
				}
				if len(stack) == 0 || stack[len(stack)-1] != t.Attr[0].Value {
					stack = append(stack, name)
				}
			} else {
				stack = append(stack, name)
			}
			curText = ""
		case xml.CharData:
			curText += string(t)
		case xml.EndElement:
			if len(stack) == 0 {
				continue
			}
			key := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			v := strings.TrimSpace(curText)
			curText = ""
			if v == "" {
				continue
			}
			// skip container elements
			if key == "coreProperties" || key == "Properties" || key == "properties" || key == "HeadingPairs" || key == "TitlesOfParts" {
				continue
			}
			// custom.xml: key is property name already
			if _, ok := m[key]; !ok {
				m[key] = v
			}
		}
	}
	return m
}

func docPropsFromFiles(originalFiles map[string][]byte) DocProps {
	var p DocProps
	if originalFiles == nil {
		return p
	}
	if data, ok := originalFiles["docProps/core.xml"]; ok {
		m := parseGenericPropsMap(data)
		p.Title = m["title"]
		p.Subject = m["subject"]
		p.Creator = m["creator"]
		p.LastModifiedBy = m["lastModifiedBy"]
		p.Keywords = m["keywords"]
		p.Description = m["description"]
		p.Category = m["category"]
		p.ContentStatus = m["contentStatus"]
	}
	if data, ok := originalFiles["docProps/app.xml"]; ok {
		m := parseGenericPropsMap(data)
		p.Company = m["Company"]
		p.Manager = m["Manager"]
	}
	return p
}

func LoadDocProps(filename string) (DocProps, bool) {
	files, err := readZipEntries(filename)
	if err != nil {
		return DocProps{}, false
	}
	if _, ok := files["docProps/core.xml"]; !ok {
		if _, ok2 := files["docProps/app.xml"]; !ok2 {
			return DocProps{}, false
		}
	}
	p := docPropsFromFiles(files)
	return p, true
}

func patchCoreXML(orig []byte, p DocProps, isNewFile bool) []byte {
	s := string(orig)
	// helper to upsert/delete element
	upsert := func(tagOpen, tagClose, value string) {
		// tagOpen like "<dc:title>", tagClose like "</dc:title>"
		re := mustRegexp(tagOpen + `.*?` + tagClose)
		trim := strings.TrimSpace(value)
		if trim != "" {
			escaped := xmlEscape(trim)
			repl := tagOpen + escaped + tagClose
			if re.MatchString(s) {
				s = re.ReplaceAllString(s, repl)
			} else {
				// insert before closing coreProperties
				s = strings.Replace(s, "</cp:coreProperties>", repl+"</cp:coreProperties>", 1)
				if !strings.Contains(s, repl) {
					s = strings.Replace(s, "</coreProperties>", repl+"</coreProperties>", 1)
				}
			}
		} else {
			// delete if exists
			s = re.ReplaceAllString(s, "")
		}
	}
	upsert("<dc:title>", "</dc:title>", p.Title)
	upsert("<dc:subject>", "</dc:subject>", p.Subject)
	upsert("<dc:creator>", "</dc:creator>", p.Creator)
	upsert("<cp:keywords>", "</cp:keywords>", p.Keywords)
	upsert("<dc:description>", "</dc:description>", p.Description)
	upsert("<cp:lastModifiedBy>", "</cp:lastModifiedBy>", p.LastModifiedBy)
	upsert("<cp:category>", "</cp:category>", p.Category)
	upsert("<cp:contentStatus>", "</cp:contentStatus>", p.ContentStatus)
	if isNewFile {
		now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		if !strings.Contains(s, "dcterms:created") {
			s = strings.Replace(s, "</cp:coreProperties>", `<dcterms:created xsi:type="dcterms:W3CDTF">`+now+`</dcterms:created></cp:coreProperties>`, 1)
		}
		// always update modified
		if strings.Contains(s, "dcterms:modified") {
			re := mustRegexp(`<dcterms:modified[^>]*>.*?</dcterms:modified>`)
			s = re.ReplaceAllString(s, `<dcterms:modified xsi:type="dcterms:W3CDTF">`+now+`</dcterms:modified>`)
		} else {
			s = strings.Replace(s, "</cp:coreProperties>", `<dcterms:modified xsi:type="dcterms:W3CDTF">`+now+`</dcterms:modified></cp:coreProperties>`, 1)
		}
		// ensure dcmitype namespace present
		if !strings.Contains(s, "xmlns:dcmitype") {
			s = strings.Replace(s, `xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"`, `xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"`, 1)
		}
	}
	return []byte(s)
}

func patchAppXML(orig []byte, p DocProps, isNewFile bool, sheetNames []string) []byte {
	s := string(orig)
	upsert := func(tag, value string) {
		trim := strings.TrimSpace(value)
		open := "<" + tag + ">"
		close := "</" + tag + ">"
		re := mustRegexp(open + `.*?` + close)
		if trim != "" {
			escaped := xmlEscape(trim)
			repl := open + escaped + close
			if re.MatchString(s) {
				s = re.ReplaceAllString(s, repl)
			} else {
				s = strings.Replace(s, "</Properties>", repl+"</Properties>", 1)
			}
		} else {
			s = re.ReplaceAllString(s, "")
		}
	}
	upsert("Company", p.Company)
	upsert("Manager", p.Manager)
	if isNewFile {
		upsert("Application", buildinfo.AppName)
		upsert("AppVersion", buildinfo.AppVersion)
	}
	// Ensure AppVersion is Excel-accepted (must contain dot); fix legacy timestamp without dot
	if strings.Contains(s, "<AppVersion>") {
		reAV := mustRegexp(`<AppVersion>.*?</AppVersion>`)
		if m := reAV.FindString(s); m != "" && !strings.Contains(m, ".") {
			s = reAV.ReplaceAllString(s, "<AppVersion>"+xmlEscape(buildinfo.AppVersion)+"</AppVersion>")
		}
	}
	// Ensure required extended properties to avoid Excel repair
	if !strings.Contains(s, "<DocSecurity>") {
		s = strings.Replace(s, "</Properties>", "<DocSecurity>0</DocSecurity></Properties>", 1)
	}
	if !strings.Contains(s, "<ScaleCrop>") {
		s = strings.Replace(s, "</Properties>", "<ScaleCrop>false</ScaleCrop></Properties>", 1)
	}
	if !strings.Contains(s, "<LinksUpToDate>") {
		s = strings.Replace(s, "</Properties>", "<LinksUpToDate>false</LinksUpToDate></Properties>", 1)
	}
	if !strings.Contains(s, "<SharedDoc>") {
		s = strings.Replace(s, "</Properties>", "<SharedDoc>false</SharedDoc></Properties>", 1)
	}
	if !strings.Contains(s, "<HyperlinksChanged>") {
		s = strings.Replace(s, "</Properties>", "<HyperlinksChanged>false</HyperlinksChanged></Properties>", 1)
	}
	// Fix malformed app.xml that has bare <i4>/<lpstr> without HeadingPairs/TitlesOfParts wrappers (e.g., legacy tuisheet saves).
	// Detect missing HeadingPairs and bare tags.
	if !strings.Contains(s, "<HeadingPairs>") && (strings.Contains(s, "<i4>") || strings.Contains(s, "<lpstr>")) {
		// Extract heading name from first bare lpstr if present, fallback to Worksheets
		headingName := "Worksheets"
		if m := regexp.MustCompile(`<lpstr>(.*?)</lpstr>`).FindStringSubmatch(s); len(m) == 2 {
			if strings.TrimSpace(m[1]) != "" {
				headingName = strings.TrimSpace(m[1])
			}
		}
		// Remove bare i4/lpstr (direct children of Properties) – they will be replaced by proper vectors
		// Use regex to remove them only when not inside vt: namespace
		reI4 := regexp.MustCompile(`<i4>.*?</i4>`)
		reLp := regexp.MustCompile(`<lpstr>.*?</lpstr>`)
		// If they are inside <vt:lpstr> or <vt:i4>, they contain colon, so bare pattern won't match vt: prefix
		s = reI4.ReplaceAllString(s, "")
		s = reLp.ReplaceAllString(s, "")
		// Build HeadingPairs/TitlesOfParts
		sheetCount := len(sheetNames)
		if sheetCount == 0 {
			sheetCount = 1
		}
		headingPairs := fmt.Sprintf(`<HeadingPairs><vt:vector size="2" baseType="variant"><vt:variant><vt:lpstr>%s</vt:lpstr></vt:variant><vt:variant><vt:i4>%d</vt:i4></vt:variant></vt:vector></HeadingPairs>`, xmlEscape(headingName), sheetCount)
		var titlesBuilder strings.Builder
		titlesBuilder.WriteString(fmt.Sprintf(`<TitlesOfParts><vt:vector size="%d" baseType="lpstr">`, sheetCount))
		if len(sheetNames) == 0 {
			titlesBuilder.WriteString(`<vt:lpstr>Sheet1</vt:lpstr>`)
		} else {
			for _, n := range sheetNames {
				titlesBuilder.WriteString(`<vt:lpstr>`)
				titlesBuilder.WriteString(xmlEscape(n))
				titlesBuilder.WriteString(`</vt:lpstr>`)
			}
		}
		titlesBuilder.WriteString(`</vt:vector></TitlesOfParts>`)
		s = strings.Replace(s, "</Properties>", headingPairs+titlesBuilder.String()+"</Properties>", 1)
	}
	return []byte(s)
}

func mustRegexp(pattern string) *regexp.Regexp {
	// use (?s) to match newlines not needed, simple
	return regexp.MustCompile(pattern)
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func corePropsMap(p DocProps, origMap map[string]string, isNewFile bool) map[string]string {
	out := map[string]string{}
	// preserve non-editable keys from orig
	for k, v := range origMap {
		switch k {
		case "title", "subject", "creator", "lastModifiedBy", "keywords", "description", "category", "contentStatus":
			// will be set from p
		default:
			out[k] = v
		}
	}
	// apply editable, delete if empty
	setOrDelete := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			out[k] = strings.TrimSpace(v)
		}
	}
	setOrDelete("title", p.Title)
	setOrDelete("subject", p.Subject)
	setOrDelete("creator", p.Creator)
	setOrDelete("lastModifiedBy", p.LastModifiedBy)
	setOrDelete("keywords", p.Keywords)
	setOrDelete("description", p.Description)
	setOrDelete("category", p.Category)
	setOrDelete("contentStatus", p.ContentStatus)
	if isNewFile {
		now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
		if _, ok := out["created"]; !ok {
			out["created"] = now
		}
		out["modified"] = now
		// ensure dcmitype namespace handled via generate
	}
	return out
}

func generateCoreXML(propsMap map[string]string) []byte {
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	buf.WriteString(`<cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/" xmlns:dcterms="http://purl.org/dc/terms/" xmlns:dcmitype="http://purl.org/dc/dcmitype/" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">`)
	// order to match typical Excel output; only emit if present
	if v, ok := propsMap["title"]; ok {
		buf.WriteString(`<dc:title>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</dc:title>`)
	}
	if v, ok := propsMap["subject"]; ok {
		buf.WriteString(`<dc:subject>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</dc:subject>`)
	}
	if v, ok := propsMap["creator"]; ok {
		buf.WriteString(`<dc:creator>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</dc:creator>`)
	}
	if v, ok := propsMap["keywords"]; ok {
		buf.WriteString(`<cp:keywords>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</cp:keywords>`)
	}
	if v, ok := propsMap["description"]; ok {
		buf.WriteString(`<dc:description>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</dc:description>`)
	}
	if v, ok := propsMap["lastModifiedBy"]; ok {
		buf.WriteString(`<cp:lastModifiedBy>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</cp:lastModifiedBy>`)
	}
	if v, ok := propsMap["category"]; ok {
		buf.WriteString(`<cp:category>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</cp:category>`)
	}
	if v, ok := propsMap["contentStatus"]; ok {
		buf.WriteString(`<cp:contentStatus>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</cp:contentStatus>`)
	}
	// preserve other known keys if present
	if v, ok := propsMap["contentType"]; ok {
		buf.WriteString(`<cp:contentType>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</cp:contentType>`)
	}
	if v, ok := propsMap["revision"]; ok {
		buf.WriteString(`<cp:revision>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</cp:revision>`)
	}
	if v, ok := propsMap["version"]; ok {
		buf.WriteString(`<cp:version>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</cp:version>`)
	}
	if v, ok := propsMap["created"]; ok {
		buf.WriteString(`<dcterms:created xsi:type="dcterms:W3CDTF">`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</dcterms:created>`)
	}
	if v, ok := propsMap["modified"]; ok {
		buf.WriteString(`<dcterms:modified xsi:type="dcterms:W3CDTF">`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</dcterms:modified>`)
	}
	if v, ok := propsMap["lastPrinted"]; ok {
		buf.WriteString(`<cp:lastPrinted>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</cp:lastPrinted>`)
	}
	if v, ok := propsMap["identifier"]; ok {
		buf.WriteString(`<dc:identifier>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</dc:identifier>`)
	}
	if v, ok := propsMap["language"]; ok {
		buf.WriteString(`<dc:language>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</dc:language>`)
	}
	// any remaining unknown keys
	known := map[string]bool{"title": true, "subject": true, "creator": true, "keywords": true, "description": true, "lastModifiedBy": true, "category": true, "contentStatus": true, "contentType": true, "revision": true, "version": true, "created": true, "modified": true, "lastPrinted": true, "identifier": true, "language": true}
	for k, v := range propsMap {
		if known[k] {
			continue
		}
		buf.WriteString("<cp:")
		buf.WriteString(k)
		buf.WriteString(">")
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString("</cp:")
		buf.WriteString(k)
		buf.WriteString(">")
	}
	buf.WriteString(`</cp:coreProperties>`)
	return buf.Bytes()
}

func appPropsMap(p DocProps, origMap map[string]string, isNewFile bool) map[string]string {
	out := map[string]string{}
	for k, v := range origMap {
		out[k] = v
	}
	if strings.TrimSpace(p.Company) != "" {
		out["Company"] = strings.TrimSpace(p.Company)
	} else {
		delete(out, "Company")
	}
	if strings.TrimSpace(p.Manager) != "" {
		out["Manager"] = strings.TrimSpace(p.Manager)
	} else {
		delete(out, "Manager")
	}
	if isNewFile {
		out["Application"] = buildinfo.AppName
		out["AppVersion"] = buildinfo.AppVersion
		if _, ok := out["DocSecurity"]; !ok {
			out["DocSecurity"] = "0"
		}
		if _, ok := out["ScaleCrop"]; !ok {
			out["ScaleCrop"] = "false"
		}
		if _, ok := out["LinksUpToDate"]; !ok {
			out["LinksUpToDate"] = "false"
		}
		if _, ok := out["SharedDoc"]; !ok {
			out["SharedDoc"] = "false"
		}
		if _, ok := out["HyperlinksChanged"]; !ok {
			out["HyperlinksChanged"] = "false"
		}
	}
	return out
}

func generateAppXML(propsMap map[string]string) []byte {
	var buf bytes.Buffer
	buf.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	buf.WriteString(`<Properties xmlns="http://schemas.openxmlformats.org/officeDocument/2006/extended-properties" xmlns:vt="http://schemas.openxmlformats.org/officeDocument/2006/docPropsVTypes">`)
	// emit Application/AppVersion first if present (conventional order)
	if v, ok := propsMap["Application"]; ok {
		buf.WriteString(`<Application>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</Application>`)
	}
	if v, ok := propsMap["AppVersion"]; ok {
		buf.WriteString(`<AppVersion>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</AppVersion>`)
	}
	if v, ok := propsMap["Company"]; ok {
		buf.WriteString(`<Company>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</Company>`)
	}
	if v, ok := propsMap["Manager"]; ok {
		buf.WriteString(`<Manager>`)
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString(`</Manager>`)
	}
	// preserve other known fields if present, skip HeadingPairs/TitlesOfParts complex
	knownFirst := map[string]bool{"Application": true, "AppVersion": true, "Company": true, "Manager": true}
	for _, k := range []string{"HyperlinkBase", "HyperlinksChanged", "LinksUpToDate", "ScaleCrop", "SharedDoc", "Template", "TotalTime", "Words", "Characters", "Lines", "Paragraphs", "Pages", "Slides", "Notes", "HiddenSlides", "MMClips", "DocSecurity"} {
		if v, ok := propsMap[k]; ok && !knownFirst[k] {
			buf.WriteString("<")
			buf.WriteString(k)
			buf.WriteString(">")
			xml.EscapeText(&buf, []byte(v))
			buf.WriteString("</")
			buf.WriteString(k)
			buf.WriteString(">")
		}
	}
	for k, v := range propsMap {
		if knownFirst[k] {
			continue
		}
		// skip known handled above
		skip := false
		for _, s := range []string{"HyperlinkBase", "HyperlinksChanged", "LinksUpToDate", "ScaleCrop", "SharedDoc", "Template", "TotalTime", "Words", "Characters", "Lines", "Paragraphs", "Pages", "Slides", "Notes", "HiddenSlides", "MMClips", "DocSecurity", "HeadingPairs", "TitlesOfParts"} {
			if k == s {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		buf.WriteString("<")
		buf.WriteString(k)
		buf.WriteString(">")
		xml.EscapeText(&buf, []byte(v))
		buf.WriteString("</")
		buf.WriteString(k)
		buf.WriteString(">")
	}
	buf.WriteString(`</Properties>`)
	return buf.Bytes()
}

func generateContentTypesXMLWithProps(sheetCount int, withProps bool) []byte {
	if !withProps {
		return generateContentTypesXML(sheetCount)
	}
	pr := newPrinter()
	pr.w.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`)
	pr.w.WriteString(`<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">`)
	pr.w.WriteString(`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>`)
	pr.w.WriteString(`<Default Extension="xml" ContentType="application/xml"/>`)
	pr.w.WriteString(`<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>`)
	for i := 0; i < sheetCount; i++ {
		pr.w.WriteString(fmt.Sprintf(`<Override PartName="/xl/worksheets/sheet%d.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>`, i+1))
	}
	pr.w.WriteString(`<Override PartName="/xl/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>`)
	pr.w.WriteString(`<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>`)
	pr.w.WriteString(`<Override PartName="/xl/sharedStrings.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sharedStrings+xml"/>`)
	pr.w.WriteString(`<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>`)
	pr.w.WriteString(`<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>`)
	pr.w.WriteString(`</Types>`)
	return pr.buf.Bytes()
}

func generateRelsXMLWithProps(withProps bool) []byte {
	if !withProps {
		return generateRelsXML()
	}
	return []byte(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>
  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>
</Relationships>`)
}

func ensureContentTypesHasDocProps(data []byte) []byte {
	s := string(data)
	if strings.Contains(s, "/docProps/core.xml") && strings.Contains(s, "/docProps/app.xml") {
		return data
	}
	// insert before </Types>
	if idx := strings.LastIndex(s, "</Types>"); idx >= 0 {
		inject := ""
		if !strings.Contains(s, "/docProps/core.xml") {
			inject += `<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>`
		}
		if !strings.Contains(s, "/docProps/app.xml") {
			inject += `<Override PartName="/docProps/app.xml" ContentType="application/vnd.openxmlformats-officedocument.extended-properties+xml"/>`
		}
		s = s[:idx] + inject + s[idx:]
		return []byte(s)
	}
	return data
}

func relsHasDocProps(data []byte) bool {
	s := string(data)
	return strings.Contains(s, "docProps/core.xml") && strings.Contains(s, "docProps/app.xml")
}

func ensureRelsHasDocProps(data []byte) []byte {
	s := string(data)
	if relsHasDocProps(data) {
		return data
	}
	// insert before </Relationships>
	if idx := strings.LastIndex(s, "</Relationships>"); idx >= 0 {
		inject := ""
		if !strings.Contains(s, "docProps/core.xml") {
			inject += `  <Relationship Id="rId2" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>` + "\n"
		}
		if !strings.Contains(s, "docProps/app.xml") {
			inject += `  <Relationship Id="rId3" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/extended-properties" Target="docProps/app.xml"/>` + "\n"
		}
		s = s[:idx] + inject + s[idx:]
		return []byte(s)
	}
	return data
}

func ensureContentTypesHasTheme(data []byte) []byte {
	if strings.Contains(string(data), "/xl/theme/theme1.xml") {
		return data
	}
	s := string(data)
	if idx := strings.LastIndex(s, "</Types>"); idx >= 0 {
		inject := `<Override PartName="/xl/theme/theme1.xml" ContentType="application/vnd.openxmlformats-officedocument.theme+xml"/>`
		s = s[:idx] + inject + s[idx:]
		return []byte(s)
	}
	return data
}

func ensureWorkbookRelsHasTheme(data []byte) []byte {
	if strings.Contains(string(data), "theme/theme1.xml") {
		return data
	}
	s := string(data)
	if idx := strings.LastIndex(s, "</Relationships>"); idx >= 0 {
		inject := `  <Relationship Id="rIdTh" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/theme" Target="theme/theme1.xml"/>` + "\n"
		s = s[:idx] + inject + s[idx:]
		return []byte(s)
	}
	return data
}
