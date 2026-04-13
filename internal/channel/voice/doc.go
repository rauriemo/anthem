// Package voice implements a channel.Channel adapter that provides always-on
// voice interaction via LiveKit WebRTC rooms. It connects user audio through a
// streaming STT provider (Deepgram nova-3), routes transcripts to the
// orchestrator, and pipes streamed responses through a TTS provider
// (ElevenLabs eleven_flash_v2_5) back into the LiveKit room.
//
// Audio uses Opus end-to-end passthrough to avoid CGo codec dependencies:
// inbound Opus payloads from the browser's WebRTC track go directly to
// Deepgram (encoding=opus), and outbound Opus frames from ElevenLabs
// (opus_48000_64) go directly to LiveKit's WriteSample for RTP packetization.
//
// The package defines pluggable provider interfaces (StreamingSTT, StreamingTTS)
// and a formal FloorController state machine that governs turn-taking and
// barge-in semantics.
package voice
