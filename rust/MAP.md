# XShadow 后端迁移到 Rust

目标是让 Rust 服务承载 XShadow HTTP API、WebSocket、服务端调度和数据库迁移。现有桌面、Web、移动端和本机 daemon 通过原有协议访问服务。Rust 候选的运行边界和选型见 [架构决策](DECISIONS.md)。

## 当前交付

Rust 候选位于 [app/](app/)。它独立处理 `/health`、`/readyz` 和 `/healthz`，并在同一进程提供工作区通用表格。Go 承载公开 API 和认证，将通过成员校验的表格请求转到本机 Rust。Go 健康检查参考版本为 `883566da7e2d8d256b4c775feec2be875a827d00`。候选仅监听 IPv4 回环地址。日常工作台按已发布版本启动 Go 服务。

原生任务详情 `GET /api/issues/{id}` 支持按工作区试点。设置 `XSHADOW_ISSUES_RUST_READ_PILOT` 为工作区 UUID 或 slug 后，Rust 在同一 PostgreSQL 中读取原任务并检查项目可见性，Go 补齐附件 URL 和冻结来源。连接可复用 `XSHADOW_TABLES_UPSTREAM` 与 `XSHADOW_TABLES_INTERNAL_TOKEN`。清空试点设置后，详情回到 Go。试点期间 Rust 不可用时，选定工作区收到 `503`，其他工作区继续经由 Go 提供详情。

原生任务列表 `GET /api/issues` 与 `POST /api/issues/query` 可用 `XSHADOW_ISSUES_RUST_LIST_PILOT` 独立选择工作区。Go 验证请求参数，Rust 筛选任务、检查可见范围、查询分页和总数。该设置只作用于这两个列表入口。清空后，列表回到 Go。

`XSHADOW_ISSUES_RUST_TABLE_PILOT` 独立指定表格行、分组标题和筛选统计的试点工作区。Go 校验公开请求、解析目录与智能体可见范围，并负责编码游标和分组描述。Rust 在每个请求的只读快照内检查项目可见范围并查询行、子任务、分组和各筛选项的计数。筛选统计按需忽略该项自身的平铺条件，保留高级筛选。清空试点设置后，三个入口回到 Go。

`XSHADOW_ISSUES_RUST_WRITE_PILOT` 独立指定普通编辑试点工作区，支持 `PUT /api/issues/{id}` 的标题、描述、附件关联、优先级、排序位置、开始日期和截止日期。Go 验证公开请求并发布原任务事件。Rust 在锁定任务的事务内检查项目可见范围、核对修订、合并渠道媒体、计算待开始的工作方法推荐并写入。混合其他字段的请求继续由 Go 执行。试点上游不可用时，已选中的写入返回 `503`。清空设置后，普通编辑由 Go 执行。

同一试点设置也覆盖 `PUT`、`DELETE /api/issues/{id}/properties/{propertyId}`。Go 规范化字段值并检查引用，Rust 通过任务行锁和属性定义的共享事务锁执行单字段更新。定义发生变化时，网关只在收到明确未写入的回执后重新读取定义并校验。清除必填字段的检查依据锁定后的任务类型。

任务元数据的 `PUT`、`DELETE /api/issues/{id}/metadata/{key}` 复用该试点。Rust 在任务锁内执行 50 键上限检查，沿用 8KB 存储上限和数字原值。真正发生变化时才增加修订并发出原任务事件。Go 继续校验公开请求、记录指标并发布实时事件。

`POST /api/issues/{id}/labels` 与 `DELETE /api/issues/{id}/labels/{labelId}` 纳入 `XSHADOW_ISSUES_RUST_WRITE_PILOT`。Go 校验公开请求。Go 和 Rust 都按标签目录行、任务行、关联表的顺序写入。标签目录删除在同一事务里先删除目录行，再清理关联。Rust 持锁复核任务可见范围并记录唤醒事件的来源。实际增删才推进 `revision` 和 `last_activity_at`，重复操作仍返回 200 且省略修订。

`XSHADOW_ISSUES_RUST_ACCEPTANCE_PILOT` 为 `POST /api/issues/{id}/acceptance` 的普通验收记录写入指定试点工作区。Go 继续校验公开请求、计算验收记录、处理工作方法重开和派工，并发布 WebSocket 事件。Rust 在任务行锁内复核项目可见范围和任务修订，写入验收记录，返回提交后的任务快照。Go 验证回执的任务修订和验收记录再发事件。试点上游不可用返回 `503`，回执格式错误返回 `502`。

`XSHADOW_ISSUES_RUST_STATUS_PILOT` 为 `PUT /api/issues/{id}` 中带任务修订的内置活动状态和列内排序写入指定试点工作区。Rust 持锁复核任务可见范围和修订。状态真的变化时，Rust 同事务写入一条时间线记录，Go 收到回执后发布已有记录的实时事件。指派给直接智能体的积压任务晋升到内置活动状态时，Go 预计算 `CreateAgentTask` 归因与就绪数据，Rust 持锁复核后在同事务写入队列行，丢回执后仍只剩一条可轮询任务；军团晋升、工作流步骤推进、重复任务重开、自定义状态、终态及组合编辑仍走 Go。试点返回的 `409` 说明需要刷新后重试。上游不可用和无效回执分别返回 `503` 和 `502`。

`XSHADOW_ISSUES_RUST_TERMINAL_STATUS_PILOT` 为 `PUT /api/issues/{id}` 的内置 `done`/`cancelled` 指定试点工作区，须在同一工作区同时启用 `XSHADOW_ISSUES_RUST_STATUS_PILOT`。Rust 在同一事务保存状态和时间线、禁用普通订阅并取消尚未开始的唤醒或续跑。取消后记录客户端事件，有运行时绑定时同时记录运行时通知。客户端记录依赖迁移 729，适用于有绑定及已解绑的运行。Go 从正常回执和后台扫描共用处理逻辑，领取后在数据库事务之外发出原有 `task:cancelled`，事件总线返回后按领取凭证确认。进程中断或确认失败后可以在领取到期时重新发送。在线客户端使用现有实时通道，断开期间可按原重连流程读回状态。工作方法、入审、重复标记重开及组合字段仍走 Go。

`XSHADOW_ISSUES_RUST_DELETE_PILOT` 为 `DELETE /api/issues/{id}` 的普通成员删除指定试点工作区。Rust 依序锁定项目与任务，复查删除权限和项目可见性，在一个事务中解除直接子任务和重复关联、清理目标链接、VCS 和依赖关系并删除任务。选择后的任务形态变化返回 `409`。智能体创建、执行队列、自动驾驶与来源上下文继续使用原 Go 路径。

`XSHADOW_ISSUES_RUST_DELETE_ATTACHMENTS_PILOT` 须在同一工作区启用单任务删除试点。支持按既有存储规则可验证归属、直接绑定到任务的工作区上传。Rust 持锁核对完整附件清单，将任务删除、附件记录删除和对象清理任务一起提交。Go 从数据库领取清理任务，确认当前存储映射和文件归属再删除对象。正常回执和后台重试共用此逻辑，依赖迁移 730。回执不明时已提交的文件清理仍会继续。评论附件、来源文件及混合形态继续走原路径。

`XSHADOW_ISSUES_RUST_DELETE_SOURCE_CONTEXT_PILOT` 须在同一工作区启用单任务删除试点。首批覆盖无项目、仅有一条无文件手动附着来源上下文的普通成员任务。Go 从数据库读取权威上下文标识并作为可信计划下发。Rust 持锁复核项目形态、上下文归属、手动来源、`origin_task_id` 为空及无文件/意图后，在删除事务内消费精确匹配的来源上下文行。不触及来源任务、评论或其他上下文。批量、智能体来源、来源文件及混合形态继续走 Go。

`XSHADOW_ISSUES_RUST_DELETE_SOURCE_CONTEXT_FILES_PILOT` 须在同一工作区同时启用单任务删除与来源删除试点。覆盖手动附着来源上下文且冻结首方副本上传的普通成员任务。Go 一次性读取上下文与其副本行并下发不可变计划。Rust 持锁复核完整副本集合、无既有对象意图后，在解绑子任务后的删除事务内写入持久清理意图、删除副本与上下文并删除任务。Go 只根据已校验回执对可信副本 URL 做即时物理删除，失败仍由既有定时回收处理。直接上传、混合形态及非规范存储继续走 Go。

`XSHADOW_ISSUES_RUST_DELETE_SOURCE_PROJECT_PILOT` 须在同一工作区同时启用单任务删除与来源删除试点。覆盖继承可见项目的普通手动附着来源上下文任务；有副本时另需来源文件删除试点。Go 下发可信 `expected_project_id`，Rust 在既有项目/成员锁序下复核锁定项目与计划一致后再执行来源删除事务。项目移动、授权撤销或迟到隐私变化返回既有冲突。无项目来源继续仅依赖来源删除试点。

`XSHADOW_ISSUES_RUST_BATCH_DELETE_PILOT` 为 `POST /api/issues/batch-delete` 的普通成员批量删除独立指定试点工作区，与单任务删除试点无关。Go 先解析试点，再判断可见选中项是否全部为普通可删形态；任一项不适用则整批走 Go。Rust 在单事务内按确定性顺序持锁、复核权限与形态，再按与 Go 相同的排除规则解除子任务与重复关联、清理目标链接并删除各行；已列入本批的子/重复任务不会被提前解绑。选定工作区在上游不可用或回执不明确时返回 `503`/`502`，不在失败后静默回退 Go。

`XSHADOW_ISSUES_RUST_BATCH_ASSIGN_PILOT` 为 `POST /api/issues/batch-update` 的普通成员批量指派至本人拥有的直接智能体独立指定试点工作区，且须与 `XSHADOW_ISSUES_RUST_ASSIGN_PILOT` 同时作用于同一工作区。Go 先校验仅含完整的 `assignee_type=agent` 与 `assignee_id` 对及可选 `suppress_run`，再按可见行逐条预计算队列归因，复用单任务 assign 事务写入与运行态通知外盒。隐藏/缺失、无权限、形态不适用或持锁冲突的行被跳过且不计数；传输或回执异常时中止后续行。标量批量试点、单任务 assign 试点与其他字段仍走各自既有路径。

`XSHADOW_ISSUES_RUST_BATCH_STATUS_PILOT` 为 `POST /api/issues/batch-update` 的普通成员内置活动状态批量改写指定试点工作区，须在同一工作区同时启用 `XSHADOW_ISSUES_RUST_STATUS_PILOT`。仅含 `status` 与可选 `suppress_run` 的请求可以在既有批量循环内按行复用单任务状态写入和时间线回执。需要实际派工的积压晋升、终态或自定义状态、命名工作流、交接、工作方法和组合字段继续使用原处理路径。隐藏、缺失、锁后冲突及确定回滚的行跳过。传输或提交回执不明时中止后续行，重试保留已提交行的原修订和时间线。

`XSHADOW_ISSUES_RUST_BATCH_TERMINAL_STATUS_PILOT` 为 `POST /api/issues/batch-update` 的普通成员内置 `done`/`cancelled` 批量修改指定试点工作区，须在同一工作区同时启用 `XSHADOW_ISSUES_RUST_STATUS_PILOT` 与 `XSHADOW_ISSUES_RUST_TERMINAL_STATUS_PILOT`。仅含 `status` 与可选 `suppress_run` 的请求按行复用单任务终态事务、取消回执和持久通知流程。入审、工作方法、重复标记、命名工作流和组合字段仍走 Go。隐藏、缺失、锁后冲突及确定回滚的行跳过。传输或提交回执不明时中止后续行，重试保留已提交行的修订和状态时间线。

`XSHADOW_ISSUES_RUST_BATCH_WORKFLOW_PILOT` 为 `POST /api/issues/batch-update` 的普通成员命名工作流军团交接指定试点工作区，须在同一工作区同时启用 `XSHADOW_ISSUES_RUST_STATUS_PILOT` 与 `XSHADOW_ISSUES_RUST_WORKFLOW_PILOT`。仅含 `status` 与可选 `suppress_run` 的请求在既有批量循环内复用单任务事务。调用者拥有在线运行时可用的军团负责人时，Rust 在同一事务中核对可见范围、流程、负责人和运行时，再保存状态、时间线和派发义务。默认工作流、终态或自定义状态、积压晋升、工作方法、组合字段及其他军团由 Go 处理。隐藏、缺失、锁后冲突及确定回滚的行跳过。传输或提交回执不明时中止后续行，重试保留已提交行的原修订与时间线。

`XSHADOW_ISSUES_RUST_CREATE_SOURCE_CONTEXT_PILOT` 为 `POST /api/comments/{commentId}/sub-issues` 的手动创建指定试点工作区，须与 `XSHADOW_ISSUES_RUST_CREATE_PILOT` 同时启用。首批覆盖无项目、无附件的来源和未指派的普通待办子任务。Go 核对预览令牌并准备原始快照。Rust 锁定来源任务，核对工作区、来源正文、前缀、选中评论及此前线程的内容和顺序，在事务内写入子任务和附着快照。智能体创建、指派及复杂任务配置由 Go 处理。

`XSHADOW_ISSUES_RUST_CREATE_SOURCE_PROJECT_PILOT` 须在同一工作区与 `XSHADOW_ISSUES_RUST_CREATE_PILOT` 及 `XSHADOW_ISSUES_RUST_CREATE_SOURCE_CONTEXT_PILOT` 同时启用。覆盖继承来源项目、使用默认工作流的普通待办子任务。Go 读取来源项目标识。Rust 在源任务锁之前核对并锁定项目和成员授权，再写入子任务和附着快照。显式目标项目、命名工作流、智能体快速创建及不适用默认工作流的配置由 Go 处理。

`XSHADOW_ISSUES_RUST_CREATE_SOURCE_FILES_PILOT` 须在同一工作区与 `XSHADOW_ISSUES_RUST_CREATE_PILOT` 及 `XSHADOW_ISSUES_RUST_CREATE_SOURCE_CONTEXT_PILOT` 同时启用。Go 流式复制文件并登记持久清理意图。Rust 核对原附件、写入克隆行并验证实际消费的意图集合。

`XSHADOW_ISSUES_RUST_CREATE_AGENT_SOURCE_PILOT` 为 `POST /api/comments/{commentId}/sub-issues` 中 `mode=agent` 指定试点工作区（默认关闭）。首批覆盖无额外 quick-create 上传、请求者拥有的本地直连智能体及已上线并声明来源快速创建能力的本机运行时。无文件时覆盖无项目来源（`CREATE_AGENT_SOURCE` 单独启用）或继承来源普通项目（另需 `CREATE_AGENT_SOURCE_PROJECT`）。有冻结来源附件时另需 `CREATE_AGENT_SOURCE_FILES`；继承项目且冻结附件须三者同启。Go 校验预览令牌、容量与来源项目可见性，流式复制并登记对象意图，准备队列归因及上下文，并返回 202。Rust 锁定工作区、成员、智能体、运行时与来源项目（有项目时先于来源任务），复核运行时版本及冻结内容，在同一事务写入队列任务、待附着快照、克隆附件行（有文件时消费意图集合）和持久唤醒记录。Go 核验回执、来源项目绑定及持久状态后投递唤醒。丢回执由同一持久队列恢复。旧 daemon 继续领取、创建并附着快照。显式目标项目、额外上传、军团、非本地或离线运行时和非本人智能体继续由 Go 处理。

`XSHADOW_ISSUES_RUST_CREATE_AGENT_SOURCE_PROJECT_PILOT` 须在同一工作区与 `XSHADOW_ISSUES_RUST_CREATE_AGENT_SOURCE_PILOT` 同时启用，默认关闭。覆盖继承可见来源项目的智能体入队；有冻结附件时另需 `CREATE_AGENT_SOURCE_FILES`。`expected_source_project_id` 与回执 `source_project_id` 绑定锁内项目；队列 QuickCreate JSON 仍不含显式 `project_id`。

`XSHADOW_ISSUES_RUST_CREATE_AGENT_SOURCE_FILES_PILOT` 须在同一工作区与 `XSHADOW_ISSUES_RUST_CREATE_AGENT_SOURCE_PILOT` 同时启用，默认关闭。直接智能体入队与手动创建共享来源文件克隆及清理意图契约。继承项目来源另需 `CREATE_AGENT_SOURCE_PROJECT`。Rust 在同一事务写入队列、待附着快照、克隆元数据和持久唤醒。无文件且无项目入队使用基本试点。

`XSHADOW_ISSUES_RUST_AGENT_SOURCE_FILES_LIFECYCLE_PILOT` 默认关闭。文件重试须在同一工作区启用 `XSHADOW_ISSUES_RUST_RETRY_AGENT_SOURCE_PILOT`。文件附着须同时启用 `XSHADOW_ISSUES_RUST_CREATE_PILOT` 和 `XSHADOW_ISSUES_RUST_ATTACH_AGENT_SOURCE_PILOT`。Rust 锁内确认原快照的精确文件集合、归属与对象键，再重试或附着同一个快照。原文件 ID 与物理对象继续使用。

带文件 enqueue 使用 `CREATE_AGENT_SOURCE_FILES`；带文件 attach/retry 单独由 lifecycle 试点控制（未开启时照旧 Go）。

`XSHADOW_ISSUES_RUST_RETRY_AGENT_SOURCE_PILOT` 为 `POST /api/tasks/{taskId}/retry-source-context` 指定试点工作区（默认关闭）。首批覆盖失败且无事项/聊天/自动驾驶绑定、仍由同一请求者持有的待附着来源快照、无目标项目或上传、智能体/运行时仍为本人本地在线且具备来源快速创建能力的形状。无项目时沿用原范围；继承来源项目另需 `XSHADOW_ISSUES_RUST_AGENT_SOURCE_PROJECT_LIFECYCLE_PILOT`；有管理克隆附件另需 `AGENT_SOURCE_FILES_LIFECYCLE`。Go 保留调用权、项目可见性与容量检查、运行时 MCP 叠加层、公开 202 契约、事件及分析。Rust 锁定运行时与原失败任务后，先锁计划来源项目及授权，再锁来源任务行、待附着快照与克隆计划，同一事务写入手动后继、转移原快照及持久唤醒；回执 `source_project_id` 绑定锁内项目。军团、非本地、离线或非原请求者继续由 Go 处理。

`XSHADOW_ISSUES_RUST_ATTACH_AGENT_SOURCE_PILOT` 须在同一工作区与 `XSHADOW_ISSUES_RUST_CREATE_PILOT` 同时启用，默认关闭。由已启动的快速创建任务凭证调用原 `POST /api/issues`。首批覆盖无项目、无附件、本人本地直连智能体的标准待办任务，支持普通优先级和日期。Rust 在原创建事务校验任务、运行时、来源与已冻结快照，在编号和容量检查后锁定并附着快照。Go 核验创建结果和原凭证归因后发布事件，旧 daemon 完成入口继续关联任务。并发提交维持原查重及单次附着行为。继承来源默认工作流项目另需 `AGENT_SOURCE_PROJECT_LIFECYCLE`；冻结克隆另需 `AGENT_SOURCE_FILES_LIFECYCLE`。`guard_parent_projects_and_assignee` 先于来源行锁定期望项目；子任务回执按 `issue.project_id` 绑定计划，不读可变来源行。

`XSHADOW_ISSUES_RUST_AGENT_SOURCE_PROJECT_LIFECYCLE_PILOT` 默认关闭。本切片覆盖 manual retry（须 `RETRY_AGENT_SOURCE`）与 quick_create attach（须 `CREATE` + `ATTACH_AGENT_SOURCE`）；有文件时另须 `AGENT_SOURCE_FILES_LIFECYCLE`。

继承项目且冻结来源附件的手动创建须在同一工作区启用上述四个试点开关。任一开关关闭时由 Go 处理。Rust 在同一事务写入带项目的子任务、快照和克隆附件，并核对实际消费的意图集合。Go 核验子任务项目和来源归属，再发布创建事件。

`XSHADOW_ISSUES_RUST_CREATE_ATTACHMENTS_PILOT` 为 `POST /api/issues` 的普通成员创建时关联上传指定试点工作区，须在同一工作区同时启用 `XSHADOW_ISSUES_RUST_CREATE_PILOT`。Go 只选同工作区的未绑定上传。Rust 在创建事务中先锁父任务，再按附件 ID 顺序关联尚未被占用的上传。可选关联回滚后仍返回已创建任务，创建修订保持原规则。Go 用回执和数据库读回生成原有文件信息及创建通知。已有归属、来源、命名工作流和智能体或军团派工继续使用原入口。

`XSHADOW_ISSUES_RUST_METHOD_ADOPT_PILOT` 为 `POST /api/issues/{id}/work-method` 中 `adopt_revision` 的普通成员采用试点。Go 沿用 `run.Adopt` 域计算与视图/事件组装，并向 Rust 提交同一工作区模板快照与计划运行。Rust 锁模板目录与事项行，核对修订与原始 `work_method_run`，仅在计划要求时写入新方法快照并推进事项修订。目标计划交付、项目交付绑定与军团执行绑定仍走 Go。试点上游不可用返回 `503`，回执异常返回 `502`，失败闭集不回退 Go 二次写入。

`XSHADOW_ISSUES_RUST_COMMENT_EDIT_PILOT` 为 `PUT /api/comments/{id}` 的严格正文编辑指定试点工作区，默认关闭。覆盖成员作者、无项目、未指派或成员指派、顶层 `comment` 类型、带正 `expected_revision`、可选 `content_base`、无附件替换/抑制/提及/笔记语法/任务补充/来源行族/触发队列的形态。Go 保留公开校验、反应与附件水合、`revision_conflict` 与 `comment:updated` 事件；Rust 按 issue `FOR UPDATE` 再锁评论、复用 `UpdateComment` CAS，正文真变时清 `source_task_id` 与抑制列表并推进任务 `revision`/`last_activity_at`，不变重放仍发事件且不附带 `issue_revision`。无取消与派工；试点选定后不回退 Go。

`XSHADOW_ISSUES_RUST_COMMENT_RESOLUTION_PILOT` 为 `POST`/`DELETE /api/comments/{id}/resolve` 指定试点工作区，默认关闭。首批覆盖成员身份、无项目任务讨论串。Go 保留公开校验、反应/附件水合与 `comment:resolved`/`comment:unresolved` 事件顺序。Rust 在事务内按 issue `FOR SHARE`、线程根 `FOR NO KEY UPDATE` 序列化，清除同串其他 `resolved_at` 后设置目标，只递增变更评论的 `revision`，不触碰任务 `revision`。重复 resolve 保留原解析人与时间戳；晚移入项目持锁后拒绝并整笔回滚。智能体身份与有项目任务继续走 Go。无 Rust 回退二次写入。

`XSHADOW_ISSUES_RUST_REACTION_PILOT` 为 `POST`/`DELETE /api/issues/{id}/reactions` 指定试点工作区，默认关闭。首批覆盖成员身份、无项目任务的添加与移除。Go 保留公开校验、持久唤醒回执与 WebSocket 事件；Rust 先对工作区/成员 `FOR SHARE` 并设置写会话，再在无任务锁的情况下写入 `issue_reaction`，随后以 `FOR NO KEY UPDATE` 复核无项目可见范围，仅在实际增删时递增 `revision`（不触碰 `updated_at`）。冲突路径在独立语句中读取获胜行并可有限重试。范围拒绝整笔回滚反应行与触发器回执。重复添加返回同一回应且无事件；移除不存在的回应仍返回 `204`。持锁后落入项目或私有范围返回 `404`/`409`。智能体身份、有项目任务及评论回应继续走 Go。Go 以数据库 revision 与反应行归属确认回执后再广播。上游不可用或回执异常分别返回 `503`/`502`，选定形状不回退 Go 二次写入。

在 XShadow 工作台根目录执行 `./workbench rust verify`。验证使用一次性 PostgreSQL 容器，并通过 [已录制的 HTTP 响应](app-cases/http/) 检查两种实现。检查覆盖完整迁移、遗漏早期迁移、迁移表查询失败、数据库不可用，以及候选进程和版本标识。

## 迁移顺序

| 阶段 | 当前状态 | 进入下一阶段的条件 |
| --- | --- | --- |
| 存活及就绪检查 | Rust 候选已接入工作台复验 | 录制用例一致，真实 PostgreSQL 的就绪状态按预期变化 |
| HTTP 请求入口和数据访问 | 待迁移 | 保留鉴权、工作区访问边界、客户端错误契约和 SQL 事务行为。验证同一请求携带不同凭证时的结果 |
| 任务、目标、派工和后台调度 | 待迁移 | 并发或重试时工作状态收敛，服务重启后待执行任务能继续，任务交接可追溯 |
| WebSocket、上传、集成及迁移程序 | 待迁移 | 验证会话重连、文件和事件传递，以及已有数据库的升级、失败恢复和回退 |
| 发布 | 待验收 | 目标范围内所有生产入口由 Rust 实现。整套回放通过后，在工作台按版本构建、备份、切换和复验 |

`rust-anything analyze server --whole-app --lang go` 在 Go 基线识别到 96 个候选单元。工具对 42 项依赖的 Rust 适配给出 `unknown`。分阶段验证依赖，整套迁移以外部行为回放为准。
