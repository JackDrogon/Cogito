# 审批门禁系统 (Approval Gate System)

审批门禁系统为 Cogito 提供“人机协同” (Human-in-the-loop) 的能力。它允许工作流在关键节点暂停，等待外部信号（如人工干预或策略评估）后再继续执行。

## 概述

审批门禁不仅仅是一个简单的“暂停”指令，它是一个完整的状态管理子系统，负责审批请求的生命周期、上下文持久化以及与不同触发源的交互。

## 触发类型 (Trigger Types)

系统支持三种不同的审批触发模式：

### 1. 显式触发 (Explicit)
通过在工作流 YAML 中定义 `kind: approval` 的步骤来显式声明审批门禁。这是最常见的用法，用于在关键步骤（如生产部署）前强制人工介入。

```yaml
- id: prod-deploy-gate
  kind: approval
  message: "Confirm production deployment"
```

### 2. 适配器触发 (Adapter)
由执行适配器（如 Agent 或外部集成）在运行过程中动态触发。当适配器返回 `ExecutionStateWaitingApproval` 状态时，运行时会自动挂起该步骤并进入审批流程。

### 3. 策略触发 (Policy)
通过 `ApprovalPolicy` 强制执行的审批。即使工作流本身没有定义审批步骤，全局策略也可以根据运行时快照（如高风险操作）注入异常审批请求。

## 决策流 (Decision Flow)

一旦进入审批状态，工作流会处于 `waiting_approval` 运行状态。系统支持以下决策：

- **批准 (Approve)**: 工作流恢复执行，该步骤标记为成功或继续其后续流程。
- **拒绝 (Deny)**: 该步骤失败，整个工作流进入失败状态并停止后续执行。
- **超时 (Timeout)**: 在规定时间内未收到决策时自动触发，通常视为拒绝处理。

## 持久化 (Persistence)

审批状态必须在进程重启后能够恢复，Cogito 通过以下方式确保一致性：

### Checkpoint 持久化
每个步骤的 `StepCheckpoint` 包含以下字段：
- `approval_id`: 本次审批请求的唯一标识。
- `approval_trigger`: 触发来源（explicit/adapter/policy）。
- `summary`: 审批请求的描述文字。

### 事件溯源 (Events)
审批生命周期产生以下关键事件：
- `ApprovalRequested`: 记录审批请求的生成及其触发上下文。
- `ApprovalGranted`: 记录批准决定及相关备注。
- `ApprovalDenied`: 记录拒绝原因。
- `ApprovalTimedOut`: 记录超时发生。

## 实现细节

### 状态机转换
运行时的状态机定义了严格的审批转换路径：
- `RunStateRunning` -> `RunStateWaitingApproval`
- `RunStateWaitingApproval` -> `RunStateRunning` (批准)
- `RunStateWaitingApproval` -> `RunStateFailed` (拒绝/超时)

### 引擎交互
`Engine` 提供了 `GrantApproval`、`DenyApproval` 和 `TimeoutApproval` 接口供 CLI 或 API 调用。这些调用会触发事件追加，并通过 `Replay` 机制恢复执行上下文。

## 示例

以下是一个典型的包含审批门禁的部署工作流片段：

```yaml
apiVersion: cogito/v1alpha1
kind: Workflow
metadata:
  name: secured-deploy
steps:
  - id: build
    kind: command
    command: "go build ."
    
  - id: approve-test-deploy
    kind: approval
    needs: ["build"]
    message: "Approve deployment to test environment"
    
  - id: deploy-test
    kind: agent
    needs: ["approve-test-deploy"]
    agent: "deployer"
    prompt: "Deploy built artifacts to testing"
```

审批门禁系统不仅能暂停流程，还能保存执行上下文，使得工作流可以在不同的会话甚至不同的机器上无缝恢复。这对于长时间运行的部署或需要跨时区审批的任务至关重要。

### 状态转换矩阵

| 源状态 | 事件 | 目标状态 | 说明 |
| :--- | :--- | :--- | :--- |
| Running | EventRunWaitingApproval | WaitingApproval | 进入审批挂起 |
| WaitingApproval | EventApprovalGranted | Running | 审批通过，恢复执行 |
| WaitingApproval | EventApprovalDenied | Failed | 审批拒绝，停止执行 |
| WaitingApproval | EventApprovalTimedOut | Failed | 审批超时，自动失败 |

### 故障恢复机制

当引擎重新加载处于 `waiting_approval` 状态的 Checkpoint 时：
1. 它会根据 `approval_trigger` 重新构建审批处理器。
2. 恢复 `approval_id` 以便后续的 `Grant/Deny` 操作能匹配到正确的门禁。
3. 如果 trigger 是 `adapter`，它会调用适配器的 `PollOrCollect` 检查外部状态。
4. 如果 trigger 是 `policy`，它会重新评估策略，看是否仍然需要人工介入。

这种机制保证了审批系统不仅仅是一个标志位，而是一个有状态的、可恢复的任务节点。

## 事件数据结构示例

一个典型的 `ApprovalRequested` 事件数据如下：

```json
{
  "sequence": 42,
  "type": "ApprovalRequested",
  "step_id": "deploy-prod",
  "approval_id": "approval-deploy-prod-01",
  "data": {
    "approval_trigger": "explicit",
    "from_state": "running",
    "to_state": "waiting_approval",
    "summary": "Confirm production deployment",
    "occurred_at": "2026-03-22T10:00:00Z"
  }
}
```

## 开发者指南

实现新的审批策略或集成外部审批系统时，请遵循以下原则：
- **幂等性**: 审批决定应该是幂等的，多次发送相同的批准请求不应产生副作用。
- **透明度**: 所有决策必须记录在事件日志中，包括决策者的身份（如果可用）和决策理由。
- **原子性**: 审批状态的更新与工作流状态的转换必须是原子操作，通过 Checkpoint 机制保证。

## 总结

通过将审批逻辑从核心调度器中分离出来，Cogito 能够灵活地处理从简单的命令行确认到复杂的分布式策略评估等各种审批场景，同时保持了核心引擎的简洁和可测试性。
