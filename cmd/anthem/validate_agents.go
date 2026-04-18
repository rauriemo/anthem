package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rauriemo/anthem/internal/guests"
)

// validateAgentsCmd loads every agent in a directory, runs ValidateReviewSpec
// on each one's review: block, and exits non-zero on any error-level
// diagnostic. Intended for project CI. See docs/architecture/review-kinds.md
// for the validation contract.
func validateAgentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "validate-agents",
		Short: "Validate review specs on every agent in an agents/ directory",
		Long: `Loads each agent .md in the given directory, parses its frontmatter,
and validates the review: block against the canonical core kinds and panel
types. Prints a diagnostic table and exits non-zero on any error-level
diagnostic. Warnings and info diagnostics are printed but do not fail.

Projects can declare custom review kinds in <project>/.prism/review-extensions.yaml
with an extends: fallback so extension kinds are not flagged as unknown.

Examples:
  anthem validate-agents
  anthem validate-agents --dir path/to/agents
  anthem validate-agents --dir agents --quiet`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, _ := cmd.Flags().GetString("dir")
			quiet, _ := cmd.Flags().GetBool("quiet")
			return runValidateAgents(cmd.OutOrStdout(), cmd.ErrOrStderr(), dir, quiet)
		},
	}
	cmd.Flags().String("dir", "agents", "Path to the agents directory to validate")
	cmd.Flags().Bool("quiet", false, "Suppress per-diagnostic output; only print the summary")
	return cmd
}

// runValidateAgents is the command body, extracted for testability. It
// returns a non-nil error if any error-level diagnostic was found, allowing
// cobra to surface the non-zero exit code.
func runValidateAgents(out, errOut io.Writer, dir string, quiet bool) error {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("resolving agents dir: %w", err)
	}
	info, err := os.Stat(absDir)
	if err != nil {
		return fmt.Errorf("agents dir %s: %w", absDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("agents dir %s is not a directory", absDir)
	}

	projectRoot := filepath.Dir(absDir)
	manifest, err := guests.LoadExtensionManifest(projectRoot)
	if err != nil {
		fmt.Fprintf(errOut, "warning: could not load extension manifest: %v\n", err)
	}
	extensionIDs := manifest.ExtensionKindIDs()

	entries, err := os.ReadDir(absDir)
	if err != nil {
		return fmt.Errorf("reading agents dir: %w", err)
	}

	type agentReport struct {
		slug  string
		diags []guests.ReviewDiagnostic
		err   error
	}
	var reports []agentReport

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		if entry.Name() == "orchestrator.md" {
			continue
		}
		slug := strings.TrimSuffix(entry.Name(), ".md")
		data, err := os.ReadFile(filepath.Join(absDir, entry.Name()))
		if err != nil {
			reports = append(reports, agentReport{slug: slug, err: err})
			continue
		}
		agent, err := guests.ParseFrontmatter(data)
		if err != nil {
			reports = append(reports, agentReport{slug: slug, err: err})
			continue
		}
		if agent.Review == nil {
			reports = append(reports, agentReport{slug: slug})
			continue
		}
		diags := guests.ValidateReviewSpec(agent.Review, extensionIDs)
		reports = append(reports, agentReport{slug: slug, diags: diags})
	}

	sort.SliceStable(reports, func(i, j int) bool { return reports[i].slug < reports[j].slug })

	var errors, warnings, infos, parseErrs int
	for _, r := range reports {
		if r.err != nil {
			parseErrs++
			fmt.Fprintf(out, "%s: parse error: %v\n", r.slug, r.err)
			continue
		}
		if len(r.diags) == 0 {
			if !quiet {
				fmt.Fprintf(out, "%s: ok\n", r.slug)
			}
			continue
		}
		for _, d := range r.diags {
			switch d.Severity {
			case guests.SeverityError:
				errors++
			case guests.SeverityWarning:
				warnings++
			case guests.SeverityInfo:
				infos++
			}
			if !quiet {
				fmt.Fprintf(out, "%s: %s: %s: %s\n", r.slug, d.Severity, d.Code, d.Message)
			}
		}
	}

	fmt.Fprintf(out, "\nValidated %d agents: %d errors, %d warnings, %d info, %d parse failures\n",
		len(reports), errors, warnings, infos, parseErrs)

	if errors > 0 || parseErrs > 0 {
		return fmt.Errorf("review spec validation failed: %d errors, %d parse failures", errors, parseErrs)
	}
	return nil
}
