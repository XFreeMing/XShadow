# 成员待办

成员待办是交给一个真实的人、且只有他能给出结果的可见事项。创建前先判断是否真的只能由本人提供：

- 能交：只有本人能导出的材料、只有本人能给的授权、只有本人有使用习惯的偏好、只有本人能承担的业务取舍。
- 不交：日志、脚本、复跑、测试报告、回执、命令行输出等团队自己能做的验证；持有授权会话的操作方也按团队内部处理，不转给用户。
- 凭据要求只写他本人能提供的那一项，并与业务取舍分开；不要把“帮我确认能不能登录”和“这个版本发不发”合成一条。

## 从已安装技能运行（无项目仓库也可用）

生成器随 `rd-project-management` 技能一起安装，不依赖业务仓库：

```bash
node <已安装的 rd-project-management 技能目录>/scripts/member-todo.mjs --check --input ./todo-input.json
node <已安装的 rd-project-management 技能目录>/scripts/member-todo.mjs --input ./todo-input.json --out-dir ./.member-todo
```

本地找不到技能目录时，用平台取回同一文件再运行：

```bash
# skill get 只接受 UUID；先按 name 从 skill list 取 id。
SKILL_ID=$("$MULTICA_CLI_PATH" skill list --output json | node -e 'let s="";process.stdin.on("data",d=>s+=d).on("end",()=>{const row=JSON.parse(s).find((r)=>r.name==="rd-project-management");if(!row)process.exit(1);process.stdout.write(row.id)})')
"$MULTICA_CLI_PATH" skill get "$SKILL_ID" --with-content --output json > ./rd-project-management.json
# 从 files[] 中取出 path 为 scripts/member-todo.mjs 的 content，写到本地后执行
node ./member-todo.mjs --check --input ./todo-input.json
```

`--sample` 会打印四类可复跑样例（材料、授权、偏好、业务取舍），可先对照再写自己的输入。校验失败时退出码为 2，并列出具体缺口；不要带着缺口创建事项，也不要用占位模板、凭空建议或默认选项补齐。预检和普通生成都会拒绝这些输入：选项缺少回复示例；标题、首屏、完成方式或接续说明出现回执、日志、测试等内部验证用语；`completion_signal=done` 却写成只回复、不必标记完成，或 `next` 承诺回复后原事项就继续；把标记完成说成走个形式、可以空着、与后续处理无关或类似的可选说法；把运行测试、附日志等团队内部验证写进选项示例；入口或接续文案仍是“待确认”“稍后公布”“稍后发给你”之类的占位信息。

## 输入

```json
{
  "kind": "choice",
  "title": "订单列表先显示金额还是先显示状态",
  "recipient": { "name": "dev" },
  "source": { "identifier": "DEV-63", "id": "<来源事项 id>" },
  "request": "order-list-first-column",
  "completion_signal": "comment",
  "problem": "订单一屏放不下所有信息，需要先定第一列显示什么，否则上线后再改会影响日常查看。",
  "why_you": "这是每天使用习惯的取舍，只有你每天看这个列表。",
  "action": "选一个你更希望打开列表就先看到的信息。",
  "where": "直接在本事项回复。",
  "options": [
    { "label": "先显示金额", "consequence": "打开列表先看到每单金额，订单状态要点开详情查看。", "example": "回复“金额”" },
    { "label": "先显示状态", "consequence": "打开列表先看到订单进度，金额要点开详情查看。", "example": "回复“状态”" }
  ],
  "submit": "回复“金额”或“状态”。",
  "done": "在本事项回复你的选择",
  "next": "项目经理会在本待办收到你的回复，并接回原事项继续处理。",
  "technical": { "来源": "DEV-63", "缺口键": "…" }
}
```

- `kind`：`material`（材料）、`authorization`（授权）、`choice`（偏好选择）、`decision`（业务取舍）。
- `handler`：固定为已配置活跃的接件角色 `幕僚长`，产品约定所有成员答复统一交给它，不能通过正文或 `handler` 改派其他角色；客户端在**当前工作区**按名称解析角色身份，不写机器 UUID。项目经理的完成回调只作为原事项的必要衔接保留。
- `recipient.name` 或 `recipient.id` 至少一个真实接收人；`source.identifier` 用于回链和查重。
- `request` 是稳定的输入请求标识，同一请求重发时沿用原文；验收决策用 `decision_key`。
- `completion_signal` 必须与真实登记的唤醒规则一致：`done`、`comment` 或 `manager`。正文里的“怎样算完成 / 之后”会被校验。触发句由 `completion_signal` 固定生成：`done` 是“先在本事项标记完成，原事项随后继续处理”，`comment` / `manager` 是项目经理收到回复后接回。`next` 只写业务处理结果；没有自带同一触发条件时，生成器会把固定触发句放在前面。声称“回复后原事项就继续”“不用标记完成”，或把标记完成写成走个形式、可以空着、与后续处理无关等说法，都与信号矛盾并被拒绝。默认用 `done`：用户在成员待办里回复并把待办标记完成，原事项用 `--until-issue <待办> --until-issue-state done` 等它完成。新版「我的任务」一次提交会先写下答复评论（提到 `handler` 角色），再按 `completion_signal` 完成待办；旧客户端仍按正文的“评论 + 完成”方式操作，两条路径都必须真实可接续。
  - `done`：正文写“在本事项回复你的选择，并把本事项标记为完成（只回复不会自动接续）”；原事项登记 `--until-issue <待办> --until-issue-state done`。
  - `comment`：正文只要求“在本事项回复”，就必须在**成员待办自身**登记 `comment.created` 事件唤醒项目经理（`issue wakeup create <待办> --event comment.created --filter-actor-type member --filter-actor-id <用户 id> --agent-id <项目经理 id>`），由他读取选择后接回原事项。不能把 `comment.created` 登记在原事项却让用户去待办回复——那是两个不同事项，永远不会唤醒。无法登记待办监听时，改成“请回原事项回复”并在原事项登记，或直接改用默认的 `done`。新版客户端答复时会提到 `handler` 角色；这条规则仍要保留，覆盖旧客户端和手工回复。
  - `manager`：同样必须有真实唤醒（待办 `comment.created` 或 `done` 规则唤醒项目经理）；只写“项目经理会查看”但没有规则会失联。
- `options`：授权、偏好和取舍至少两项，每项写清 `label`、`consequence`、`example`；需要跨版本保持身份时再给稳定的 `key`（字母、数字、下划线或连字符），没有时生成器按名称派生。不设默认项或推荐项。选项会随标记原子写入正文末尾，客户端只读标记，不再从 Markdown 列表推测。
- `problem` / `why_you` / `action`：会以同一份原文进入标记，供「我的任务」卡片直接展示；改正文时保持与标记一致，重新生成即可。
- `technical`：键值对，会进入正文末尾的技术附录。标准号、缺口键、SHA、候选版本、环境等只放这里。

## 生成结果

```text
<问题>                    ← 第一屏：发生了什么、影响是什么
<为什么需要你>

**你需要做一件事：** <具体动作>
入口：<真实入口或“直接在本事项回复”>

可选的做法：
- **<做法>**：<交付什么、放弃什么>（例如：<他会怎么回复>）

请在本事项回复：<最小提交物>
怎样算完成：<与完成信号一致的结单方式>
之后：<原事项会怎样继续>
---
## 技术附录（团队内部，无需阅读）
- 来源事项：<回链>
- 输入请求：<request>
- <technical 键值>
decision_key: <验收决策键>
<!-- multica-member-todo {"todo_key":"…","kind":"…","source_issue":"…","request":"…","completion_signal":"…","handler":"幕僚长","problem":"…","why_you":"…","action":"…","options":[{"key":"…","label":"…","consequence":"…","example":"…"}]} -->
```

标题写用户能看懂的结论或取舍，不超过 60 字；不要放标准号、缺口键、SHA、轮次或环境。信息不足时先回原事项补齐上下文，再重新生成；不要用“这项功能”“需要你决定下一步”之类泛称兜底。

标记里的 `options` 是一个整体：客户端要么完整渲染全部选项，要么安全回落到普通评论，不会猜测任意 Markdown。材料待办没有选项；DEV-86 及更早生成、标记里没有选项或 `completion_signal` 的历史待办同样回落为“评论 + 完成”的旧方式，不显示选择表单。

## 创建与登记

1. 用生成器产出 `title.txt`、`body.md`、`metadata.json`，按输出的 `create_command` 用平台正常入口创建并指派给真实接收人。
2. 读回事项，确认 assignee 是该人，正文首屏与生成结果一致。
3. 补写元数据 `member_todo_key`、`member_todo_kind`、`member_todo_source`（验收决策另有 `acceptance_decision_key`），值都用字符串。
4. 登记与 `completion_signal` 一致的恢复规则，并读回确认：
   - `done`：在原事项登记 `--until-issue <待办> --until-issue-state done --mode once --agent-id <负责人 id>`，正文写明把待办标记完成。
   - 只有指定选项才能交接时，从待办正文的选项取稳定的 key，在上面的单次规则加 `--until-member-option <key>`。登记在提出请求的原事项，读回接单人、选项和规则 ID。暂缓、自由文本和仅标记完成都继续等。幕僚长收到答复后先看原事项已有的规则及运行，同一交接只走一次。旧客户端的无选项答复由幕僚长核实后处理。另一事项仍须满足自己的资料、批次授权和独立审核。
   - `comment`：在**成员待办**上登记 `--event comment.created --filter-actor-type member --filter-actor-id <用户 id> --mode once --agent-id <项目经理 id>`；项目经理被唤醒后读取回复并接回原事项。正文写“在本事项回复”，不再写“原事项会自动继续”。
   - `manager`：同样在成员待办上登记 `done` 或 `comment.created` 规则唤醒项目经理，不能只有一句“项目经理会查看”。
   没有登记成功的规则，不要承诺“只回复就会自动继续”。
5. 答复路由：新版「我的任务」在提交时按**当前工作区**解析 `幕僚长` 角色，写一条带 `multica-member-answer` 标记和 @mention 的答复评论；该评论只唤醒幕僚长一次（客户端用稳定的答复 UUID 防重）。幕僚长读取本人原话、判断下一步并派回原负责人。旧客户端的“评论 + 完成”路径保持不变，两条路径都必须能接回原事项。正文里出现别的 handler 名称不代表改派，接收端只认当前工作区的幕僚长。

## 查重与复用

- 同一来源事项 + 同一输入请求只对应一条未关闭待办；即使守护缺口键变化，也沿用旧待办和它仍然有效的恢复条件。
- 按正文里的 `multica-member-todo` 标记（`todo_key`、`source_issue`、`request`）或元数据 `member_todo_key` 查重，不按标题判断；标题可以改写，身份不变。
- 旧待办已关闭但原事项仍需同一输入时，再按同一 `todo_key` 新建；创建失败或同一请求重试时先搜索复用，不重复建单。
- 平台按规范化标题查重，忽略正文键：创建返回重复标题时，先 `issue get` 读回命中待办的标记或元数据。同键直接复用并补写元数据；异键不合并、不关闭对方，按生成器给出的 `title_candidates` 换成「（第 2 项）」等自然序号重试，不关闭平台保护。同一键的并发重试从基础标题走同一序列，最终落在同一候选上。
- 创建或复用都没有确认前，不要回链新待办，也不要声称已派发。