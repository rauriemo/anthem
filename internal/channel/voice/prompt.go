package voice

import "strings"

// VoicePromptModifier generates system-prompt additions for the orchestrator
// when operating in voice mode. These instructions optimize the orchestrator's
// output for spoken delivery: concise, front-loaded, conversational.
const voiceSystemSuffix = `
You are currently in voice mode. The user is speaking to you via an always-on voice room.
Your responses will be converted to speech via TTS, so:

1. Keep responses concise -- aim for 2-3 sentences per turn unless the user asks for detail.
2. Front-load the key information -- put the most important point in your first sentence.
3. Use conversational phrasing -- speak as a colleague, not a document.
4. Avoid markdown, bullet lists, code blocks, or visual formatting -- these don't translate to speech.
5. When narrating management actions (dispatching agents, inviting specialists), be brief and natural:
   - Good: "I'm pulling in Eiji for the scene layout."
   - Bad: "I will now dispatch the request to the agent named Eiji who specializes in scene composition."
6. If you need to share something complex (code, data), say so and note it will appear in the text transcript.
7. When interrupted (barge-in), pick up naturally -- don't repeat everything from the start.
`

// ApplyVoiceModifier appends voice-mode instructions to a system prompt.
func ApplyVoiceModifier(systemPrompt string) string {
	return strings.TrimRight(systemPrompt, "\n") + "\n" + voiceSystemSuffix
}

// RecoveryModifier prepends interruption context to a user message
// when there was a barge-in on the previous turn.
func RecoveryModifier(userMessage string, recovery *InterruptionRecovery) string {
	prefix := recovery.RecoveryPrefix()
	if prefix == "" {
		return userMessage
	}
	return prefix + "\n\n" + userMessage
}
