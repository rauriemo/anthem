package config

type Config struct {
	Tracker   TrackerConfig   `yaml:"tracker"`
	Polling   PollingConfig   `yaml:"polling"`
	Workspace WorkspaceConfig `yaml:"workspace"`
	Hooks     HooksConfig     `yaml:"hooks"`
	Agent     AgentConfig     `yaml:"agent"`
	Rules     []RuleConfig    `yaml:"rules"`
	System    SystemConfig    `yaml:"system"`
	Server    ServerConfig    `yaml:"server"`
}

type TrackerConfig struct {
	Kind   string   `yaml:"kind"`
	Repo   string   `yaml:"repo"`
	Labels LabelSet `yaml:"labels"`
}

type LabelSet struct {
	Active   []string `yaml:"active"`
	Terminal []string `yaml:"terminal"`
}

type PollingConfig struct {
	IntervalMS int `yaml:"interval_ms"`
}

type WorkspaceConfig struct {
	Root string `yaml:"root"`
}

type HooksConfig struct {
	AfterCreate   string `yaml:"after_create"`
	BeforeRun     string `yaml:"before_run"`
	AfterComplete string `yaml:"after_complete"`
}

type AgentConfig struct {
	Command               string            `yaml:"command"`
	MaxTurns              int               `yaml:"max_turns"`
	MaxConcurrent         int               `yaml:"max_concurrent"`
	MaxConcurrentPerLabel map[string]int    `yaml:"max_concurrent_per_label"`
	StallTimeoutMS        int               `yaml:"stall_timeout_ms"`
	MaxRetryBackoffMS     int               `yaml:"max_retry_backoff_ms"`
	AllowedTools          []string          `yaml:"allowed_tools"`
	Model                 string            `yaml:"model"`
	MCPServers            []MCPServerConfig `yaml:"mcp_servers"`
	Skills                []string          `yaml:"skills"`
}

type MCPServerConfig struct {
	Name    string   `yaml:"name"`
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}

type RuleConfig struct {
	Match         RuleMatch `yaml:"match"`
	Action        string    `yaml:"action"`
	ApprovalLabel string    `yaml:"approval_label,omitempty"`
	MaxCost       float64   `yaml:"max_cost,omitempty"`
}

type RuleMatch struct {
	Labels       []string `yaml:"labels,omitempty"`
	TitlePattern string   `yaml:"title_pattern,omitempty"`
}

type SystemConfig struct {
	WorkflowChangesRequireApproval bool     `yaml:"workflow_changes_require_approval"`
	Constraints                    []string `yaml:"constraints"`
}

type ServerConfig struct {
	Port int `yaml:"port"`
}

func DefaultConfig() Config {
	return Config{
		Polling:   PollingConfig{IntervalMS: 10000},
		Workspace: WorkspaceConfig{Root: "./workspaces"},
		Agent: AgentConfig{
			Command:           "claude",
			MaxTurns:          5,
			MaxConcurrent:     3,
			StallTimeoutMS:    300000,
			MaxRetryBackoffMS: 300000,
		},
		System: SystemConfig{
			WorkflowChangesRequireApproval: true,
		},
		Server: ServerConfig{Port: 8080},
	}
}
