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

通用表格（表、字段、记录、单元格、视图）由 Rust 读写和校验。Go 只在现有成员校验之后，把已经确定的工作区标识和内部令牌转发到本机回环地址。Rust 不读取客户端的 `X-Workspace-ID`。未配置上游或令牌时，这条路由返回未启用，其余入口保持原样。

任务自定义属性、任务类型、知识来源和跨表引用是另外的聚合。它们以后可以保存这里的字段或记录标识，但不进入这四张表，本阶段也不预留空接口。
