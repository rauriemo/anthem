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
	"github.com/rauriemo/anthem/internal/config"
	"github.com/rauriemo/anthem/internal/logging"
	"github.com/rauriemo/anthem/internal/orchestrator"
	"github.com/rauriemo/anthem/internal/tracker"
	"github.com/rauriemo/anthem/internal/types"
	ghtracker "github.com/rauriemo/anthem/internal/tracker/github"
	localtracker "github.com/rauriemo/anthem/internal/tracker/local"
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

			ws := workspace.NewMockWorkspaceManager()
			ws.PrepareFunc = func(_ context.Context, _ types.Task) (string, error) {
				return ".", nil
			}

			events := orchestrator.NewEventBus(logger)

			orch := orchestrator.New(orchestrator.Opts{
				Config:       cfg,
				TemplateBody: body,
				Tracker:      trk,
				Runner:       runner,
				Workspace:    ws,
				EventBus:     events,
				Logger:       logger,
			})

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()

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
		Short: "Create starter WORKFLOW.md and bootstrap ~/.anthem/VOICE.md",
		RunE: func(_ *cobra.Command, _ []string) error {
			// Create WORKFLOW.md in current directory
			if err := createFileIfNotExists("WORKFLOW.md", defaultWorkflow); err != nil {
				return err
			}

			// Bootstrap ~/.anthem/ and VOICE.md
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

			fmt.Println("Anthem initialized:")
			fmt.Println("  ./WORKFLOW.md created")
			fmt.Printf("  %s created\n", voicePath)
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
