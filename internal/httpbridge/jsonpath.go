package httpbridge

import (
	"fmt"
	"strconv"
	"strings"
)

// extractByPath navigates a nested JSON structure (map[string]any / []any) using
// a dot-separated path. Array indices use bracket notation: "items[0].name".
// Returns the value at the terminal segment, or an error if any segment is
// missing or has a type mismatch.
func extractByPath(data any, path string) (any, error) {
	segments := splitPath(path)
	current := data
	for i, seg := range segments {
		consumed := strings.Join(segments[:i+1], ".")
		key, idx, hasIdx := parseSegment(seg)

		m, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("expected object at %q, got %T", consumed, current)
		}

		val, exists := m[key]
		if !exists {
			return nil, fmt.Errorf("key %q not found at %q", key, consumed)
		}

		if hasIdx {
			arr, ok := val.([]any)
			if !ok {
				return nil, fmt.Errorf("expected array for %q at %q, got %T", key, consumed, val)
			}
			if idx < 0 || idx >= len(arr) {
				return nil, fmt.Errorf("index %d out of bounds (len %d) at %q", idx, len(arr), consumed)
			}
			current = arr[idx]
		} else {
			current = val
		}
	}
	return current, nil
}

// splitPath splits a dot-separated path, keeping bracket notation attached to
// its key. "a.b[0].c" -> ["a", "b[0]", "c"].
func splitPath(path string) []string {
	var segments []string
	var buf strings.Builder
	for i := 0; i < len(path); i++ {
		if path[i] == '.' && buf.Len() > 0 {
			segments = append(segments, buf.String())
			buf.Reset()
		} else {
			buf.WriteByte(path[i])
		}
	}
	if buf.Len() > 0 {
		segments = append(segments, buf.String())
	}
	return segments
}

// parseSegment extracts a key and optional array index from a path segment.
// "items[0]" -> ("items", 0, true); "name" -> ("name", 0, false).
func parseSegment(seg string) (key string, index int, hasIndex bool) {
	open := strings.IndexByte(seg, '[')
	if open < 0 {
		return seg, 0, false
	}
	close := strings.IndexByte(seg[open:], ']')
	if close < 0 {
		return seg, 0, false
	}
	idxStr := seg[open+1 : open+close]
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		return seg, 0, false
	}
	return seg[:open], idx, true
}
