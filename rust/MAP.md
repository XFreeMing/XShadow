# XShadow 后端迁移到 Rust

目标是让 Rust 服务承载 XShadow HTTP API、WebSocket、服务端调度和数据库迁移。现有桌面、Web、移动端和本机 daemon 通过原有协议访问服务。Rust 候选的运行边界和选型见 [架构决策](DECISIONS.md)。

## 当前交付

Rust 候选位于 [app/](app/)。它独立处理 `/health`、`/readyz` 和 `/healthz`，并在同一进程提供工作区通用表格。Go 承载公开 API 和认证，将通过成员校验的表格请求转到本机 Rust。Go 健康检查参考版本为 `883566da7e2d8d256b4c775feec2be875a827d00`。候选仅监听 IPv4 回环地址。日常工作台按已发布版本启动 Go 服务。

原生任务详情 `GET /api/issues/{id}` 支持按工作区试点。设置 `XSHADOW_ISSUES_RUST_READ_PILOT` 为工作区 UUID 或 slug 后，Rust 在同一 PostgreSQL 中读取原任务并检查项目可见性，Go 补齐附件 URL 和冻结来源。连接可复用 `XSHADOW_TABLES_UPSTREAM` 与 `XSHADOW_TABLES_INTERNAL_TOKEN`。清空试点设置后，详情回到 Go。试点期间 Rust 不可用时，选定工作区收到 `503`，其他工作区继续经由 Go 提供详情。

原生任务列表 `GET /api/issues` 与 `POST /api/issues/query` 可用 `XSHADOW_ISSUES_RUST_LIST_PILOT` 独立选择工作区。Go 验证请求参数，Rust 筛选任务、检查可见范围、查询分页和总数。该设置只作用于这两个列表入口。清空后，列表回到 Go。

`XSHADOW_ISSUES_RUST_TABLE_PILOT` 独立指定表格行、分组标题和筛选统计的试点工作区。Go 校验公开请求、解析目录与智能体可见范围，并负责编码游标和分组描述。Rust 在每个请求的只读快照内检查项目可见范围并查询行、子任务、分组和各筛选项的计数。筛选统计按需忽略该项自身的平铺条件，保留高级筛选。清空试点设置后，三个入口回到 Go。

`XSHADOW_ISSUES_RUST_WRITE_PILOT` 独立指定普通编辑试点工作区，支持 `PUT /api/issues/{id}` 的标题、描述、附件关联、优先级、排序位置、开始日期和截止日期。Go 验证公开请求并发布原任务事件。Rust 在锁定任务的事务内检查项目可见范围、核对修订、合并渠道媒体、计算待开始的工作方法推荐并写入。混合其他字段的请求继续由 Go 执行。试点上游不可用时，已选中的写入返回 `503`。清空设置后，普通编辑由 Go 执行。

同一试点设置也覆盖 `PUT`、`DELETE /api/issues/{id}/properties/{propertyId}`。Go 规范化字段值并检查引用，Rust 通过任务行锁和属性定义的共享事务锁执行单字段更新。定义发生变化时，网关只在收到明确未写入的回执后重新读取定义并校验。清除必填字段的检查依据锁定后的任务类型。

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
