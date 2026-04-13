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

func TestSentenceBuffer_EagerComma(t *testing.T) {
	t.Parallel()
	var got []string
	sb := NewSentenceBuffer(func(s string) { got = append(got, s) })
	sb.EagerMode = true

	sb.Write("Hello, world")
	sb.Flush()

	if len(got) != 2 {
		t.Fatalf("got %d sentences, want 2: %v", len(got), got)
	}
	if got[0] != "Hello," {
		t.Errorf("sentence[0] = %q, want %q", got[0], "Hello,")
	}
	if got[1] != "world" {
		t.Errorf("sentence[1] = %q, want %q", got[1], "world")
	}
}

func TestSentenceBuffer_EagerColon(t *testing.T) {
	t.Parallel()
	var got []string
	sb := NewSentenceBuffer(func(s string) { got = append(got, s) })
	sb.EagerMode = true

	sb.Write("Step one: do this thing")
	sb.Flush()

	if len(got) != 2 {
		t.Fatalf("got %d sentences, want 2: %v", len(got), got)
	}
	if got[0] != "Step one:" {
		t.Errorf("sentence[0] = %q, want %q", got[0], "Step one:")
	}
	if got[1] != "do this thing" {
		t.Errorf("sentence[1] = %q, want %q", got[1], "do this thing")
	}
}

func TestSentenceBuffer_EagerCharLimit(t *testing.T) {
	t.Parallel()
	var got []string
	sb := NewSentenceBuffer(func(s string) { got = append(got, s) })
	sb.EagerMode = true

	long := "This is a long piece of text without any punctuation that keeps going and going"
	sb.Write(long)
	sb.Flush()

	if len(got) < 2 {
		t.Fatalf("expected at least 2 chunks from %d-char input, got %d: %v", len(long), len(got), got)
	}
	if len(got[0]) > eagerCharLimit+10 {
		t.Errorf("first chunk too long: %d chars (%q)", len(got[0]), got[0])
	}
}

func TestSentenceBuffer_EagerDisabled(t *testing.T) {
	t.Parallel()
	var got []string
	sb := NewSentenceBuffer(func(s string) { got = append(got, s) })

	sb.Write("Hello, world: step one")
	if len(got) != 0 {
		t.Errorf("expected no flush without EagerMode for clause punctuation, got %v", got)
	}
	sb.Flush()
	if len(got) != 1 {
		t.Errorf("expected 1 sentence from Flush, got %d: %v", len(got), got)
	}
}

func TestSentenceBuffer_EagerStillFlushesSentences(t *testing.T) {
	t.Parallel()
	var got []string
	sb := NewSentenceBuffer(func(s string) { got = append(got, s) })
	sb.EagerMode = true

	sb.Write("First sentence. Second sentence! Done")
	sb.Flush()

	if len(got) != 3 {
		t.Fatalf("got %d, want 3: %v", len(got), got)
	}
	if got[0] != "First sentence." {
		t.Errorf("sentence[0] = %q, want %q", got[0], "First sentence.")
	}
	if got[1] != "Second sentence!" {
		t.Errorf("sentence[1] = %q, want %q", got[1], "Second sentence!")
	}
	if got[2] != "Done" {
		t.Errorf("sentence[2] = %q, want %q", got[2], "Done")
	}
}
