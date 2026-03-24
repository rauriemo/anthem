package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/rauriemo/anthem/internal/agent/claude"
	"github.com/rauriemo/anthem/internal/audit"
	"github.com/rauriemo/anthem/internal/channel"
	slackch "github.com/rauriemo/anthem/internal/channel/slack"
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/constraints"
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
		statusCmd(),
		versionCmd(),
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

			// Load voice
			var voiceContent string
			home, _ := os.UserHomeDir()
			voicePath := filepath.Join(home, ".anthem", "VOICE.md")
			vc, err := voice.LoadFile(voicePath)
			if err != nil {
				logger.Warn("VOICE.md not found, continuing without personality", "path", voicePath)
			} else {
				voiceContent = vc.Raw
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
			runner := claude.NewDriver(pm, logger)

			ws := workspace.NewManager(cfg.Workspace.Root, cfg.Hooks, logger)

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
			if channelCreds != nil && channelCreds.Slack != nil && len(cfg.Channels) > 0 {
				for _, chCfg := range cfg.Channels {
					if chCfg.Kind == "slack" {
						slackAdapter := slackch.NewAdapter(
							channelCreds.Slack.BotToken,
							channelCreds.Slack.AppToken,
							chCfg.Target,
							logger,
						)
						chanManager.Register(slackAdapter)
						logger.Info("registered slack channel", "target", chCfg.Target)
					}
				}
			}

			// Create orchestrator agent if enabled
			var orchAgent *orchestrator.OrchestratorAgent
			if cfg.Orchestrator.Enabled {
				orchAgent = orchestrator.NewOrchestratorAgent(
					runner, voiceContent, cfg.Orchestrator.MaxContextTokens, logger,
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
				VoiceContent:    voiceContent,
				UserConstraints: userConstraints,
				StatePath:       statePath,
				OrchAgent:       orchAgent,
				AuditLogger:     auditLogger,
				ChannelManager:  chanManager,
			})

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

			logger.Info("starting anthem",
				"workflow", workflowPath,
				"tracker", cfg.Tracker.Kind,
			)

			return orch.Run(ctx)
		},
	}
	cmd.Flags().StringP("workflow", "w", "WORKFLOW.md", "Path to workflow file")
	cmd.Flags().Int("port", 8080, "Dashboard port")
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

			fmt.Println("Anthem initialized:")
			fmt.Println("  ./WORKFLOW.md created")
			fmt.Printf("  %s created\n", voicePath)
			fmt.Printf("  %s created\n", constraintsPath)
			fmt.Printf("  %s created\n", channelsPath)
			return nil
		},
	}
}

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Query running orchestrator state",
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Println("anthem status: not yet implemented (Phase 3)")
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
