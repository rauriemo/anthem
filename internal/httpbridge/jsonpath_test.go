package httpbridge

import (
	"testing"
)

func TestExtractByPath(t *testing.T) {
	data := map[string]any{
		"name": "operations/op-123",
		"done": true,
		"response": map[string]any{
			"generateVideoResponse": map[string]any{
				"generatedSamples": []any{
					map[string]any{
						"video": map[string]any{
							"uri": "https://example.com/video.mp4",
						},
					},
					map[string]any{
						"video": map[string]any{
							"uri": "https://example.com/video2.mp4",
						},
					},
				},
			},
		},
	}

	tests := []struct {
		name    string
		path    string
		want    any
		wantErr bool
		errMsg  string
	}{
		{
			name: "top-level key",
			path: "name",
			want: "operations/op-123",
		},
		{
			name: "top-level bool",
			path: "done",
			want: true,
		},
		{
			name: "nested map",
			path: "response.generateVideoResponse",
			want: nil, // just check no error; value is a map
		},
		{
			name: "array index zero",
			path: "response.generateVideoResponse.generatedSamples[0].video.uri",
			want: "https://example.com/video.mp4",
		},
		{
			name: "array index one",
			path: "response.generateVideoResponse.generatedSamples[1].video.uri",
			want: "https://example.com/video2.mp4",
		},
		{
			name:    "missing key",
			path:    "nonexistent",
			wantErr: true,
			errMsg:  "not found",
		},
		{
			name:    "missing nested key",
			path:    "response.missing.field",
			wantErr: true,
			errMsg:  "not found",
		},
		{
			name:    "index out of bounds",
			path:    "response.generateVideoResponse.generatedSamples[5].video.uri",
			wantErr: true,
			errMsg:  "out of bounds",
		},
		{
			name:    "type mismatch - expected object got string",
			path:    "name.nested",
			wantErr: true,
			errMsg:  "expected object",
		},
		{
			name:    "array index on non-array",
			path:    "response[0]",
			wantErr: true,
			errMsg:  "expected array",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractByPath(data, tt.path)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.errMsg != "" && !containsStr(err.Error(), tt.errMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.errMsg)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.want != nil && got != tt.want {
				t.Errorf("got %v (%T), want %v (%T)", got, got, tt.want, tt.want)
			}
		})
	}
}

func TestSplitPath(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"name", []string{"name"}},
		{"a.b.c", []string{"a", "b", "c"}},
		{"items[0].name", []string{"items[0]", "name"}},
		{"a.b[2].c.d[0]", []string{"a", "b[2]", "c", "d[0]"}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := splitPath(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("segment[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestParseSegment(t *testing.T) {
	tests := []struct {
		input    string
		wantKey  string
		wantIdx  int
		wantHas  bool
	}{
		{"name", "name", 0, false},
		{"items[0]", "items", 0, true},
		{"arr[42]", "arr", 42, true},
		{"broken[", "broken[", 0, false},
		{"broken]", "broken]", 0, false},
		{"bad[abc]", "bad[abc]", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			key, idx, has := parseSegment(tt.input)
			if key != tt.wantKey {
				t.Errorf("key = %q, want %q", key, tt.wantKey)
			}
			if idx != tt.wantIdx {
				t.Errorf("idx = %d, want %d", idx, tt.wantIdx)
			}
			if has != tt.wantHas {
				t.Errorf("hasIndex = %v, want %v", has, tt.wantHas)
			}
		})
	}
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
