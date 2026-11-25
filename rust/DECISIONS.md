# XShadow Rust 架构决策

## ADR-001  服务端迁移范围

将 `server/cmd/server` 承载的 API、实时连接和后台任务迁到 Rust，随后迁移 `server/cmd/migrate`。客户端、本机 daemon 和外部集成通过现有网络接口接入。复用 PostgreSQL 的表、约束和历史 SQL 迁移。

## ADR-002  HTTP 与数据库

HTTP 路由使用 Axum，异步运行时使用 Tokio，数据库使用 SQLx。首个候选按模块隔离启动、HTTP 响应和就绪检查。编译时从现有 SQL 文件收集迁移版本，运行时查询同一 `schema_migrations` 表。候选只读数据库，并可在数据库不可用时返回独立的存活状态。

技术文档见 [Axum](https://docs.rs/axum/0.8.9/axum/) 和 [SQLx PostgreSQL](https://docs.rs/sqlx/0.8.6/sqlx/postgres/)。

## ADR-003  隔离复验和切换

候选先独立运行，日常进程通过工作台原入口发布。若后续引入 Rust 转发入口，需要先用原客户端验证 HTTP 流式响应、WebSocket 升级、Host 与来源检查，以及客户端地址的识别。上游 `rust-anything` 的通用 HTTP 模板只覆盖普通请求转发，因此当前候选直接暴露已迁移的路由。 [模板实现](https://github.com/erkamyaman/rust-anything/blob/v4.0.0/src/front_door_http.rs)

回放归一化了进程号、提交号和启动时间。工作台另行检查候选的进程号与构建提交是否准确。生产切换还需验证浏览器跨域、鉴权、WebSocket、任务延续和发布后的数据兼容。 [录制配置](app-cases/rust-anything.json)

## ADR-004  通用表格

通用表格由 Rust 读写和校验，存储包括表、字段、记录、单元格、视图，以及按表递增的变更和在线位置。Go 在现有成员或任务凭据校验之后，把已经确定的工作区标识、内部令牌和可信协作者转发到本机回环地址。Rust 不读取客户端的 `X-Workspace-ID`，也不采信客户端自报的作者。未配置上游或令牌时，这条路由返回未启用，其余入口保持原样。

智能体任务凭据与成员使用同一公开入口。`GET /api/tables/{id}/changes?since=` 按序号补读变更和当前在线位置，`POST /api/tables/{id}/presence` 续租当前单元格，`PATCH /api/tables/{id}/records/{recordId}` 仍按 `expected_revision` 写入。变更序号在持有表行锁的同一事务里递增。同一基线、同一记录的不同字段，在从客户端基线到当前版本的变更链完整且字段互不相交时都落库。同一字段并发时一个成功、另一个返回 409，已保存的值保留。同一协作者用相同客户端版本和相同摘要重试，且单元格仍是该值时，不另写一条历史；单元格后来被改过则不再视为同一次写入。基线之后的历史有缺口、重复或不连续时不猜测，返回 409。`expected_revision` 等于当前版本时直接写入。网页每 2 秒请求 `/changes`。出现新序号时重新读取该表快照，让筛选、排序和可见列重新计算；正在编辑的格子先保留本地草稿，失焦后再读。事件只提供作者和游标，不把其中的值写回快照。

任务自定义属性、任务类型、知识来源和跨表引用是另外的聚合。它们以后可以保存这里的字段或记录标识，但不写入通用表格，本阶段也不预留空接口。
