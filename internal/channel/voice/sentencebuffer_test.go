package voice

import "testing"

func TestSentenceBuffer_SingleSentence(t *testing.T) {
	t.Parallel()
	var got []string
	sb := NewSentenceBuffer(func(s string) { got = append(got, s) })

	sb.Write("Hello world. ")
	sb.Write("More text")
	sb.Flush()

	want := []string{"Hello world.", "More text"}
	if len(got) != len(want) {
		t.Fatalf("got %d sentences, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sentence[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSentenceBuffer_MultiSentence(t *testing.T) {
	t.Parallel()
	var got []string
	sb := NewSentenceBuffer(func(s string) { got = append(got, s) })

	sb.Write("First sentence. Second sentence! Third? ")

	want := []string{"First sentence.", "Second sentence!", "Third?"}
	if len(got) != len(want) {
		t.Fatalf("got %d sentences, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("sentence[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSentenceBuffer_IncrementalTokens(t *testing.T) {
	t.Parallel()
	var got []string
	sb := NewSentenceBuffer(func(s string) { got = append(got, s) })

	tokens := []string{"I", " am", " a", " sentence", ".", " Next"}
	for _, tok := range tokens {
		sb.Write(tok)
	}
	sb.Flush()

	if len(got) != 2 {
		t.Fatalf("got %d sentences, want 2: %v", len(got), got)
	}
	if got[0] != "I am a sentence." {
		t.Errorf("sentence[0] = %q, want %q", got[0], "I am a sentence.")
	}
	if got[1] != "Next" {
		t.Errorf("sentence[1] = %q, want %q", got[1], "Next")
	}
}

func TestSentenceBuffer_EmptyDelta(t *testing.T) {
	t.Parallel()
	var got []string
	sb := NewSentenceBuffer(func(s string) { got = append(got, s) })

	sb.Write("")
	sb.Write("")
	sb.Flush()

	if len(got) != 0 {
		t.Errorf("expected no sentences from empty deltas, got %v", got)
	}
}

func TestSentenceBuffer_FlushMidSentence(t *testing.T) {
	t.Parallel()
	var got []string
	sb := NewSentenceBuffer(func(s string) { got = append(got, s) })

	sb.Write("No punctuation here")
	sb.Flush()

	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0] != "No punctuation here" {
		t.Errorf("sentence = %q, want %q", got[0], "No punctuation here")
	}
}

func TestSentenceBuffer_Reset(t *testing.T) {
	t.Parallel()
	var got []string
	sb := NewSentenceBuffer(func(s string) { got = append(got, s) })

	sb.Write("Discard this")
	sb.Reset()
	sb.Flush()

	if len(got) != 0 {
		t.Errorf("expected no sentences after reset, got %v", got)
	}
}

func TestSentenceBuffer_Buffered(t *testing.T) {
	t.Parallel()
	sb := NewSentenceBuffer(func(string) {})
	sb.Write("partial")
	if got := sb.Buffered(); got != "partial" {
		t.Errorf("Buffered() = %q, want %q", got, "partial")
	}
}

func TestSentenceBuffer_SemicolonBoundary(t *testing.T) {
	t.Parallel()
	var got []string
	sb := NewSentenceBuffer(func(s string) { got = append(got, s) })

	sb.Write("Part one; part two")
	sb.Flush()

	if len(got) != 2 {
		t.Fatalf("got %d, want 2: %v", len(got), got)
	}
	if got[0] != "Part one;" {
		t.Errorf("sentence[0] = %q, want %q", got[0], "Part one;")
	}
}

func TestSentenceBuffer_Unicode(t *testing.T) {
	t.Parallel()
	var got []string
	sb := NewSentenceBuffer(func(s string) { got = append(got, s) })

	sb.Write("Héllo wörld. Ünïcödé")
	sb.Flush()

	if len(got) != 2 {
		t.Fatalf("got %d, want 2: %v", len(got), got)
	}
}
