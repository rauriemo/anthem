package orchestrator

import "strings"

// normalizeDataImageURLsInHTML removes whitespace and line breaks from the base64
// payload of every data:image/...;base64,... URL in HTML. Browsers reject data URLs
// when the model inserts newlines or spaces inside the attribute value.
func normalizeDataImageURLsInHTML(html string) string {
	if html == "" || !strings.Contains(strings.ToLower(html), "data:image/") {
		return html
	}
	var b strings.Builder
	b.Grow(len(html))
	i := 0
	for i < len(html) {
		rel := strings.Index(strings.ToLower(html[i:]), "data:image/")
		if rel < 0 {
			b.WriteString(html[i:])
			break
		}
		start := i + rel
		b.WriteString(html[i:start])
		rest := strings.ToLower(html[start:])
		rel2 := strings.Index(rest, ";base64,")
		if rel2 < 0 {
			b.WriteString(html[start:])
			break
		}
		payloadStart := start + rel2 + len(";base64,")
		b.WriteString(html[start:payloadStart])
		p := payloadStart
		for p < len(html) {
			c := html[p]
			if c == ' ' || c == '\n' || c == '\r' || c == '\t' {
				p++
				continue
			}
			if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
				c == '+' || c == '/' || c == '=' {
				b.WriteByte(c)
				p++
				continue
			}
			break
		}
		i = p
	}
	return b.String()
}

// normalizeDisplayComponentsDataImageURLs applies normalizeDataImageURLsInHTML to
// html and markdown display payloads before they are sent to Prism.
func normalizeDisplayComponentsDataImageURLs(displays []map[string]any) {
	for _, comp := range displays {
		kind, _ := comp["kind"].(string)
		if kind != "html" && kind != "markdown" {
			continue
		}
		content, ok := comp["content"].(string)
		if !ok || content == "" {
			continue
		}
		comp["content"] = normalizeDataImageURLsInHTML(content)
	}
}
