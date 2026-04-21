package orchestrator

import "github.com/rauriemo/anthem/internal/contextreport"

// The types and parser originally lived in this file. They now live in
// internal/contextreport so internal/execute can read reports without
// importing orchestrator (which would introduce a cycle: orchestrator
// already imports execute). The aliases below keep every existing
// orchestrator-package caller working unchanged.

// ContextReport re-exports [contextreport.ContextReport] for backwards
// compatibility with orchestrator-package callers.
type ContextReport = contextreport.ContextReport

// ReportArtifact re-exports [contextreport.ReportArtifact] for backwards
// compatibility with orchestrator-package callers.
type ReportArtifact = contextreport.ReportArtifact

// ParseContextReport re-exports [contextreport.Parse] for backwards
// compatibility with orchestrator-package callers.
var ParseContextReport = contextreport.Parse
