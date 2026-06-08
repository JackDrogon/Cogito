package feishuproject

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/larksuite/project-oapi-sdk-golang/v2/service/workitem"
)

// typeContext caches the field keys resolved for a particular work item type
// so Pull does not re-detect them on every story.
type typeContext struct {
	TypeKey            string
	OwnerFieldKey      string
	PlannedEffortKey   string
	EstimatePointKey   string
	RequirementTypeKey string
	RepoFieldKey       string
}

// detectTypeContext picks the canonical field keys used downstream. The
// strict owner field falls back to keyword search if the project did not
// expose the standard workitem_owner field.
//
// RequirementTypeKey targets the `template` field — Lark Project's per-
// story template reference. Its value is a map shaped like {id, version}
// rather than a label, so the extracted Story.RequirementType ends up as
// the template ID. Callers who want a human-readable template name need an
// additional template-detail lookup, intentionally not done here.
//
// RepoFieldKey targets a custom select field whose alias is conventionally
// "repo"; the concrete key is a project-scoped hash so we keyword-detect
// rather than hard-code it.
func detectTypeContext(typeKey string, fields []workitem.SimpleField) typeContext {
	ctx := typeContext{TypeKey: typeKey}

	ctx.OwnerFieldKey = resolveStrictOwnerFieldKey(fields)
	if ctx.OwnerFieldKey == "" {
		ctx.OwnerFieldKey = detectFieldKey(fields, []string{"owner", "assignee", "负责人"})
	}
	ctx.PlannedEffortKey = detectFieldKey(fields, []string{"计划工时", "预估工时", "planned effort", "planned hour", "planned work"})
	ctx.EstimatePointKey = detectFieldKey(fields, []string{"story point", "story_point", "estimate point", "估点", "点数"})
	ctx.RequirementTypeKey = detectFieldKey(fields, []string{"template"})
	ctx.RepoFieldKey = detectFieldKey(fields, []string{"repo"})

	return ctx
}

// extractFirstString reads a single field by key and returns the first
// stringified value. Used for select-style fields whose value is either a
// scalar or a {label,value} map.
func extractFirstString(item workitem.WorkItemInfo, fieldKey string) string {
	if fieldKey == "" {
		return ""
	}
	field, ok := findField(item.Fields, fieldKey)
	if !ok {
		return ""
	}
	values := stringsFromAny(field.FieldValue)
	if len(values) == 0 {
		return ""
	}

	return values[0]
}

// extractDescription pulls the plain-text body of the standard `description`
// MultiText field. Rich text and HTML variants are intentionally ignored so
// downstream task-input use cases stay markup-free.
func extractDescription(item workitem.WorkItemInfo) string {
	for _, mt := range item.MultiTexts {
		if derefString(mt.FieldKey) != "description" {
			continue
		}
		if mt.FieldValue == nil {
			return ""
		}
		return strings.TrimSpace(derefString(mt.FieldValue.DocText))
	}

	return ""
}

func resolveStrictOwnerFieldKey(fields []workitem.SimpleField) string {
	for _, field := range fields {
		parts := []string{
			strings.ToLower(strings.TrimSpace(derefString(field.FieldKey))),
			strings.ToLower(strings.TrimSpace(derefString(field.FieldAlias))),
			strings.ToLower(strings.TrimSpace(derefString(field.FieldName))),
		}
		for _, part := range parts {
			switch part {
			case "workitem_owner", "当前需求的负责人":
				return derefString(field.FieldKey)
			}
		}
	}

	return ""
}

func detectFieldKey(fields []workitem.SimpleField, keywords []string) string {
	bestKey := ""
	bestScore := -1
	for _, field := range fields {
		score := scoreField(field, keywords)
		if score > bestScore {
			bestScore = score
			bestKey = derefString(field.FieldKey)
		}
	}
	if bestScore <= 0 {
		return ""
	}

	return bestKey
}

func scoreField(field workitem.SimpleField, keywords []string) int {
	parts := []string{
		strings.ToLower(derefString(field.FieldKey)),
		strings.ToLower(derefString(field.FieldName)),
		strings.ToLower(derefString(field.FieldAlias)),
	}
	score := 0
	for _, keyword := range keywords {
		lowerKeyword := strings.ToLower(keyword)
		for _, part := range parts {
			switch {
			case part == lowerKeyword:
				score += 5
			case strings.Contains(part, lowerKeyword):
				score += 2
			}
		}
	}

	return score
}

// findField scans the FieldValuePair slice for a matching key. Returns the
// pair and true when found.
func findField(fields []workitem.FieldValuePair, fieldKey string) (workitem.FieldValuePair, bool) {
	for _, field := range fields {
		if derefString(field.FieldKey) == fieldKey {
			return field, true
		}
	}

	return workitem.FieldValuePair{}, false
}

func extractNumber(item workitem.WorkItemInfo, fieldKey string) (float64, bool) {
	if fieldKey == "" {
		return 0, false
	}
	field, ok := findField(item.Fields, fieldKey)
	if !ok {
		return 0, false
	}

	return numberFromAny(field.FieldValue)
}

func extractStatus(item workitem.WorkItemInfo) string {
	if item.WorkItemStatus == nil {
		return ""
	}

	return derefString(item.WorkItemStatus.StateKey)
}

func extractPriority(item workitem.WorkItemInfo) string {
	field, ok := findField(item.Fields, "priority")
	if !ok {
		return ""
	}
	values := stringsFromAny(field.FieldValue)
	if len(values) == 0 {
		return ""
	}

	return values[0]
}

func extractBusinessID(item workitem.WorkItemInfo) string {
	field, ok := findField(item.Fields, "business")
	if !ok {
		return ""
	}
	values := stringsFromAny(field.FieldValue)
	if len(values) == 0 {
		return ""
	}

	return values[0]
}

func extractOwnerNames(item workitem.WorkItemInfo, fieldKey string) []string {
	if fieldKey == "" {
		return nil
	}
	field, ok := findField(item.Fields, fieldKey)
	if !ok {
		return nil
	}
	userKeys := uniqueStrings(stringsFromAny(field.FieldValue))
	if len(userKeys) == 0 {
		return nil
	}

	return resolveUserNames(userKeys, item.UserDetails)
}

func extractCreatorName(item workitem.WorkItemInfo) string {
	field, ok := findField(item.Fields, "owner")
	if !ok {
		return ""
	}
	userKeys := uniqueStrings(stringsFromAny(field.FieldValue))
	names := resolveUserNames(userKeys, item.UserDetails)
	if len(names) == 0 {
		return ""
	}

	return names[0]
}

func resolveUserNames(userKeys []string, details []workitem.UserDetail) []string {
	nameByKey := make(map[string]string, len(details))
	for _, detail := range details {
		key := derefString(detail.UserKey)
		if key == "" {
			continue
		}
		nameByKey[key] = firstNonEmpty(
			derefString(detail.NameCn),
			derefString(detail.NameEn),
			derefString(detail.Username),
			derefString(detail.Email),
			key,
		)
	}

	names := make([]string, 0, len(userKeys))
	for _, key := range userKeys {
		if name, ok := nameByKey[key]; ok && name != "" {
			names = append(names, name)
			continue
		}
		names = append(names, key)
	}

	return uniqueStrings(names)
}

// stringsFromAny normalizes the polymorphic FieldValue payloads into a
// string slice. The SDK returns either scalars, slices, or user-detail maps
// depending on the field type, so a single converter avoids duplication.
func stringsFromAny(value any) []string {
	if value == nil {
		return nil
	}

	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	case []string:
		return uniqueStrings(typed)
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			values = append(values, stringsFromAny(item)...)
		}
		return uniqueStrings(values)
	case map[string]any:
		return stringsFromMap(typed)
	case map[string]string:
		converted := make(map[string]any, len(typed))
		for key, item := range typed {
			converted[key] = item
		}
		return stringsFromMap(converted)
	default:
		if number, ok := numberFromAny(value); ok {
			return []string{strconv.FormatFloat(number, 'f', -1, 64)}
		}
		return nil
	}
}

func stringsFromMap(value map[string]any) []string {
	preferred := []string{"name_cn", "name_en", "username", "name", "label", "title", "display_name", "email", "user_key", "value", "key", "id"}
	for _, key := range preferred {
		if raw, ok := value[key]; ok {
			values := stringsFromAny(raw)
			if len(values) > 0 {
				return values
			}
		}
	}

	collected := make([]string, 0, len(value))
	for _, raw := range value {
		collected = append(collected, stringsFromAny(raw)...)
	}

	return uniqueStrings(collected)
}

func numberFromAny(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}

	return result
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}

	return ""
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}

	return *value
}
