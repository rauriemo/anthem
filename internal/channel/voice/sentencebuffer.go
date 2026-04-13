package voice

import (
	"strings"
	"unicode"
)

// SentenceBuffer accumulates StreamDelta text tokens and emits complete
// sentences (or clauses) suitable for TTS synthesis. This enables
// sentence-level streaming: pipe each sentence to TTS as soon as it's
// complete rather than waiting for the full response.
type SentenceBuffer struct {
	buf      strings.Builder
	callback func(sentence string)
}

// NewSentenceBuffer creates a buffer that calls cb with each complete sentence.
func NewSentenceBuffer(cb func(sentence string)) *SentenceBuffer {
	return &SentenceBuffer{callback: cb}
}

// Write appends a text delta and flushes any complete sentences.
func (sb *SentenceBuffer) Write(delta string) {
	sb.buf.WriteString(delta)
	sb.flushSentences()
}

// Flush emits any remaining buffered text regardless of sentence boundaries.
// Call this on StreamDone.
func (sb *SentenceBuffer) Flush() {
	remaining := strings.TrimSpace(sb.buf.String())
	if remaining != "" {
		sb.callback(remaining)
	}
	sb.buf.Reset()
}

// Reset discards buffered content without emitting.
func (sb *SentenceBuffer) Reset() {
	sb.buf.Reset()
}

// Buffered returns the current unemitted content.
func (sb *SentenceBuffer) Buffered() string {
	return sb.buf.String()
}

func (sb *SentenceBuffer) flushSentences() {
	text := sb.buf.String()

	start := 0
	lastEmitted := 0
	for i, r := range text {
		if isSentenceEnd(r) {
			afterIdx := i + len(string(r))
			if afterIdx < len(text) {
				next := rune(text[afterIdx])
				if unicode.IsSpace(next) || unicode.IsUpper(next) {
					sentence := strings.TrimSpace(text[start:afterIdx])
					if sentence != "" {
						sb.callback(sentence)
					}
					start = afterIdx
					lastEmitted = afterIdx
				}
			}
		}
	}

	if lastEmitted > 0 {
		sb.buf.Reset()
		sb.buf.WriteString(text[lastEmitted:])
	}
}

func isSentenceEnd(r rune) bool {
	return r == '.' || r == '!' || r == '?' || r == ';'
}
