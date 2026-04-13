// Package voice implements a channel.Channel adapter that provides always-on
// voice interaction via LiveKit WebRTC rooms. It connects user audio through a
// streaming STT provider (Deepgram nova-3), routes transcripts to the
// orchestrator, and pipes streamed responses through a TTS provider
// (ElevenLabs eleven_flash_v2_5) back into the LiveKit room.
//
// Audio format: inbound 48kHz PCM16 mono (no resampling to STT), outbound
// 24kHz PCM16 mono (no resampling from TTS). The publish loop slices
// variable-length TTS chunks into 20ms frames.
//
// The package defines pluggable provider interfaces (StreamingSTT, StreamingTTS)
// and a formal FloorController state machine that governs turn-taking and
// barge-in semantics.
package voice
