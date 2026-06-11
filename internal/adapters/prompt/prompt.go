package prompt

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// taskPreviewLimit and stringPreviewLimit cap how many entries are rendered in
// each task/string preview block, matching AgentLoop's BuildPrompt call sites.
const (
	taskPreviewLimit   = 10
	stringPreviewLimit = 20
	defaultBugDir      = ".agent/toolchain-bugs"
)

// TaskRef is one task fed to the agent. ID is a stable identifier (a workflow
// step id in Cogito); Text is the task body (the agent step prompt). Cogito has
// no AgentLoop-style todo file, so the task body is supplied inline here while
// the surrounding prompt wording is ported verbatim (plan OQ #5).
type TaskRef struct {
	ID   string
	Text string
}

// PromptInput carries the substitution values shared by every prompt template.
type PromptInput struct {
	// Root is the project root the agent operates on.
	Root string
	// Tasks are the runnable tasks for this invocation.
	Tasks []TaskRef
	// FailedTasks are tasks already exhausted (shown as blocked context).
	FailedTasks []TaskRef
	// BugDir overrides the toolchain bug report directory; empty uses the
	// AgentLoop default of .agent/toolchain-bugs.
	BugDir string
}

// mainPromptTemplate is a verbatim port of AgentLoop internal/prompt/
// main_prompt.go::mainPromptTemplate. Placeholders are substituted via
// strings.NewReplacer. The JSON example uses literal braces ({...}).
const mainPromptTemplate = `
你是一个代码执行 Agent。

这次要处理的项目根目录：
{project_root}

这次要处理的 todo 文件：
{todo_path}

当前目录是否为 git 仓库：
{git_repo_note}

目标：
优先恢复 todo 中已有的 ` + "`[~]`" + ` 任务，再处理新的 ` + "`[ ]`" + ` 任务；在保证质量、验证和提交纪律的前提下，本次运行要尽量多完成上面列出的可推进任务，而不是只做 1 个就停。

当前任务数：{len(runnable_tasks)}
当前因失败次数达到上限而暂时跳过的任务数：{len(exhausted_tasks)}

本轮可推进任务（按优先级顺序展示）：
{current_task}

当前已达到失败上限、需要暂时跳过的任务：
{blocked_preview}

要求：
1. 直接读取并更新 ` + "`{todo_path}`" + `，把它当作任务真实来源。
2. 以上任务按这个优先级顺序推进：先处理 ` + "`[~]`" + ` 恢复任务，再处理新的 ` + "`[ ]`" + ` 任务；同类任务尽量保持 todo 中的原始顺序。
3. 这批任务要尽量在同一个 codex 会话里连续推进；当前一个任务真正完成后，继续在当前会话里推进后面的可推进任务，不要为了切换到下一个任务而主动结束并重开会话。
4. 当你准备开始处理某个新的 ` + "`[ ]`" + ` 任务时，先立即把它在 todo 中改成 ` + "`[~]`" + `，确认它进入进行中状态后再开始写测试、实现或验证；如果同一会话里切到下一个新的 ` + "`[ ]`" + ` 任务，要改成 ` + "`[x]`" + `。
5. 不要只做第一个任务就停。当前一个任务真正完成后，继续推进后面的可推进任务；如果某个任务被明确阻塞，保持它为 ` + "`[~]`" + ` 并写进 ` + "`blocked`" + `，然后继续尝试后面彼此独立、不会被它直接破坏的任务。
6. 如果后续任务显然依赖前一个未完成任务，先不要硬做依赖项；只有在确认后续任务独立可推进时，才继续处理它。
7. 对代码实现类任务强制执行 TDD：先写或补最小 failing test，再运行它确认失败，然后实现最小改动让它通过，最后做必要的重构和回归。
8. 优先复用项目里已有的测试、构建、lint、benchmark 入口；如果缺少测试基座而某个 todo 明确要求补齐，就先补最小可运行的项目级测试基座再继续。
9. 不允许作弊或偷工减料：不要用 stub、假实现、只对当前测试或 fixture 生效的硬编码、跳过断言/验证、注释掉失败路径、吞掉错误、伪造结果、伪造提交，或任何“看起来完成但实际没实现”的手段来冒充完成。
10. 最终交付必须是完整实现，不是“最小能过测试”的半成品。red/green 阶段可以先做最小改动定位问题，但结束前必须补齐必要的边界处理、错误处理、兼容性、集成逻辑和验收标准要求。
11. 必须考虑代码性能和资源开销：选择合理的数据结构与算法，避免明显的重复扫描、重复分配、不必要拷贝、无界缓存增长或其他可预见性能退化；如果当前方案有明显性能风险，先修正设计再提交。
12. 如果某个任务过大，宁可拆分复杂任务也不要提交半成品；可以在 todo 中把剩余工作拆成紧跟当前任务的更具体后续 ` + "`[ ]`" + ` 子任务，并在总结中说明拆分方案，但不得把部分实现冒充为完成。
13. 只有在某个任务真正完成并且相关验证通过后，才把对应项改成 ` + "`[x]`" + `，并且保留原任务文本，不要改写任务描述。
14. 对暂时做不完的任务保持 ` + "`[~]`" + `，并在总结中说明阻塞原因；但不要因为一个阻塞任务就放弃本轮其余可独立完成的任务。
15. 修改代码后必须运行相关测试、构建或最小必要验证，并且这些验证必须实际通过；只有验证通过后才允许创建提交。输出里要说明执行了哪些验证，以及 TDD 的 red/green 分别用了什么命令。如果某个验证只是 skip、mock、空跑或未覆盖真实行为，它不足以证明任务完成，你必须补充更真实的验证。
16. 如果怀疑遇到了项目工具链 bug（例如编译器、解释器、构建系统、测试运行器、包管理器本身的问题），必须在继续前把 bug 记录下来：
   - 先把问题缩减成最小可复现代码或最小可复现输入。
   - 把复现文件写到 ` + "`{toolchain_bug_repro_dir_label}/`" + ` 下，文件名用时间戳加短描述，扩展名按项目实际类型决定。
   - 把 bug 报告写到 ` + "`{toolchain_bug_dir_label}/`" + ` 下，文件名与 repro 对应，后缀 ` + "`.md`" + `。
   - 报告必须包含以下一级小节，标题要完全一致：
     ` + "`## Summary`" + `
     ` + "`## Affected Tasks`" + `
     ` + "`## Toolchain Command`" + `
     ` + "`## Actual Error`" + `
     ` + "`## Expected Behavior`" + `
     ` + "`## Repro File`" + `
     ` + "`## Repro Code`" + `
     ` + "`## Notes`" + `
   - ` + "`## Repro File`" + ` 小节下一行只放 repro 文件路径，并用反引号包起来。
   - ` + "`## Repro Code`" + ` 小节必须包含一个 fenced code block，内容要和 repro 文件内容一致；语言标记可按文件类型填写，也可以留空。
   - 被该工具链 bug 阻塞的任务必须留在 ` + "`[~]`" + `，并写进 ` + "`blocked`" + `。
17. 如果当前目录是 git 仓库，并且你在本轮完成了某个任务或新增了该任务相关的工具链 bug 记录，你必须在本轮结束前立即自己创建提交；不要依赖外层脚本代为提交，也不要把已勾选但未提交的任务留给下一轮。
18. 提交要求：
   - 可以在一轮里创建多个普通提交，但每个 todo 任务最多对应一个提交；不要把多个 todo 任务合并到同一次提交。
   - 提交消息要自然、简洁，能概括当前任务的真实改动，不要使用机械化模板。
   - 每次只 stage 当前要提交的那个任务直接相关的文件；如果工作区里有无关脏改动，不要把它们一起提交。
   - 不要 amend 既有提交。
19. 如果项目不是 git 仓库，也继续完成任务，并把 ` + "`commits`" + ` 留空数组。
20. 不要回退用户已有改动，不要执行 destructive git 操作，也不要扩大到和这个 todo 无关的工作。
21. 结束前必须输出一行严格单行结果，要求：
    - 这一行必须以 ` + "`{RESULT_PREFIX}`" + ` 开头
    - 前缀后面紧跟一个单行 JSON
    - JSON 示例：{"completed":["任务1"],"blocked":["任务2"],"deferred":["任务3"],"toolchain_bugs":[".agent/toolchain-bugs/bug-report.md"],"commits":["abc1234"],"verification":["命令1"],"summary":"一句话总结"}

字段说明：
- completed：填写本次真正完成、并且已经在 todo 文件中勾选的任务文本，可以有多个
- blocked：填写本次尝试过但暂时无法完成的任务文本，可以有多个
- deferred：默认使用空数组；不要把没有实际尝试的后续任务写进这里
- toolchain_bugs：本次新增或更新的任务相关工具链 bug 报告 markdown 路径
- commits：本次新创建的 git commit 哈希，短哈希或长哈希都可以；非 git 项目时用空数组
- verification：本次在提交前实际运行且通过的验证命令，按执行顺序填写；如果 ` + "`completed`" + ` 非空，这里必须至少有一条
- 没有内容时使用空数组
`

// commitRecoveryTemplate is a verbatim port of AgentLoop internal/prompt/
// commit_recovery_prompt.go::commitRecoveryTemplate.
const commitRecoveryTemplate = `
你是一个代码执行 Agent。

上一个批次已经完成了部分工作，但没有创建 git commit。
这次不要继续做新的 todo 任务；只处理“补提交”收尾。

项目根目录：
{project_root}

todo 文件：
{todo_path}

待补提交的已完成任务：
{task_preview}

待补提交的 toolchain bug 报告：
{bug_preview}

这是第 {attempt_count} 次补提交尝试。

要求：
1. 先检查当前 git 状态和最近提交，确认哪些未提交改动属于上述任务或 bug 报告。
2. 按列表顺序处理这些待补提交任务；对每个任务先重新运行它直接相关的验证命令，确认通过后，再只 stage 相关文件并立即创建一个清晰、自然的普通提交。
3. 如果只有 toolchain bug 报告待提交，也要先验证 repro 和报告内容仍然匹配，再单独提交相关文件。
4. 不要为了补提交而作弊或掩盖未完成工作：如果现有改动仍是半成品、靠硬编码或跳过验证过关、存在明显性能问题，先继续修正或恢复 todo 状态，不要创建“表面完成”的提交。
5. 不要继续实现新的 todo 任务，不要扩大工作范围，不要把多个 todo 任务合并进同一个补提交，也不要把无关脏改动一起提交。
6. 如果你发现上一个批次把 todo 勾选早了，先把错误勾选恢复成 ` + "`[~]`" + ` 或修正相关文件，再把这次修正提交掉。
7. 结束前必须输出一行严格单行结果，要求：
   - 这一行必须以 ` + "`{RESULT_PREFIX}`" + ` 开头
   - 前缀后面紧跟一个单行 JSON
   - JSON 示例：{"completed":[],"blocked":[],"deferred":[],"toolchain_bugs":[],"commits":["abc1234"],"verification":["命令1"],"summary":"一句话总结"}

字段说明：
- ` + "`completed`" + `：默认使用空数组，除非这次顺手修正了 todo 状态并重新确认完成
- ` + "`blocked`" + `：如果因为明确原因暂时无法完成补提交，写原因对应的任务文本
- ` + "`deferred`" + `：默认使用空数组
- ` + "`toolchain_bugs`" + `：默认使用空数组；只有这次新增或更新了 bug 报告时才填写
- ` + "`commits`" + `：这次新创建的 git commit 哈希，短哈希或长哈希都可以
- ` + "`verification`" + `：补提交前重新运行且通过的验证命令；如果 ` + "`commits`" + ` 非空且 ` + "`tasks`" + ` 非空，这里必须至少有一条
`

// dirtyWorktreeTemplate is a verbatim port of AgentLoop internal/prompt/
// dirty_worktree_prompt.go::dirtyWorktreeTemplate.
const dirtyWorktreeTemplate = `
你是一个代码执行 Agent。

当前任务的迭代被中断后，仓库里留下了未提交改动。
这次不是新的工作流，也不是额外的预处理；你是在继续“当前 todo 任务”的同一轮迭代，目标是保证当前任务验证通过并提交，然后才允许进入后续任务。

项目根目录：
{project_root}

todo 文件：
{todo_path}

当前待恢复任务：
{current_task}

当前阻塞并停止后续推进的任务：
{blocked_preview}

当前 git 脏改动：
{dirty_preview}

要求：
1. 先检查当前 git 状态、todo 勾选和未提交文件，确认这些现有改动与上面这批任务的关系。
2. 绝对不要开始新的 todo 任务；只允许围绕上面的这批待恢复任务继续收尾、补验证、补实现或补提交。
3. 不允许作弊或表面修补：不要靠硬编码、临时 stub、跳过验证、关闭断言、伪造结果或其他取巧手段让当前任务“看起来通过”。
4. 当前改动必须收敛到完整实现而不是“刚好能过”的半成品；如发现任务过大，宁可保持 ` + "`[~]`" + ` 并拆分剩余工作，也不要提交未完整实现的结果。
5. 如果你在同一轮恢复里继续推进到后面仍是 ` + "`[ ]`" + ` 的新任务，先把那一项改成 ` + "`[~]`" + `，确认进入进行中状态后再开始写测试、实现或验证。
6. 先重新运行当前任务直接相关的验证命令；如果验证失败，继续修改当前任务相关内容直到通过，或明确判定当前任务阻塞。
7. 必须考虑代码性能和资源开销；如果当前未提交实现有明显性能缺陷或可预见退化，先修正再提交。
8. 尽量把这批任务里能完成的任务都完成；某个任务确认阻塞时，保持它为 ` + "`[~]`" + ` 并写进 ` + "`blocked`" + `，然后继续处理后面独立可推进的任务。
9. 只有在某个任务验证通过后，才允许创建该任务对应的提交；如果一轮里补完了多个任务，可以创建多个提交，但不要把多个 todo 任务合并进同一个提交。
10. 如果某个任务还没真正完成，就不要为了“清理工作区”而做部分提交；保持它是当前任务的一部分继续迭代。
11. 如果验证通过，只 stage 当前正在提交的那个任务直接相关的现有文件，创建一个清晰、自然的普通提交。
12. 不要把当前任务和后续任务合并到同一个提交，也不要引入新的无关修改。
13. 不要回退用户已有改动，不要执行 destructive git 操作。
14. 结束前必须输出一行严格单行结果，要求：
   - 这一行必须以 ` + "`{RESULT_PREFIX}`" + ` 开头
   - 前缀后面紧跟一个单行 JSON
   - JSON 示例：{"completed":[],"blocked":["任务文本"],"deferred":[],"toolchain_bugs":[],"commits":["abc1234"],"verification":["命令1"],"summary":"一句话总结"}

字段说明：
- ` + "`completed`" + `：如果你让某些任务真正完成并在 todo 中勾选，填写这些任务文本；否则用空数组
- ` + "`blocked`" + `：如果某些任务仍然无法通过验证或暂时不能完成，填写这些任务文本或阻塞说明
- ` + "`deferred`" + `：默认使用空数组
- ` + "`toolchain_bugs`" + `：只有这次新增或更新了 bug 报告时才填写
- ` + "`commits`" + `：这次为这些任务创建的新提交哈希
- ` + "`verification`" + `：这些任务提交前实际运行且通过的验证命令；如果 ` + "`completed`" + ` 或 ` + "`commits`" + ` 非空，这里必须至少有一条
`

// BuildMain renders the primary task prompt for an agent invocation. The
// template prose is a verbatim AgentLoop port; Cogito-specific values fill the
// placeholders (Root, inline tasks, and bug-report directories).
func BuildMain(input PromptInput) string {
	root := rootLabel(input.Root)
	bugDir, reproDir := toolchainBugDirs(input.BugDir)

	replacer := strings.NewReplacer(
		"{project_root}", root,
		"{todo_path}", root,
		"{git_repo_note}", gitRepoNote(input.Root),
		"{len(runnable_tasks)}", strconv.Itoa(len(input.Tasks)),
		"{len(exhausted_tasks)}", strconv.Itoa(len(input.FailedTasks)),
		"{current_task}", summarizeTasks(input.Tasks, taskPreviewLimit),
		"{blocked_preview}", summarizeTasks(input.FailedTasks, taskPreviewLimit),
		"{toolchain_bug_repro_dir_label}", reproDir,
		"{toolchain_bug_dir_label}", bugDir,
		"{RESULT_PREFIX}", ResultPrefix,
	)

	return replacer.Replace(mainPromptTemplate)
}

// BuildCommitRecovery renders the commit-recovery prompt. missingCommits lists
// the commit references the previous batch claimed but did not produce; they
// render in the outstanding-reports section using the verbatim template wording
// (plan OQ #5). This template is wired in L5; L1 only ports it.
func BuildCommitRecovery(input PromptInput, missingCommits []string) string {
	root := rootLabel(input.Root)

	replacer := strings.NewReplacer(
		"{project_root}", root,
		"{todo_path}", root,
		"{task_preview}", summarizeTasks(input.Tasks, stringPreviewLimit),
		"{bug_preview}", summarizeStrings(missingCommits, stringPreviewLimit),
		"{attempt_count}", "1",
		"{RESULT_PREFIX}", ResultPrefix,
	)

	return replacer.Replace(commitRecoveryTemplate)
}

// BuildDirtyWorktree renders the dirty-worktree recovery prompt. Cogito does not
// thread live worktree status at build time, so the dirty preview defaults to
// "无"; this template is wired in L5.
func BuildDirtyWorktree(input PromptInput) string {
	root := rootLabel(input.Root)

	replacer := strings.NewReplacer(
		"{project_root}", root,
		"{todo_path}", root,
		"{current_task}", summarizeTasks(input.Tasks, taskPreviewLimit),
		"{blocked_preview}", summarizeTasks(input.FailedTasks, taskPreviewLimit),
		"{dirty_preview}", summarizeStrings(nil, stringPreviewLimit),
		"{RESULT_PREFIX}", ResultPrefix,
	)

	return replacer.Replace(dirtyWorktreeTemplate)
}

func rootLabel(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		return "."
	}

	return root
}

func gitRepoNote(root string) string {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "."
	}

	if _, err := os.Stat(filepath.Join(root, ".git")); err == nil {
		return "是"
	}

	return "否"
}

func toolchainBugDirs(bugDir string) (string, string) {
	bugDir = strings.TrimSpace(bugDir)
	if bugDir == "" {
		bugDir = defaultBugDir
	}

	return bugDir, path.Join(bugDir, "repros")
}

// summarizeTasks renders a numbered list of task texts, "无" when empty, with an
// overflow line past limit. Ported from AgentLoop prompt.SummarizeTasks.
func summarizeTasks(tasks []TaskRef, limit int) string {
	if len(tasks) == 0 {
		return "无"
	}

	lines := make([]string, 0, len(tasks))

	for index, task := range tasks {
		if index >= limit {
			break
		}

		lines = append(lines, fmt.Sprintf("%d. %s", index+1, task.Text))
	}

	if len(tasks) > limit {
		lines = append(lines, fmt.Sprintf("... 还有 %d 个任务", len(tasks)-limit))
	}

	return strings.Join(lines, "\n")
}

// summarizeStrings renders a numbered list of strings, "无" when empty, with an
// overflow line past limit. Ported from AgentLoop prompt.SummarizeStrings.
func summarizeStrings(items []string, limit int) string {
	if len(items) == 0 {
		return "无"
	}

	lines := make([]string, 0, len(items))

	for index, item := range items {
		if index >= limit {
			break
		}

		lines = append(lines, fmt.Sprintf("%d. %s", index+1, item))
	}

	if len(items) > limit {
		lines = append(lines, fmt.Sprintf("... 还有 %d 项", len(items)-limit))
	}

	return strings.Join(lines, "\n")
}
