package orchestrator

import "testing"

func TestNormalizeDataImageURLsInHTML_stripsWhitespaceInBase64(t *testing.T) {
	t.Parallel()
	const prefix = `data:image/png;base64,`
	payload := "iVBORw0KGgo\nANSUhE\ngA" + "\t" + "AAA="
	want := prefix + "iVBORw0KGgoANSUhEgAAAA="
	html := `<img src="` + prefix + payload + `" alt="x">`
	got := normalizeDataImageURLsInHTML(html)
	if got != `<img src="`+want+`" alt="x">` {
		t.Fatalf("got %q want %q", got, `<img src="`+want+`" alt="x">`)
	}
}

func TestNormalizeDataImageURLsInHTML_multipleImages(t *testing.T) {
	t.Parallel()
	a := "data:image/png;base64,AA\nBB"
	b := "data:image/jpeg;base64,CC\r\nDD"
	html := `<img src="` + a + `"><img src='` + b + `'>`
	got := normalizeDataImageURLsInHTML(html)
	want := `<img src="data:image/png;base64,AABB"><img src='data:image/jpeg;base64,CCDD'>`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestNormalizeDataImageURLsInHTML_noDataURI(t *testing.T) {
	t.Parallel()
	html := `<img src="https://example.com/x.png">`
	if got := normalizeDataImageURLsInHTML(html); got != html {
		t.Fatalf("got %q", got)
	}
}

func TestNormalizeDisplayComponentsDataImageURLs(t *testing.T) {
	t.Parallel()
	displays := []map[string]any{
		{"kind": "html", "content": "<img src=\"data:image/png;base64,AB\nCD\">"},
		{"kind": "data", "content": "x"},
		{"kind": "markdown", "content": "![](data:image/png;base64,XY\nZ)"},
	}
	normalizeDisplayComponentsDataImageURLs(displays)
	if displays[0]["content"] != `<img src="data:image/png;base64,ABCD">` {
		t.Fatalf("html: %v", displays[0]["content"])
	}
	if displays[1]["content"] != "x" {
		t.Fatal("data kind should be unchanged")
	}
	if displays[2]["content"] != "![](data:image/png;base64,XYZ)" {
		t.Fatalf("markdown: %v", displays[2]["content"])
	}
}
