package voice

import (
	"context"
	"testing"
)

func TestNullFastResponder(t *testing.T) {
	t.Parallel()
	fr := &NullFastResponder{}

	if err := fr.Start(context.Background()); err != nil {
		t.Fatal(err)
	}

	resp, err := fr.TryRespond(context.Background(), "anything")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Handled {
		t.Error("NullFastResponder should never handle")
	}

	if err := fr.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRuleBasedFastResponder_MatchesSimple(t *testing.T) {
	t.Parallel()
	fr := NewRuleBasedFastResponder()
	_ = fr.Start(context.Background())

	tests := []struct {
		input   string
		handled bool
		text    string
	}{
		{"yes", true, "Got it."},
		{"Yeah", true, "Got it."},
		{"YES", true, "Got it."},
		{"no", true, "Understood."},
		{"thanks", true, "You're welcome."},
		{"hello", true, "Hey there."},
		{"go ahead", true, "On it."},
		{"tell me about the architecture", false, ""},
		{"what do you think about this design?", false, ""},
		{"", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			resp, err := fr.TryRespond(context.Background(), tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if resp.Handled != tt.handled {
				t.Errorf("handled = %v, want %v", resp.Handled, tt.handled)
			}
			if tt.handled && resp.Text != tt.text {
				t.Errorf("text = %q, want %q", resp.Text, tt.text)
			}
		})
	}
}

func TestRuleBasedFastResponder_NormalizePunctuation(t *testing.T) {
	t.Parallel()
	fr := NewRuleBasedFastResponder()

	resp, _ := fr.TryRespond(context.Background(), "Yes!")
	if resp.Handled {
		t.Log("Punctuation stripped, should match 'yes'")
	}
}

func TestNormalizeForMatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"Hello", "hello"},
		{"  yes  ", "yes"},
		{"GO AHEAD", "go ahead"},
		{"yes!", "yes"},
		{"Hello, World!", "hello world"},
		{"", ""},
		{"   ", ""},
	}
	for _, tt := range tests {
		got := normalizeForMatch(tt.input)
		if got != tt.want {
			t.Errorf("normalizeForMatch(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
