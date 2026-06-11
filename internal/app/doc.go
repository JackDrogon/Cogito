/*
Package app is the CLI-facing orchestration layer for Cogito.

# Responsibilities

This package owns five concerns:

  - Command registration: rootCommands and workflowCommands are command registry
    tables that route subcommands to their handlers. Adding or renaming a command
    means updating those tables in app.go and app_commands.go, not adding ad hoc
    argument branching.

  - Shared flag parsing: parseSharedFlags is the single source of truth for the
    flags common to all execution commands (--repo, --state-dir, --approval,
    --provider-timeout, --allow-dirty, -v). Commands that mix shared flags with
    their own flags call registerSharedFlags directly so definitions stay in sync.

  - Service coordination: application_service.go coordinates the fixed execution
    order for run-class commands: parse approval mode, resolve run state directory,
    ensure the .cogito root is git-ignored, acquire the repo lock, open the store,
    persist the compiled workflow, build the engine, execute until settled, and
    release the lock on return. run_service.go owns the lower-level engine
    construction and execution loop.

  - Runtime dependency wiring: wiring.go resolves the repo path and working
    directory (from --repo flag, checkpoint, or cwd fallback), builds the adapter
    lookup and command runner, and assembles the MachineDependencies struct passed
    to the engine. Provider subpackages are imported here only as blank imports
    that populate the adapter registry; no provider-specific logic leaks into
    command handlers.

  - Presentation: text_presenter.go formats run status, replay views, and error
    summaries for human-readable CLI output. Diagnostic messages go to stderr via
    diagnosticOutput; command output uses the injected stdout writer passed through Run.

# Layering rules

Commands call service methods; service methods call runtime and store; wiring
builds dependencies without leaking provider details. Presenters consume view
types from runtime; they never format durable state directly from store types.
*/
package app
