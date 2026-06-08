package feishuproject

import (
	"context"
	"fmt"
	"sync"

	"github.com/larksuite/project-oapi-sdk-golang/v2/service/workitem"
)

// workflowFetchConcurrency caps in-flight GetWorkFlow calls so a large project
// doesn't burst past the upstream rate limit. Chosen well below gen-daily's
// observed 13rps ceiling because each Pull also issues a Filter call.
const workflowFetchConcurrency = 8

// flowTypeNode selects the node-flow shape (workflow-mode work items). State-
// flow work items would use flowTypeState (1); the project's "story" type is
// node-flow so we hard-code 0 here.
const flowTypeNode = int64(0)

// WorkflowNode is the exported subset of a node in a story's node flow.
// The upstream payload carries more (schedules, sub-tasks, role assignees),
// but we only surface fields that are useful for downstream review and diff.
type WorkflowNode struct {
	ID               string   `json:"id"`
	Name             string   `json:"name,omitempty"`
	StateKey         string   `json:"state_key,omitempty"`
	Status           int32    `json:"status"`
	Owners           []string `json:"owners,omitempty"`
	ActualBeginTime  string   `json:"actual_begin_time,omitempty"`
	ActualFinishTime string   `json:"actual_finish_time,omitempty"`
}

// attachWorkflows fetches each story's node flow concurrently and assigns
// the resulting slice to Story.Workflow. A per-story fetch failure is logged
// through the error channel but does not abort the whole Pull.
func (s *Service) attachWorkflows(ctx context.Context, stories []Story) []error {
	if len(stories) == 0 {
		return nil
	}

	var (
		mu      sync.Mutex
		errs    []error
		sem     = make(chan struct{}, workflowFetchConcurrency)
		wg      sync.WaitGroup
		typeKey = s.cfg.WorkItemTypeKey
	)

	for index := range stories {
		index := index
		select {
		case <-ctx.Done():
			mu.Lock()
			errs = append(errs, ctx.Err())
			mu.Unlock()
			return errs
		case sem <- struct{}{}:
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()

			nodes, err := s.fetchWorkflow(ctx, typeKey, stories[index].ID)
			if err != nil {
				mu.Lock()
				errs = append(errs, fmt.Errorf("workflow story=%d: %w", stories[index].ID, err))
				mu.Unlock()
				return
			}
			stories[index].Workflow = nodes
		}()
	}

	wg.Wait()

	return errs
}

func (s *Service) fetchWorkflow(ctx context.Context, typeKey string, storyID int64) ([]WorkflowNode, error) {
	req := workitem.NewGetWorkFlowReqBuilder().
		ProjectKey(s.cfg.ProjectKey).
		WorkItemTypeKey(typeKey).
		WorkItemID(storyID).
		FlowType(flowTypeNode).
		Build()

	resp, err := s.client.WorkItem.GetWorkFlow(ctx, req, requestOptions(s.cfg)...)
	if err != nil {
		return nil, fmt.Errorf("get workflow: %w", err)
	}
	if !resp.Success() {
		return nil, fmt.Errorf(
			"get workflow failed: code=%d request_id=%s msg=%s",
			resp.Code(), resp.RequestId(), resp.ErrMsg,
		)
	}
	if resp.Data == nil {
		return nil, nil
	}

	return mapWorkflowNodes(resp.Data.WorkflowNodes, resp.Data.UserDetails), nil
}

func mapWorkflowNodes(rawNodes []workitem.WorkItem_work_item_WorkflowNode, details []workitem.WorkItem_work_item_UserDetail) []WorkflowNode {
	if len(rawNodes) == 0 {
		return nil
	}

	nameByKey := workflowUserNames(details)
	nodes := make([]WorkflowNode, 0, len(rawNodes))
	for _, raw := range rawNodes {
		nodes = append(nodes, WorkflowNode{
			ID:               derefString(raw.ID),
			Name:             derefString(raw.Name),
			StateKey:         derefString(raw.StateKey),
			Status:           derefInt32(raw.Status),
			Owners:           resolveOwnerNames(raw.Owners, nameByKey),
			ActualBeginTime:  derefString(raw.ActualBeginTime),
			ActualFinishTime: derefString(raw.ActualFinishTime),
		})
	}

	return nodes
}

func workflowUserNames(details []workitem.WorkItem_work_item_UserDetail) map[string]string {
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

	return nameByKey
}

func resolveOwnerNames(userKeys []string, nameByKey map[string]string) []string {
	if len(userKeys) == 0 {
		return nil
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

func derefInt32(value *int32) int32 {
	if value == nil {
		return 0
	}

	return *value
}
