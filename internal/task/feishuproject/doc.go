/*
Package feishuproject pulls work items (stories) from a Feishu/Lark Project
(Meegle) project space.

This package is part of Cogito's task-management surface, not the AI provider
adapter layer. It does not implement provider.Provider; runtime and workflow
packages never import it. Higher CLI layers in internal/app wire it behind a
dedicated `cogito feishu` subcommand group.

# Scope

The package is intentionally read-only against the upstream API. It resolves a
work item type, lists work items in the configured project, normalizes the
relevant fields into Story values, and persists incremental state so a later
poll can report additions and changes.

Provider-specific types from larksuite/project-oapi-sdk-golang are confined to
this package; only the Story value type and Service entry points are exported.

# Endpoints

  - https://project.feishu.cn      (国内站, domestic)
  - https://project.larksuite.com  (国际站, international)

The active endpoint is selected via [meegle].base_url in the configuration
file; no domain is hard-coded.

# Lifecycle

Pull executes a single synchronous fetch and returns the current Story set
plus an incremental diff against on-disk state. Watch wraps Pull in a ticker
that survives process restarts by reading and writing the same state file.
*/
package feishuproject
