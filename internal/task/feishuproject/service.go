package feishuproject

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	sdk "github.com/larksuite/project-oapi-sdk-golang"
	"github.com/larksuite/project-oapi-sdk-golang/v2/service/workitem"
)

// Service is the entry point for pulling stories and running the watch loop.
// It owns the SDK client and the resolved configuration; callers should
// construct one per command invocation.
type Service struct {
	cfg    Config
	client *sdk.ClientV2
}

// NewService builds a Service from a validated Config. The SDK client is
// constructed internally so callers don't need to know SDK types.
func NewService(cfg Config) *Service {
	return &Service{cfg: cfg, client: NewClient(cfg)}
}

// PullResult bundles a single Pull outcome.
//
// WorkflowErrors lists per-story workflow fetch failures. The story slice
// itself is always returned with the items that did fetch successfully;
// stories without workflow data simply have Story.Workflow == nil. CLI and
// other callers can choose to surface or ignore these warnings.
type PullResult struct {
	Stories        []Story
	Diff           Diff
	State          State
	Snapshot       Snapshot
	WorkflowErrors []error
}

// Pull fetches the current story set from the configured project, compares
// it against the on-disk state, and returns the result. The caller decides
// whether to persist the new state (typically yes for watch, optional for
// one-shot pull).
func (s *Service) Pull(ctx context.Context) (PullResult, error) {
	if err := s.cfg.Validate(); err != nil {
		return PullResult{}, err
	}

	typeCtx, err := s.resolveTypeContext(ctx)
	if err != nil {
		return PullResult{}, err
	}

	items, err := s.fetchWorkItems(ctx, typeCtx)
	if err != nil {
		return PullResult{}, err
	}

	stories := mapStories(items, typeCtx, s.cfg.ProjectKey)
	applyRepoPaths(stories, s.cfg.Repos)
	workflowErrs := s.attachWorkflows(ctx, stories)

	prev, err := LoadState(s.cfg.Poll.StateFile)
	if err != nil {
		return PullResult{}, err
	}

	now := time.Now().UTC()
	diff, next := BuildDiff(prev, stories, now)
	snapshot := Snapshot{
		GeneratedAt: now,
		ProjectKey:  s.cfg.ProjectKey,
		Count:       len(stories),
		Diff:        diff,
		Stories:     stories,
	}

	return PullResult{
		Stories:        stories,
		Diff:           diff,
		State:          next,
		Snapshot:       snapshot,
		WorkflowErrors: workflowErrs,
	}, nil
}

// Persist writes both the snapshot file and the new state file. Splitting
// this out of Pull lets dry-run callers inspect the result without touching
// disk.
func (s *Service) Persist(result PullResult) error {
	if err := WriteSnapshot(s.cfg.Poll.OutputFile, result.Snapshot); err != nil {
		return err
	}
	if err := SaveState(s.cfg.Poll.StateFile, result.State); err != nil {
		return err
	}

	return nil
}

// Watch runs Pull on a ticker until the context is cancelled. It performs
// an immediate pull before the first tick so callers see results without
// waiting one interval.
func (s *Service) Watch(ctx context.Context, observer WatchObserver) error {
	if observer == nil {
		observer = noopObserver{}
	}

	if err := s.tick(ctx, observer); err != nil {
		return err
	}

	ticker := time.NewTicker(s.cfg.Poll.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := s.tick(ctx, observer); err != nil {
				observer.OnError(err)
			}
		}
	}
}

func (s *Service) tick(ctx context.Context, observer WatchObserver) error {
	result, err := s.Pull(ctx)
	if err != nil {
		return err
	}
	if err := s.Persist(result); err != nil {
		return err
	}
	observer.OnTick(result)

	return nil
}

// WatchObserver receives Watch lifecycle events. The CLI implementation
// renders human-readable summaries; tests can supply silent stubs.
type WatchObserver interface {
	OnTick(result PullResult)
	OnError(err error)
}

type noopObserver struct{}

func (noopObserver) OnTick(PullResult) {}
func (noopObserver) OnError(error)     {}

// FuncObserver adapts plain functions to the WatchObserver interface so the
// CLI layer can wire io.Writer-based renderers without a custom type.
type FuncObserver struct {
	Tick  func(PullResult)
	Error func(error)
}

func (f FuncObserver) OnTick(result PullResult) {
	if f.Tick != nil {
		f.Tick(result)
	}
}

func (f FuncObserver) OnError(err error) {
	if f.Error != nil {
		f.Error(err)
	}
}

// WriteSummary prints a compact human-readable summary suitable for stdout.
// The CLI calls this from both pull and watch flows.
func WriteSummary(out io.Writer, result PullResult) {
	fmt.Fprintf(out,
		"feishu pull: %d stories  added=%d updated=%d removed=%d  at=%s\n",
		len(result.Stories),
		len(result.Diff.Added),
		len(result.Diff.Updated),
		len(result.Diff.Removed),
		result.Snapshot.GeneratedAt.Format(time.RFC3339),
	)
}

func (s *Service) resolveTypeContext(ctx context.Context) (typeContext, error) {
	req := workitem.NewQueryProjectFieldsReqBuilder().
		ProjectKey(s.cfg.ProjectKey).
		WorkItemTypeKey(s.cfg.WorkItemTypeKey).
		Build()

	resp, err := s.client.WorkItem.QueryProjectFields(ctx, req, requestOptions(s.cfg)...)
	if err != nil {
		return typeContext{}, fmt.Errorf("feishuproject: query project fields: %w", err)
	}
	if !resp.Success() {
		return typeContext{}, fmt.Errorf(
			"feishuproject: query project fields failed: code=%d request_id=%s msg=%s",
			resp.Code(), resp.RequestId(), resp.ErrMsg,
		)
	}

	return detectTypeContext(s.cfg.WorkItemTypeKey, resp.Data), nil
}

func (s *Service) fetchWorkItems(ctx context.Context, typeCtx typeContext) ([]workitem.WorkItemInfo, error) {
	pageNum := int64(1)
	needUserDetail := true
	needMultiText := true
	items := make([]workitem.WorkItemInfo, 0)

	for {
		req := workitem.NewFilterReqBuilder().
			ProjectKey(s.cfg.ProjectKey).
			WorkItemTypeKeys([]string{typeCtx.TypeKey}).
			PageNum(pageNum).
			PageSize(s.cfg.PageSize).
			Expand(&workitem.Expand{
				NeedUserDetail: &needUserDetail,
				NeedMultiText:  &needMultiText,
			}).
			Build()

		resp, err := s.client.WorkItem.Filter(ctx, req, requestOptions(s.cfg)...)
		if err != nil {
			return nil, fmt.Errorf("feishuproject: filter work items page %d: %w", pageNum, err)
		}
		if !resp.Success() {
			return nil, fmt.Errorf(
				"feishuproject: filter work items failed: code=%d request_id=%s msg=%s",
				resp.Code(), resp.RequestId(), resp.ErrMsg,
			)
		}

		items = append(items, resp.Data...)
		if !morePagesAvailable(resp, len(items), len(resp.Data), s.cfg.PageSize) {
			return items, nil
		}
		pageNum++
	}
}

// morePagesAvailable centralises the pagination-stop logic so the loop in
// fetchWorkItems stays readable. The SDK may or may not populate Pagination
// depending on the project, so both paths are handled.
func morePagesAvailable(resp *workitem.FilterResp, accumulated, page int, pageSize int64) bool {
	if page == 0 {
		return false
	}
	if resp.Pagination == nil || resp.Pagination.Total == nil {
		return int64(page) >= pageSize
	}

	return int64(accumulated) < *resp.Pagination.Total
}

func mapStories(items []workitem.WorkItemInfo, typeCtx typeContext, projectKey string) []Story {
	stories := make([]Story, 0, len(items))
	for _, item := range items {
		stories = append(stories, mapStory(item, typeCtx, projectKey))
	}

	return stories
}

// applyRepoPaths fills Story.RepoPath in-place when a story's Repo label
// matches an entry in the configured [repos] table. Mutates the slice so
// callers don't have to re-bind. RepoPath is derived data and is left out
// of Story.Fingerprint by design.
func applyRepoPaths(stories []Story, repos map[string]string) {
	if len(repos) == 0 {
		return
	}
	for index := range stories {
		path, ok := repos[stories[index].Repo]
		if !ok {
			continue
		}
		stories[index].RepoPath = path
	}
}

func mapStory(item workitem.WorkItemInfo, typeCtx typeContext, projectKey string) Story {
	story := Story{
		ID:              derefInt64(item.ID),
		Name:            derefString(item.Name),
		WorkItemTypeKey: firstNonEmpty(derefString(item.WorkItemTypeKey), typeCtx.TypeKey),
		ProjectKey:      firstNonEmpty(derefString(item.ProjectKey), projectKey),
		Status:          extractStatus(item),
		Priority:        extractPriority(item),
		Owners:          extractOwnerNames(item, typeCtx.OwnerFieldKey),
		Creator:         extractCreatorName(item),
		BusinessLineID:  extractBusinessID(item),
		RequirementType: extractFirstString(item, typeCtx.RequirementTypeKey),
		Repo:            extractFirstString(item, typeCtx.RepoFieldKey),
		Description:     extractDescription(item),
		CreatedAt:       derefInt64(item.CreatedAt),
		UpdatedAt:       derefInt64(item.UpdatedAt),
	}
	if effort, ok := extractNumber(item, typeCtx.PlannedEffortKey); ok {
		story.PlannedEffort = &effort
	}
	if point, ok := extractNumber(item, typeCtx.EstimatePointKey); ok {
		story.StoryPoint = &point
	}

	return story
}

// ErrConfigMissing is returned when no -c value is supplied and no default
// path exists. The CLI surfaces this as a friendly message.
var ErrConfigMissing = errors.New("feishuproject: --config path is required")
