package orchestrator

import (
	"reflect"
	"testing"
)

func TestParseContextReport_FullReport(t *testing.T) {
	t.Parallel()

	jsonBlock := `{"context_report":{"action":"commit","summary":"Added handler","artifact":{"id":"a-1","type":"text/plain","path":"notes/x.txt","description":"notes","tags":["a","b"],"metadata":{"k":"v"},"depends_on":["p"],"status":"draft"},"progress":"50%","blocked_on":"","produces":["foo.go","bar.go"]}}`
	want := &ContextReport{
		Action:  "commit",
		Summary: "Added handler",
		Artifact: &ReportArtifact{
			ID:          "a-1",
			Type:        "text/plain",
			Path:        "notes/x.txt",
			Description: "notes",
			Tags:        []string{"a", "b"},
			Metadata:    map[string]string{"k": "v"},
			DependsOn:   []string{"p"},
			Status:      "draft",
		},
		Progress:  "50%",
		BlockedOn: "",
		Produces:  []string{"foo.go", "bar.go"},
	}

	tests := []struct {
		name string
		raw  string
		want *ContextReport
	}{
		{"single_line", jsonBlock, want},
		{"after_prose", "Done.\n" + jsonBlock, want},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseContextReport(tt.raw)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if !reflect.DeepEqual(tt.want, got) {
				t.Fatalf("mismatch\nwant: %#v\ngot:  %#v", tt.want, got)
			}
		})
	}
}

func TestParseContextReport_MinimalReport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want *ContextReport
	}{
		{
			name: "action_and_summary",
			raw:  `{"context_report":{"action":"noop","summary":"Nothing to do"}}`,
			want: &ContextReport{
				Action:  "noop",
				Summary: "Nothing to do",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseContextReport(tt.raw)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if !reflect.DeepEqual(tt.want, got) {
				t.Fatalf("mismatch\nwant: %#v\ngot:  %#v", tt.want, got)
			}
		})
	}
}

func TestParseContextReport_NoReport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"plain_text", "Hello, this is a normal assistant reply.\nNo JSON here."},
		{"json_without_key", `{"tool":"search","args":{"q":"x"}}`},
		{"nested_other_only", `prefix {"outer":{"inner":1}} suffix`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseContextReport(tt.in)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if got != nil {
				t.Fatalf("want nil, got %#v", got)
			}
		})
	}
}

func TestParseContextReport_MalformedJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
	}{
		{"invalid_token", `{"context_report":{"action":"x","summary":"y",broken}}`},
		{"invalid_primitive", `{"context_report":}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseContextReport(tt.raw)
			if err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestParseContextReport_NestedJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want *ContextReport
	}{
		{
			name: "noise_object_then_report",
			raw:  `tool result: {"foo":1,"bar":{"z":2}} then {"context_report":{"action":"fix","summary":"patched"}}`,
			want: &ContextReport{
				Action:  "fix",
				Summary: "patched",
			},
		},
		{
			name: "fenced_decoy_skipped",
			raw: "intro\n```json\n{\"context_report\":{\"action\":\"bad\",\"summary\":\"in_fence\"}}\n```\n{\"context_report\":{\"action\":\"good\",\"summary\":\"out\"}}",
			want: &ContextReport{
				Action:  "good",
				Summary: "out",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseContextReport(tt.raw)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if !reflect.DeepEqual(tt.want, got) {
				t.Fatalf("mismatch\nwant: %#v\ngot:  %#v", tt.want, got)
			}
		})
	}
}

func TestParseContextReport_MixedWithConversation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want *ContextReport
	}{
		{
			name: "paragraphs_then_json",
			raw: `I reviewed the module and applied the requested edits.

The main risk was duplicate imports; those are resolved.

{"context_report":{"action":"edit","summary":"Cleaned imports in pkg/api"}}`,
			want: &ContextReport{
				Action:  "edit",
				Summary: "Cleaned imports in pkg/api",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseContextReport(tt.raw)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if !reflect.DeepEqual(tt.want, got) {
				t.Fatalf("mismatch\nwant: %#v\ngot:  %#v", tt.want, got)
			}
		})
	}
}

func TestParseContextReport_MultipleReports(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want *ContextReport
	}{
		{
			name: "last_wins",
			raw: `{"context_report":{"action":"first","summary":"one"}}
{"context_report":{"action":"second","summary":"two"}}`,
			want: &ContextReport{
				Action:  "second",
				Summary: "two",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseContextReport(tt.raw)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if !reflect.DeepEqual(tt.want, got) {
				t.Fatalf("mismatch\nwant: %#v\ngot:  %#v", tt.want, got)
			}
		})
	}
}

func TestParseContextReport_EmptyArtifact(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want *ContextReport
	}{
		{
			name: "no_artifact_field",
			raw:  `{"context_report":{"action":"scan","summary":"no files","progress":"done"}}`,
			want: &ContextReport{
				Action:   "scan",
				Summary:  "no files",
				Progress: "done",
				Artifact: nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseContextReport(tt.raw)
			if err != nil {
				t.Fatalf("err: %v", err)
			}
			if !reflect.DeepEqual(tt.want, got) {
				t.Fatalf("mismatch\nwant: %#v\ngot:  %#v", tt.want, got)
			}
			if got.Artifact != nil {
				t.Fatalf("Artifact: want nil, got %#v", got.Artifact)
			}
		})
	}
}
