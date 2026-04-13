package voice

import "fmt"

// AckTemplate is a pre-composed acknowledgment phrase for agent identity.
type AckTemplate struct {
	AgentID  string
	Template string
}

// DefaultAckTemplates returns agent-identifying phrases used during multi-agent
// mode handoffs. The ack is synthesized immediately while the agent's full
// response is still being generated.
var DefaultAckTemplates = map[string][]string{
	"eiji":     {"Eiji here.", "On it.", "Let me take a look."},
	"miyazaki": {"Miyazaki here.", "I'll handle this.", "Let me work on that."},
	"shigeru":  {"Shigeru here.", "Got it.", "Let me think about this."},
	"tolkien":  {"Tolkien here.", "I'll weigh in.", "Let me share my thoughts."},
	"kaplan":   {"Kaplan here.", "I've got this.", "Let me check on that."},
}

// OrchestratorBridgePhrases are brief phrases the orchestrator says before
// handing off to an agent in multi-agent mode.
var OrchestratorBridgePhrases = []string{
	"Let me hand this to %s.",
	"I'll let %s take this one.",
	"%s is better suited for this.",
	"Passing this to %s.",
}

// GetAckPhrase returns an acknowledgment phrase for the given agent.
// Uses a simple round-robin within the agent's templates.
func GetAckPhrase(agentID string, turnIndex int) string {
	templates, ok := DefaultAckTemplates[agentID]
	if !ok || len(templates) == 0 {
		return fmt.Sprintf("%s here.", agentID)
	}
	return templates[turnIndex%len(templates)]
}

// GetBridgePhrase returns an orchestrator bridge phrase for a handoff.
func GetBridgePhrase(agentName string, turnIndex int) string {
	tmpl := OrchestratorBridgePhrases[turnIndex%len(OrchestratorBridgePhrases)]
	return fmt.Sprintf(tmpl, agentName)
}
