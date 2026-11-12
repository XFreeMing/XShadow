# 个人智能体军团初始化

在另一台电脑 clone 本仓库后，从仓库根目录执行：

```bash
make agent init
```

命令复用 `scripts/dev-env.sh`，启动当前检出的 API 和 daemon，准备本地开发账号与工作区，再初始化 **18 个智能体、13 项技能、5 个小队**。它不会创建默认服务项目、自动化日程或模型任务。XShadow 是承载平台，军团服务于用户指定的项目与任务。

## 运行前提

- 使用仓库支持的开发环境，安装 Git、Make、Bash、Node.js、Go、curl 和 PostgreSQL 启动所需依赖；具体版本与数据库方式见 [CONTRIBUTING.md](../CONTRIBUTING.md)。Linux/macOS 可使用本机工具链；Windows 使用具备这些依赖的 WSL 环境。
- 安装并登录 Claude Code、Codex 和 Grok，使 daemon 能发现它们。模型账号、API Key 和认证不会随模板迁移。
- Claude Code 的本机提供商配置需要支持 `deepseek-v4.1-flash`，推理等级为 `high`。
- 当前快照保留实际配置：4 个 Codex 角色使用 `gpt-6-astra / high`，Grok 角色使用 `grok-4.7 / high`。之前讨论的 Sol/Terra 替换尚未应用；需要时明确修改 `team.json` 中相应角色的模型。
- Cursor 暂不纳入初始化。

初始化前会核对全部运行时、模型和推理等级。任一项缺失时，军团配置不会开始写入；已启动的本地环境会保留，修正后重跑同一命令即可。模型目录可用只代表配置可选，不代表真实任务已经验收。

需要桌面界面时运行 `make up C=desktop`。初始化本身只启动 API 和 daemon。默认账号和服务属于本地开发环境；这不是生产部署或数据库迁移命令。

## 初始化到已有实例

先通过仓库 CLI 的 `setup self-host` / `login` 完成登录并选择工作区，再使用对应 profile：

```bash
make agent init ARGS="--profile my-team --check"
make agent init ARGS="--profile my-team"
```

显式传入 `--profile` 时不会启动服务。脚本读取该 profile 的 `server_url`、`token`、`workspace_id`，不从当前任务的 `MULTICA_TOKEN` 等变量继承身份。不要复制旧电脑的 profile 作为军团模板。

默认选择本机 daemon 对应的在线运行时。需要指定其他运行时，或者同一 provider 有多个候选时，显式映射：

```bash
make agent init ARGS="--profile my-team --runtime claude=运行时UUID --runtime codex=运行时UUID --runtime grok=运行时UUID"
```

实际 UUID 可通过 `multica runtime list --profile my-team --output json` 获取。`--profiles-dir` 可指定 CLI profile 根目录。

## 重复执行与更新范围

脚本按工作区中的**名称**匹配技能、智能体和小队。重复执行会同步模板拥有的字段，包括人设、描述、模型、运行时绑定、权限、并发、聊天入口和技能文件；请先将需要保留的定制同步到模板。同名智能体属于其他账号或已归档时会停止。

未出现在模板中的项目、任务、角色、技能、小队，以及额外绑定的技能和小队成员会保留。所有模板角色默认仅创建者可调用。三个被选中运行时的总并发分别设为 Claude 2、Codex 1、Grok 1，这也会影响绑定到它们的其他智能体。

创建顺序为技能、智能体、小队和成员关系；小队指令中的 `{{squad:key}}` 会替换成目标实例的 ID。写入中途失败时，已经成功的部分会保留；重跑按名称继续，不重复创建。完成后逐项读回核验。一次只运行一个初始化进程。

`--check` 只验证模板、目标身份、运行时和模型；不修改军团配置。不指定 profile 时，它要求当前检出的托管环境已启动。

## 模板维护

- `team.json`：角色、模型、小队关系、聊天入口和并发。
- `common.md`：军团共同职责与调度规则。
- `skills/<name>/SKILL.md`：技能入口；同目录可添加 UTF-8 的 `scripts/`、`references/` 等文件，初始化时一并同步。

领域专家跨项目复用。项目路径、仓库规范和任务历史放在实际项目/任务上下文中，不写进通用人设。凭据、MCP 密钥、个人账号、上传附件、头像及执行历史不属于本配置包。文件技能当前按 UTF-8 文本同步，不用于二进制资源。

修改后验证：

```bash
node --test scripts/agent-team.test.mjs
pnpm test:scripts
make agent init ARGS="--profile my-team --check"
```

这些检查不启动真实模型任务。真实调用、工具授权及跨智能体交付需另行验收。
