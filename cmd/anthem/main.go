package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rauriemo/anthem/internal/agent/claude"
	"github.com/rauriemo/anthem/internal/audit"
	"github.com/rauriemo/anthem/internal/channel"
	dispatchch "github.com/rauriemo/anthem/internal/channel/dispatch"
	prismch "github.com/rauriemo/anthem/internal/channel/prism"
	slackch "github.com/rauriemo/anthem/internal/channel/slack"
	voicech "github.com/rauriemo/anthem/internal/channel/voice"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/constraints"
	"github.com/rauriemo/anthem/internal/discovery"
	"github.com/rauriemo/anthem/internal/guests"
	"github.com/rauriemo/anthem/internal/httpbridge"
	"github.com/rauriemo/anthem/internal/logging"
	"github.com/rauriemo/anthem/internal/maintenance"
	"github.com/rauriemo/anthem/internal/orchestrator"
	"github.com/rauriemo/anthem/internal/tracker"
	ghtracker "github.com/rauriemo/anthem/internal/tracker/github"
	localtracker "github.com/rauriemo/anthem/internal/tracker/local"
	"github.com/rauriemo/anthem/internal/voice"
	"github.com/rauriemo/anthem/internal/workspace"
)

var version = "dev"

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "anthem",
		Short: "Claude agent orchestrator",
		Long:  "Anthem is an open-source agent orchestrator for Claude Code.",
	}

	root.AddCommand(
		initCmd(),
		runCmd(),
		validateCmd(),
		validateAgentsCmd(),
		versionCmd(),
		httpBridgeCmd(),
	)

	return root
}

func runCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start the orchestrator",
		RunE: func(cmd *cobra.Command, _ []string) error {
			workflowPath, _ := cmd.Flags().GetString("workflow")
			logLevel, _ := cmd.Flags().GetString("log-level")

			level := parseLogLevel(logLevel)
			logger := logging.NewLogger(os.Stdout, level)

			if err := bootstrapAnthemDir(logger); err != nil {
				return fmt.Errorf("bootstrapping ~/.anthem: %w", err)
			}

			// Load user-level constraints
			var userConstraints []string
			constraintsPath, err := constraints.DefaultPath()
			if err != nil {
				return fmt.Errorf("resolving constraints path: %w", err)
			}
			cc, err := constraints.LoadFile(constraintsPath)
			if err != nil {
				return fmt.Errorf("loading constraints: %w", err)
			}
			if cc.Loaded {
				userConstraints = cc.Constraints
			} else {
				logger.Debug("no user constraints file found, continuing without")
			}

			// Load user context (shared across ALL agents -- onboarding brief)
			var userContext string
			home, _ := os.UserHomeDir()
			voicePath := filepath.Join(home, ".anthem", "VOICE.md")
			vc, err := voice.LoadFile(voicePath)
			if err != nil {
				logger.Warn("VOICE.md not found, continuing without user context", "path", voicePath)
			} else {
				userContext = vc.Raw
			}

			// Run migration: split VOICE.md Identity/Personality into agents/orchestrator.md
			projectDir, _ := os.Getwd()
			agentsDir := filepath.Join(projectDir, "agents")
			projectName := strings.Replace(filepath.Base(projectDir), "-", " ", -1)
			projectName = strings.Title(projectName) //nolint:staticcheck
			if err := voice.MigrateVoiceToOrchestrator(voicePath, agentsDir, projectName, logger); err != nil {
				logger.Warn("voice migration failed", "error", err)
			}

			// Reload user context after migration (Identity/Personality may have been removed)
			if vc2, err := voice.LoadFile(voicePath); err == nil {
				userContext = vc2.Raw
			}

			// Load orchestrator persona (project-specific)
			var orchPersona string
			if p, err := guests.LoadOrchestratorPersona(agentsDir); err == nil {
				orchPersona = p
			} else if err != nil {
				logger.Debug("orchestrator.md not found, continuing without persona", "error", err)
			}

			cfg, body, err := config.LoadFile(workflowPath)
			if err != nil {
				return fmt.Errorf("loading workflow: %w", err)
			}
			if err := config.Validate(cfg); err != nil {
				return fmt.Errorf("validating workflow: %w", err)
			}

			// Override port if flag provided
			if cmd.Flags().Changed("port") {
				port, _ := cmd.Flags().GetInt("port")
				cfg.Server.Port = port
			}

			trk, err := createTracker(cmd.Context(), cfg, logger)
			if err != nil {
				return err
			}

			pm := claude.NewPlatformProcessManager()
			runner := claude.NewDriver(pm, logger, cfg.Agent.Command)

			var ws workspace.WorkspaceManager
			if cfg.Workspace.Shared {
				ws = workspace.NewSharedManager(cfg.Workspace.Root, cfg.Hooks, logger)
			} else {
				ws = workspace.NewManager(cfg.Workspace.Root, cfg.Hooks, logger)
			}

			events := orchestrator.NewEventBus(logger)

			statePath, err := orchestrator.DefaultStatePath()
			if err != nil {
				return fmt.Errorf("resolving state path: %w", err)
			}

			// Open audit logger
			auditPath := filepath.Join(home, ".anthem", "audit.db")
			auditLogger, err := audit.NewSQLiteAuditLogger(auditPath)
			if err != nil {
				return fmt.Errorf("opening audit database: %w", err)
			}
			defer auditLogger.Close()

			// Load channel credentials
			credPath, err := channel.DefaultCredentialsPath()
			if err != nil {
				return fmt.Errorf("resolving channel credentials path: %w", err)
			}
			channelCreds, err := channel.LoadCredentials(credPath)
			if err != nil {
				return fmt.Errorf("loading channel credentials: %w", err)
			}

			// Create channel manager and register adapters
			chanManager := channel.NewManager(logger)
			var prismTarget string
			var prismAdapter *prismch.Adapter
			if channelCreds != nil && len(cfg.Channels) > 0 {
				for _, chCfg := range cfg.Channels {
					switch chCfg.Kind {
					case "slack":
						if channelCreds.Slack != nil {
							slackAdapter := slackch.NewAdapter(
								channelCreds.Slack.BotToken,
								channelCreds.Slack.AppToken,
								chCfg.Target,
								logger,
							)
							chanManager.Register(slackAdapter)
							logger.Info("registered slack channel", "target", chCfg.Target)
						} else {
							logger.Warn("slack channel configured but no credentials found in channels.yaml")
						}
					case "dispatch":
						if channelCreds.Dispatch != nil {
							dispatchAdapter := dispatchch.NewAdapter(
								channelCreds.Dispatch.Token,
								chCfg.Target,
								logger,
							)
							chanManager.Register(dispatchAdapter)
							logger.Info("registered dispatch channel", "addr", chCfg.Target)
						} else {
							logger.Warn("dispatch channel configured but no credentials found in channels.yaml")
						}
					case "prism":
						if channelCreds.Prism != nil {
							prismAdapter = prismch.NewAdapter(
								channelCreds.Prism.Token,
								chCfg.Target,
								logger,
							)
							chanManager.Register(prismAdapter)
							prismTarget = chCfg.Target
							logger.Info("registered prism channel", "addr", chCfg.Target)
						} else {
							logger.Warn("prism channel configured but no credentials found in channels.yaml")
						}
					case "voice":
						if channelCreds.Voice == nil {
							logger.Warn("voice channel configured but no credentials found in channels.yaml")
							continue
						}
						vc := channelCreds.Voice
						var missing []string
						if vc.LiveKitURL == "" {
							missing = append(missing, "livekit_url")
						}
						if vc.LiveKitAPIKey == "" {
							missing = append(missing, "livekit_api_key")
						}
						if vc.LiveKitAPISecret == "" {
							missing = append(missing, "livekit_api_secret")
						}
						if vc.DeepgramAPIKey == "" {
							missing = append(missing, "deepgram_api_key")
						}
						if vc.ElevenLabsAPIKey == "" {
							missing = append(missing, "elevenlabs_api_key")
						}
						if len(missing) > 0 {
							logger.Warn("voice channel skipped: missing credentials", "fields", missing)
							continue
						}
						roomName := chCfg.Target
						if roomName == "" {
							roomName = "anthem-voice"
						}
						orchVoiceID := guests.ReadOrchestratorVoiceID(filepath.Join(projectDir, "agents"))
						if orchVoiceID != "" {
							logger.Info("orchestrator voice_id loaded", "voice_id", orchVoiceID)
						}
						voiceConfigs := make(map[string]string)
						if orchVoiceID != "" {
							voiceConfigs["orchestrator"] = orchVoiceID
						}
						if gIdx, err := guests.ScanDirectory(filepath.Join(projectDir, "agents"), logger); err == nil {
							for slug, agent := range gIdx.Agents {
								if agent.VoiceID != "" {
									voiceConfigs[slug] = agent.VoiceID
								}
								if agent.Role == "orchestrator" && agent.VoiceID != "" {
									voiceConfigs["orchestrator"] = agent.VoiceID
								}
							}
							if n := len(voiceConfigs); n > 1 {
								logger.Info("loaded guest voice configs", "count", n-1)
							}
						}
						stt := voicech.NewDeepgramSTT(vc.DeepgramAPIKey, logger)
						tts := voicech.NewElevenLabsTTS(vc.ElevenLabsAPIKey, logger)
						transport := voicech.NewLiveKitTransport(
							vc.LiveKitURL, vc.LiveKitAPIKey, vc.LiveKitAPISecret,
							roomName, "anthem-gateway", stt, tts, logger,
						)
						var fastLLM *voicech.FastLLM
						if vc.AnthropicAPIKey != "" {
							systemPrompt := fmt.Sprintf(
								"You are a voice assistant for the %s project. Be concise -- this is spoken aloud. "+
									"You can invite specialist agents, delegate tasks, or check status using the provided tools. "+
									"Keep replies under 2 sentences unless asked for detail.",
								filepath.Base(projectDir),
							)
							fastLLM = voicech.NewFastLLM(voicech.FastLLMOpts{
								APIKey:       vc.AnthropicAPIKey,
								SystemPrompt: systemPrompt,
								Tools:        voicech.VoiceToolDefs(),
								Logger:       logger,
							})
							logger.Info("voice fast-LLM enabled (direct Anthropic API)")
						} else {
							logger.Warn("anthropic_api_key not set, voice will use orchestrator fallback (higher latency)")
						}
						voiceAdapter := voicech.NewAdapterWithOpts(voicech.AdapterOpts{
							STT:                 stt,
							TTS:                 tts,
							Transport:           transport,
							OrchestratorVoiceID: orchVoiceID,
							VoiceConfigs:        voiceConfigs,
							FastLLM:             fastLLM,
							Logger:              logger,
						})
						chanManager.Register(voiceAdapter)
						logger.Info("registered voice channel", "room", roomName)
					default:
						logger.Warn("unknown channel kind, skipping", "kind", chCfg.Kind)
					}
				}
			}

			// Create orchestrator agent if enabled
			var orchAgent *orchestrator.OrchestratorAgent
			if cfg.Orchestrator.Enabled {
				orchAgent = orchestrator.NewOrchestratorAgent(
					runner, orchPersona, userContext, cfg.Orchestrator.MaxContextTokens, cfg.Orchestrator.MaxTurns, cfg.Orchestrator.PlanMaxTurns, cfg.Orchestrator.ExplorerMaxTurns, cfg.Orchestrator.MaxExplorers, logger,
				)
			}

			orch := orchestrator.New(orchestrator.Opts{
				Config:          cfg,
				TemplateBody:    body,
				Tracker:         trk,
				Runner:          runner,
				Workspace:       ws,
				EventBus:        events,
				Logger:          logger,
				VoiceContent:    userContext,
				UserConstraints: userConstraints,
				StatePath:       statePath,
				OrchAgent:       orchAgent,
				AuditLogger:     auditLogger,
				ChannelManager:  chanManager,
			})

			// Wire guest agent updates and mode to Prism adapter
			if prismAdapter != nil {
				orch.SetGuestUpdateCallback(prismAdapter.UpdateGuestIndex)
				if cfg.MaxActiveGuests > 0 {
					prismAdapter.SetMaxActiveGuests(cfg.MaxActiveGuests)
				}
				prismAdapter.SetCurrentMode(string(orch.CurrentMode))
			}

			// Start config hot-reload watcher
			cfgWatcher := config.NewWatcher(workflowPath, orch.ReloadConfig, logger)
			if err := cfgWatcher.Start(); err != nil {
				logger.Warn("config watcher failed to start, hot-reload disabled", "error", err)
			}
			defer cfgWatcher.Stop()

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()

			// Start channel manager
			if err := chanManager.Start(ctx); err != nil {
				logger.Warn("failed to start channel manager, continuing without channels", "error", err)
			}
			defer chanManager.Close()

			// Merge event filters from all channel configs for the bridge
			seen := make(map[string]bool)
			var bridgeAllowedEvents []string
			for _, chCfg := range cfg.Channels {
				for _, ev := range chCfg.Events {
					if !seen[ev] {
						seen[ev] = true
						bridgeAllowedEvents = append(bridgeAllowedEvents, ev)
					}
				}
			}
			eventBridge := channel.NewEventBridge(chanManager, events.Subscribe(), bridgeAllowedEvents, logger)
			eventBridge.Start(ctx)
			defer eventBridge.Close()

			// Start maintenance scanner
			scanner := maintenance.NewScanner(auditLogger, events, cfg.Maintenance, logger)
			scanner.Start(ctx)
			defer scanner.Close()

			// Start channel listener for inbound messages
			orch.StartChannelListener(ctx)

			// Start mDNS advertisement for Prism discovery
			project := cfg.Tracker.Repo
			if parts := strings.SplitN(project, "/", 2); len(parts) == 2 {
				project = parts[1]
			}
			mdnsPort := 0
			if prismTarget != "" {
				if _, portStr, err := net.SplitHostPort(prismTarget); err == nil {
					if p, err := strconv.Atoi(portStr); err == nil {
						mdnsPort = p
					}
				}
			}
			if mdnsPort == 0 {
				mdnsPort = cfg.Server.Port
			}
			if mdnsPort == 0 {
				mdnsPort = 8080
			}
			advertiser := discovery.NewAdvertiser(project, mdnsPort, cfg.Tracker.Repo, logger)
			if err := advertiser.Start(); err != nil {
				logger.Warn("mDNS advertisement failed to start, Prism auto-discovery disabled", "error", err)
			} else {
				defer advertiser.Stop()
			}

			logger.Info("starting anthem",
				"workflow", workflowPath,
				"tracker", cfg.Tracker.Kind,
			)

			return orch.Run(ctx)
		},
	}
	cmd.Flags().StringP("workflow", "w", "WORKFLOW.md", "Path to workflow file")
	cmd.Flags().Int("port", 8080, "Server port (mDNS discovery fallback)")
	cmd.Flags().String("log-level", "info", "Log level (debug, info, warn, error)")
	return cmd
}

func validateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Validate WORKFLOW.md without starting",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, _ := cmd.Flags().GetString("workflow")

			cfg, _, err := config.LoadFile(path)
			if err != nil {
				return fmt.Errorf("loading %s: %w", path, err)
			}

			if err := config.Validate(cfg); err != nil {
				return fmt.Errorf("validation failed:\n%w", err)
			}

			fmt.Printf("%s is valid\n", path)
			fmt.Printf("  tracker: %s\n", cfg.Tracker.Kind)
			if cfg.Tracker.Repo != "" {
				fmt.Printf("  repo:    %s\n", cfg.Tracker.Repo)
			}
			fmt.Printf("  agent:   %s (max %d concurrent, %d turns)\n",
				cfg.Agent.Command, cfg.Agent.MaxConcurrent, cfg.Agent.MaxTurns)
			fmt.Printf("  rules:   %d defined\n", len(cfg.Rules))
			return nil
		},
	}
	cmd.Flags().StringP("workflow", "w", "WORKFLOW.md", "Path to workflow file")
	return cmd
}

func initCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create starter WORKFLOW.md and bootstrap ~/.anthem/",
		RunE: func(_ *cobra.Command, _ []string) error {
			// Create WORKFLOW.md in current directory
			if err := createFileIfNotExists("WORKFLOW.md", defaultWorkflow); err != nil {
				return err
			}

			// Bootstrap ~/.anthem/, VOICE.md, and constraints.yaml
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("resolving home directory: %w", err)
			}
			anthemDir := filepath.Join(home, ".anthem")
			if err := os.MkdirAll(anthemDir, 0755); err != nil {
				return fmt.Errorf("creating %s: %w", anthemDir, err)
			}

			voicePath := filepath.Join(anthemDir, "VOICE.md")
			if err := createFileIfNotExists(voicePath, defaultVoice); err != nil {
				return err
			}

			constraintsPath := filepath.Join(anthemDir, "constraints.yaml")
			if err := createFileIfNotExists(constraintsPath, defaultConstraints); err != nil {
				return err
			}

			channelsPath := filepath.Join(anthemDir, "channels.yaml")
			if err := createFileIfNotExists(channelsPath, defaultChannels); err != nil {
				return err
			}

			// Create agents/orchestrator.md in cwd
			agentsDir := filepath.Join(".", "agents")
			if err := os.MkdirAll(agentsDir, 0755); err != nil {
				return fmt.Errorf("creating agents directory: %w", err)
			}
			orchPath := filepath.Join(agentsDir, "orchestrator.md")
			cwd, _ := os.Getwd()
			dirName := filepath.Base(cwd)
			displayName := strings.Replace(dirName, "-", " ", -1)
			displayName = strings.Title(displayName) //nolint:staticcheck
			orchContent := fmt.Sprintf(defaultOrchestrator, displayName, displayName)
			if err := createFileIfNotExists(orchPath, orchContent); err != nil {
				return err
			}

			fmt.Println("Anthem initialized:")
			fmt.Println("  ./WORKFLOW.md created")
			fmt.Printf("  %s created\n", voicePath)
			fmt.Printf("  %s created\n", constraintsPath)
			fmt.Printf("  %s created\n", channelsPath)
			fmt.Printf("  %s created\n", orchPath)
			return nil
		},
	}
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version",
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Printf("anthem %s\n", version)
		},
	}
}

func createTracker(ctx context.Context, cfg *config.Config, logger *slog.Logger) (tracker.IssueTracker, error) {
	switch cfg.Tracker.Kind {
	case "":
		return nil, nil
	case "github":
		owner, repo, err := ghtracker.ParseRepo(cfg.Tracker.Repo)
		if err != nil {
			return nil, err
		}
		return ghtracker.New(ctx, ghtracker.Options{
			Owner:        owner,
			Repo:         repo,
			ActiveLabels: cfg.Tracker.Labels.Active,
			Logger:       logger,
		})
	case "local_json":
		return localtracker.New("tasks.json"), nil
	default:
		return nil, fmt.Errorf("unsupported tracker kind: %s", cfg.Tracker.Kind)
	}
}

func bootstrapAnthemDir(logger *slog.Logger) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolving home directory: %w", err)
	}
	return bootstrapDir(filepath.Join(home, ".anthem"), logger)
}

func bootstrapDir(anthemDir string, logger *slog.Logger) error {
	created := false
	if _, err := os.Stat(anthemDir); os.IsNotExist(err) {
		if err := os.MkdirAll(anthemDir, 0755); err != nil {
			return fmt.Errorf("creating %s: %w", anthemDir, err)
		}
		logger.Info("created anthem directory", "path", anthemDir)
		created = true
	}

	voicePath := filepath.Join(anthemDir, "VOICE.md")
	if _, err := os.Stat(voicePath); os.IsNotExist(err) {
		if err := os.WriteFile(voicePath, []byte(defaultVoice), 0644); err != nil {
			return fmt.Errorf("writing %s: %w", voicePath, err)
		}
		logger.Info("created default VOICE.md", "path", voicePath)
		created = true
	}

	constraintsPath := filepath.Join(anthemDir, "constraints.yaml")
	if _, err := os.Stat(constraintsPath); os.IsNotExist(err) {
		if err := os.WriteFile(constraintsPath, []byte(defaultConstraints), 0644); err != nil {
			return fmt.Errorf("writing %s: %w", constraintsPath, err)
		}
		logger.Info("created default constraints.yaml", "path", constraintsPath)
		created = true
	}

	channelsPath := filepath.Join(anthemDir, "channels.yaml")
	if _, err := os.Stat(channelsPath); os.IsNotExist(err) {
		if err := os.WriteFile(channelsPath, []byte(defaultChannels), 0644); err != nil {
			return fmt.Errorf("writing %s: %w", channelsPath, err)
		}
		logger.Info("created default channels.yaml", "path", channelsPath)
		created = true
	}

	if !created {
		logger.Debug("anthem directory already bootstrapped", "path", anthemDir)
	}
	return nil
}

func createFileIfNotExists(path, content string) error {
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("  %s already exists, skipping\n", path)
		return nil
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

func httpBridgeCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "http-bridge",
		Short:  "Run as an MCP stdio server bridging HTTP tool configs",
		Hidden: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			tools, err := httpbridge.LoadConfigFromEnv()
			if err != nil {
				return fmt.Errorf("loading http-bridge config: %w", err)
			}
			root, err := httpbridge.LoadRootFromEnv()
			if err != nil {
				return fmt.Errorf("loading http-bridge root: %w", err)
			}
			return httpbridge.Run(tools, root, os.Stdin, os.Stdout)
		},
	}
}

func parseLogLevel(s string) slog.Level {
	switch s {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
