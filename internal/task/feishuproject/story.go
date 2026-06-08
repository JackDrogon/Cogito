package feishuproject

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
)

// Story is the exported value type for one work item pulled from a Feishu
// Project space. It deliberately omits SDK-specific types so callers and
// JSON consumers stay decoupled from the upstream client.
//
// Workflow is populated on Pull from a second-stage GetWorkFlow call per
// story; it is nil when that fetch fails or returns no nodes. The deliberate
// pointer-elision lets json.Marshal omit the field for stories that have no
// node-flow data, keeping the snapshot compact.
type Story struct {
	ID              int64          `json:"id"`
	Name            string         `json:"name"`
	WorkItemTypeKey string         `json:"work_item_type_key"`
	ProjectKey      string         `json:"project_key"`
	Status          string         `json:"status,omitempty"`
	Priority        string         `json:"priority,omitempty"`
	Owners          []string       `json:"owners,omitempty"`
	Creator         string         `json:"creator,omitempty"`
	BusinessLineID  string         `json:"business_line_id,omitempty"`
	PlannedEffort   *float64       `json:"planned_effort,omitempty"`
	StoryPoint      *float64       `json:"story_point,omitempty"`
	RequirementType string         `json:"requirement_type,omitempty"`
	Repo            string         `json:"repo,omitempty"`
	RepoPath        string         `json:"repo_path,omitempty"`
	Description     string         `json:"description,omitempty"`
	CreatedAt       int64          `json:"created_at,omitempty"`
	UpdatedAt       int64          `json:"updated_at,omitempty"`
	Workflow        []WorkflowNode `json:"workflow,omitempty"`
}

// Fingerprint returns a stable hex digest that changes whenever any
// user-visible field of the story changes. Watch mode uses it to detect
// updates without storing the entire previous story snapshot.
func (s Story) Fingerprint() string {
	owners := append([]string(nil), s.Owners...)
	sort.Strings(owners)

	parts := []string{
		s.Name,
		s.Status,
		s.Priority,
		strings.Join(owners, ","),
		s.BusinessLineID,
		s.RequirementType,
		s.Repo,
		s.Description,
		floatToken(s.PlannedEffort),
		floatToken(s.StoryPoint),
	}

	hash := sha256.Sum256([]byte(strings.Join(parts, "|")))

	return hex.EncodeToString(hash[:])
}

func floatToken(value *float64) string {
	if value == nil {
		return ""
	}

	return formatFloat(*value)
}
