package config

type Config struct {
	Tracker      TrackerConfig         `yaml:"tracker"`
	Polling      PollingConfig         `yaml:"polling"`
	Workspace    WorkspaceConfig       `yaml:"workspace"`
	Hooks        HooksConfig           `yaml:"hooks"`
	Agent        AgentConfig           `yaml:"agent"`
	Rules        []RuleConfig          `yaml:"rules"`
	System       SystemConfig          `yaml:"system"`
	Orchestrator OrchestratorConfig    `yaml:"orchestrator"`
	Channels     []ChannelTargetConfig `yaml:"channels"`
	Maintenance  MaintenanceConfig     `yaml:"maintenance"`
	Server       ServerConfig          `yaml:"server"`
}

type ChannelTargetConfig struct {
	Kind   string   `yaml:"kind"`
	Target string   `yaml:"target"`
	Events []string `yaml:"events"`
}

type MaintenanceConfig struct {
	ScanIntervalMS        int      `yaml:"scan_interval_ms"`
	AutoApprove           []string `yaml:"auto_approve"`
	FailureThreshold      int      `yaml:"failure_threshold"`
	StaleThresholdHours   int      `yaml:"stale_threshold_hours"`
	CostAnomalyMultiplier float64  `yaml:"cost_anomaly_multiplier"`
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
	Command               string                  `yaml:"command"`
	MaxTurns              int                     `yaml:"max_turns"`
	MaxConcurrent         int                     `yaml:"max_concurrent"`
	MaxConcurrentPerLabel map[string]int          `yaml:"max_concurrent_per_label"`
	StallTimeoutMS        int                     `yaml:"stall_timeout_ms"`
	MaxRetryBackoffMS     int                     `yaml:"max_retry_backoff_ms"`
	AllowedTools          []string                `yaml:"allowed_tools"`
	Model                 string                  `yaml:"model"`
	MCPServers            []MCPServerConfig       `yaml:"mcp_servers"`
	Skills                []string                `yaml:"skills"`
	PermissionMode        string                  `yaml:"permission_mode"`
	SkipPermissions       bool                    `yaml:"skip_permissions"`
	DeniedTools           []string                `yaml:"denied_tools"`
	AdditionalDirs        []string                `yaml:"additional_dirs"`
	ReviewEnabled         bool                    `yaml:"review_enabled"`
	ReviewMaxTurns        int                     `yaml:"review_max_turns"`
	ReviewMaxRetries      int                     `yaml:"review_max_retries"`
	ReviewPrompt          string                  `yaml:"review_prompt"`
	Profiles              map[string]AgentProfile `yaml:"profiles,omitempty"`
}

type MCPServerConfig struct {
	Name    string   `yaml:"name"`
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}

type AgentProfile struct {
	PromptPrefix  string   `yaml:"prompt_prefix"`
	PromptSuffix  string   `yaml:"prompt_suffix"`
	AllowedTools  []string `yaml:"allowed_tools,omitempty"`
	DeniedTools   []string `yaml:"denied_tools,omitempty"`
	Model         string   `yaml:"model,omitempty"`
	MaxTurns      int      `yaml:"max_turns,omitempty"`
	ReviewEnabled *bool    `yaml:"review_enabled,omitempty"`
}

type RuleConfig struct {
	Match         RuleMatch `yaml:"match"`
	Action        string    `yaml:"action"`
	ApprovalLabel string    `yaml:"approval_label,omitempty"`
	MaxCost       float64   `yaml:"max_cost,omitempty"`
	AutoAssignee  string    `yaml:"auto_assignee,omitempty"`
}

type RuleMatch struct {
	Labels       []string `yaml:"labels,omitempty"`
	TitlePattern string   `yaml:"title_pattern,omitempty"`
}

type SystemConfig struct {
	Constraints                    []string `yaml:"constraints"`
	RequireApprovalForRiskyActions bool     `yaml:"require_approval_for_risky_actions"`
}

type OrchestratorConfig struct {
	Enabled          bool `yaml:"enabled"`
	MaxContextTokens int  `yaml:"max_context_tokens"`
	StallTimeoutMS   int  `yaml:"stall_timeout_ms"`
	MaxTurns         int  `yaml:"max_turns"`
	PlanMaxTurns     int  `yaml:"plan_max_turns"`
	ExplorerMaxTurns int  `yaml:"explorer_max_turns"`
	MaxExplorers     int  `yaml:"max_explorers"`
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
			ReviewMaxTurns:    3,
			Profiles: map[string]AgentProfile{
				"coder":     {PromptPrefix: "You are a coding agent. Write clean, tested code."},
				"architect": {PromptPrefix: "You are an architect agent. Analyze and design, do not write code.", DeniedTools: []string{"Write", "Edit", "Bash"}},
				"tester":    {PromptPrefix: "You are a testing agent. Focus on writing comprehensive tests."},
				"debugger":  {PromptPrefix: "You are a debugger agent. A previous attempt failed. Analyze the feedback carefully and fix the issues."},
				"explorer":  {PromptPrefix: "You are a codebase research agent. Investigate thoroughly, cite specific files and line numbers. Do not modify any files.", DeniedTools: []string{"Write", "Edit", "MultiEdit"}},
			},
		},
		System: SystemConfig{},
		Orchestrator: OrchestratorConfig{
			Enabled:          true,
			MaxContextTokens: 80000,
			StallTimeoutMS:   60000,
			MaxTurns:         10,
			PlanMaxTurns:     25,
			ExplorerMaxTurns: 10,
			MaxExplorers:     5,
		},
		Maintenance: MaintenanceConfig{
			ScanIntervalMS:        600000,
			FailureThreshold:      3,
			StaleThresholdHours:   24,
			CostAnomalyMultiplier: 2.0,
		},
		Server: ServerConfig{Port: 8080},
	}
}
