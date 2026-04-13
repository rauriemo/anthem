package voice

import (
	"strings"
	"unicode"
)

const eagerCharLimit = 50

// SentenceBuffer accumulates StreamDelta text tokens and emits complete
// sentences (or clauses) suitable for TTS synthesis. This enables
// sentence-level streaming: pipe each sentence to TTS as soon as it's
// complete rather than waiting for the full response.
//
// When EagerMode is true the buffer also flushes on clause-level punctuation
// (commas, colons, em-dashes) and on a character-count threshold so that TTS
// receives shorter chunks and can begin speaking sooner.
type SentenceBuffer struct {
	buf       strings.Builder
	callback  func(sentence string)
	EagerMode bool
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
		flush := false

		if isSentenceEnd(r) {
			afterIdx := i + len(string(r))
			if afterIdx < len(text) {
				next := rune(text[afterIdx])
				if unicode.IsSpace(next) || unicode.IsUpper(next) {
					flush = true
				}
			}
		} else if sb.EagerMode && isClausePause(r) {
			afterIdx := i + len(string(r))
			if afterIdx < len(text) && unicode.IsSpace(rune(text[afterIdx])) {
				flush = true
			}
		}

		if flush {
			afterIdx := i + len(string(r))
			sentence := strings.TrimSpace(text[start:afterIdx])
			if sentence != "" {
				sb.callback(sentence)
			}
			start = afterIdx
			lastEmitted = afterIdx
		}
	}

	if sb.EagerMode && lastEmitted < len(text) {
		remainder := text[start:]
		if len(remainder) >= eagerCharLimit {
			cut := findWordBreak(remainder, eagerCharLimit)
			if cut > 0 {
				chunk := strings.TrimSpace(remainder[:cut])
				if chunk != "" {
					sb.callback(chunk)
				}
				start += cut
				lastEmitted = start
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

func isClausePause(r rune) bool {
	return r == ',' || r == ':' || r == '\u2014' // em-dash
}

// findWordBreak returns the index of the last space at or before limit,
// giving a clean word-boundary cut point.
func findWordBreak(s string, limit int) int {
	if limit >= len(s) {
		return len(s)
	}
	for i := limit; i > 0; i-- {
		if s[i] == ' ' {
			return i
		}
	}
	return limit
}
