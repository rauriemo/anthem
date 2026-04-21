package httpbridge

import (
	"strings"
	"testing"

	"github.com/rauriemo/anthem/internal/guests"
)

// TestResolveString_FallbackDefault covers the ${input.X | 'default'} syntax
// introduced in the Scope 2 matting-pipeline plan so post_process config can
// parameterize fps / columns without requiring every caller to set inputs.
func TestResolveString_FallbackDefault(t *testing.T) {
	cases := []struct {
		name string
		s    string
		vars map[string]string
		want string
	}{
		{
			name: "default used when var missing",
			s:    "${input.extract_fps | '24'}",
			vars: map[string]string{},
			want: "24",
		},
		{
			name: "caller override wins over default",
			s:    "${input.extract_fps | '24'}",
			vars: map[string]string{"extract_fps": "12"},
			want: "12",
		},
		{
			name: "caller override of empty-string default still wins",
			s:    "${input.columns | ''}",
			vars: map[string]string{"columns": "8"},
			want: "8",
		},
		{
			name: "empty-string default when var missing",
			s:    "${input.columns | ''}",
			vars: map[string]string{},
			want: "",
		},
		{
			name: "default next to literal text",
			s:    "fps=${input.extract_fps | '24'}.",
			vars: map[string]string{},
			want: "fps=24.",
		},
		{
			name: "two defaults, one overridden",
			s:    "${input.fps | '24'}@${input.cols | '8'}",
			vars: map[string]string{"fps": "12"},
			want: "12@8",
		},
		{
			name: "spaces around pipe tolerated",
			s:    "${input.fps|'24'}",
			vars: map[string]string{},
			want: "24",
		},
		{
			name: "backwards compatible: plain var still resolves",
			s:    "${input.prompt}",
			vars: map[string]string{"prompt": "hello"},
			want: "hello",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveString(tc.s, tc.vars)
			if err != nil {
				t.Fatalf("resolveString: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// Missing var without default must still error; regression guard for the
// existing request-template path that treats every reference as required.
func TestResolveString_MissingWithoutDefaultStillErrors(t *testing.T) {
	_, err := resolveString("${input.prompt}", map[string]string{})
	if err == nil {
		t.Fatal("expected error for missing var without default, got nil")
	}
	if !strings.Contains(err.Error(), "prompt") {
		t.Errorf("error message should mention the missing var: %q", err.Error())
	}
}

// TestExtractInputVars_FallbackDefaultsAreOptional verifies that vars referenced
// only via fallback syntax are not surfaced as required in the tool schema.
func TestExtractInputVars_FallbackDefaultsAreOptional(t *testing.T) {
	tmpl := map[string]any{
		"required_only": "${input.prompt}",
		"optional_only": "${input.extract_fps | '24'}",
		"mixed":         "fps=${input.cols | '8'}",
	}
	got := ExtractInputVars(tmpl)
	if len(got) != 1 || got[0] != "prompt" {
		t.Errorf("ExtractInputVars = %v, want [prompt] (fallback vars must be optional)", got)
	}
}

// TestExtractAllInputVars covers the split required/optional surfaces that
// feed the MCP tool schema. Without this the MCP client never sees
// fallback-default inputs (aspect_ratio, spritesheet_columns, ...) and
// can't override the YAML defaults.
func TestExtractAllInputVars(t *testing.T) {
	tests := []struct {
		name         string
		tmpl         map[string]any
		wantRequired []string
		wantOptional []string
	}{
		{
			name: "required and optional split",
			tmpl: map[string]any{
				"prompt":       "${input.prompt}",
				"aspect_ratio": "${input.aspect_ratio | '1:1'}",
				"style":        "${input.style | 'painterly'}",
			},
			wantRequired: []string{"prompt"},
			wantOptional: []string{"aspect_ratio", "style"},
		},
		{
			name: "required appearance wins when var referenced both ways",
			tmpl: map[string]any{
				"with_default":    "${input.prompt | 'hi'}",
				"without_default": "${input.prompt}",
			},
			wantRequired: []string{"prompt"},
			wantOptional: []string{},
		},
		{
			name: "nested template with fallbacks",
			tmpl: map[string]any{
				"gen_config": map[string]any{
					"image_config": map[string]any{
						"aspect_ratio": "${input.aspect_ratio | '1:1'}",
					},
				},
				"contents": []any{
					map[string]any{"text": "${input.prompt}"},
				},
			},
			wantRequired: []string{"prompt"},
			wantOptional: []string{"aspect_ratio"},
		},
		{
			name:         "no vars",
			tmpl:         map[string]any{"static": "value"},
			wantRequired: []string{},
			wantOptional: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotRequired, gotOptional := ExtractAllInputVars(tt.tmpl)
			if !slicesEqual(gotRequired, tt.wantRequired) {
				t.Errorf("required = %v, want %v", gotRequired, tt.wantRequired)
			}
			if !slicesEqual(gotOptional, tt.wantOptional) {
				t.Errorf("optional = %v, want %v", gotOptional, tt.wantOptional)
			}
		})
	}
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestResolvePostProcess covers the new helper that applies input-var
// resolution to post_process config values, the main motivation for Section
// 1.5 of the plan (walt.md wires fps and spritesheet_columns via this path).
func TestResolvePostProcess(t *testing.T) {
	ops := []guests.PostProcessOp{
		{
			Op: "extract_video_frames",
			Config: map[string]string{
				"fps": "${input.extract_fps | '24'}",
			},
		},
		{
			Op: "video_matte",
			Config: map[string]string{
				"keyframe_every": "8",
				"refine":         "true",
			},
		},
		{
			Op: "stitch_spritesheet",
			Config: map[string]string{
				"columns":    "${input.spritesheet_columns | '8'}",
				"keep_video": "false",
			},
		},
	}

	t.Run("defaults apply when no inputs passed", func(t *testing.T) {
		resolved, err := ResolvePostProcess(ops, map[string]string{})
		if err != nil {
			t.Fatalf("ResolvePostProcess: %v", err)
		}
		if resolved[0].Config["fps"] != "24" {
			t.Errorf("fps = %q, want 24", resolved[0].Config["fps"])
		}
		if resolved[2].Config["columns"] != "8" {
			t.Errorf("columns = %q, want 8", resolved[2].Config["columns"])
		}
		if resolved[1].Config["keyframe_every"] != "8" {
			t.Errorf("keyframe_every = %q, want 8 (literal)", resolved[1].Config["keyframe_every"])
		}
	})

	t.Run("caller override flips both knobs for the 12 fps lane", func(t *testing.T) {
		resolved, err := ResolvePostProcess(ops, map[string]string{
			"extract_fps":         "12",
			"spritesheet_columns": "4",
		})
		if err != nil {
			t.Fatalf("ResolvePostProcess: %v", err)
		}
		if resolved[0].Config["fps"] != "12" {
			t.Errorf("fps = %q, want 12", resolved[0].Config["fps"])
		}
		if resolved[2].Config["columns"] != "4" {
			t.Errorf("columns = %q, want 4", resolved[2].Config["columns"])
		}
	})

	t.Run("original ops not mutated", func(t *testing.T) {
		_, err := ResolvePostProcess(ops, map[string]string{
			"extract_fps":         "12",
			"spritesheet_columns": "4",
		})
		if err != nil {
			t.Fatalf("ResolvePostProcess: %v", err)
		}
		if ops[0].Config["fps"] != "${input.extract_fps | '24'}" {
			t.Errorf("original ops[0] mutated: fps = %q", ops[0].Config["fps"])
		}
	})

	t.Run("missing var without default bubbles up", func(t *testing.T) {
		bad := []guests.PostProcessOp{
			{
				Op:     "extract_video_frames",
				Config: map[string]string{"fps": "${input.no_such_var}"},
			},
		}
		_, err := ResolvePostProcess(bad, map[string]string{})
		if err == nil {
			t.Fatal("expected error for missing var, got nil")
		}
		if !strings.Contains(err.Error(), "no_such_var") {
			t.Errorf("error should mention missing var: %v", err)
		}
	})

	t.Run("empty ops pass through", func(t *testing.T) {
		resolved, err := ResolvePostProcess(nil, map[string]string{})
		if err != nil || resolved != nil {
			t.Errorf("nil ops: got (%v, %v), want (nil, nil)", resolved, err)
		}
		resolved, err = ResolvePostProcess([]guests.PostProcessOp{}, map[string]string{})
		if err != nil {
			t.Errorf("empty ops: got err %v", err)
		}
		if len(resolved) != 0 {
			t.Errorf("empty ops: got %v, want zero-length", resolved)
		}
	})
}
