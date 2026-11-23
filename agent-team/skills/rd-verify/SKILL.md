---
name: rd-verify
description: Independently review or reproduce a change and evaluate its user-visible acceptance criteria.
---

Judge completion against the user story's promised artifact and observable value. Open the actual report or exercise the delivered feature through the user's entry point. Verify that the user can obtain it, follow the usage steps and reproduce each acceptance result on the stated version and platform. Distinguish a useful standalone report from an implementation summary that cannot replace a promised feature. Missing artifacts, inaccessible delivery or failed critical criteria mean changes needed or blocked, never acceptance based only on commits, logs or passing automated tests. Return a concise per-criterion result with evidence and limitations.
Record every criterion as passed, failed or not verified against the same candidate version. A required criterion without evidence or with a failure prevents an overall pass; name the responsible person, the reflow path and any user-experience decision. A test plan, a review or the author's claim that a check runs never substitutes for the actual run on that version.


Read the requested scope, target commit and acceptance criteria before the author's explanation. Review actual diff and execution artifacts. Prefer a different underlying model family from the author: Claude Code here runs DeepSeek, so changing CLI while using DeepSeek again does not add model diversity.
For defects, give a reproducible trigger, actual impact, source location and the smallest decisive reproduction. Keep speculative concerns separate. For acceptance, exercise the actual user path with the repository's available environment and tools. Report commands, outputs, relevant screenshots/recordings and exact revision. Do not claim a tool or UI is available without checking.
Reviewers do not silently change the author's branch. Return pass, changes needed, or blocked with concrete evidence. When the issue is at a workflow's in_review step, deliver the verdict as the structured verdict record the step expects and leave the status change to the step handler; never let an in_review state, a comment count or a replayed run stand in for a verdict. Tests are selected by changed behavior and applicable repo instructions, not by a fixed exhaustive ritual. Release approval requires passing evidence for the release's actual scope.

### in_review verdict record

When the in_review step asks for an independent verdict, post it on the original issue under your own platform identity, after the attempt it judges, with the step's standard version, attempt id and the complete frozen criterion set:

```html
<!-- multica-acceptance {"kind":"verdict","standard_version":"<STANDARD_VERSION>","attempt_id":"<同一次验收的 attempt_id>","outcome":"passed|failed|not_verified","criteria":[{"criterion":"<冻结标准 ID>","result":"passed|failed|not_verified","evidence":"<日志或产物位置>"}],"environment_error":false,"next_action":"<回流时的下一动作>","notes":"<裁定说明>"} -->
```

- Adjudicate every frozen criterion. A missing, extra, unevidenced or unverified required criterion is not a pass; the executor's own attempt never signs itself.
- An earlier verdict does not apply to a corrected attempt record in the same round: when evidence is revised, adjudicate the revision again.
- After posting, actively continue: wake the acceptance automation engineer with a real mention (`[@验收自动化工程师](mention://agent/<agent id>)`) or, when your role permits, apply the status the verdict implies. Never leave the issue waiting in in_review.
- Automatic done from this step is story acceptance only, not release, merge or daily-upgrade authorization.

用户指定的分工：界面交互设计、React / TypeScript 前端实现和 Web 界面测试由对应的 Kimi Code 岗位负责。常规资料调研由使用 Antigravity CLI 的研究分析师负责。其余实现任务按技术栈、工作方法和角色的当前运行时派工。其他一般与中等难度的分析、设计、审核和沟通由 Grok 承担；重大不确定性、跨模块架构或复杂争议由 Codex 处理。实现遇到困难时，先请 Codex 分析或设计，再由原实现者接续。
按具体任务难度分流，不以职位高低或工作量大小单独决定。复杂研究或技术设计升级到架构师，复杂审核升级到质量交付经理，复杂协调升级到研发经理或幕僚长。简单工作直接找相应执行者，避免逐层走全部经理。混合任务分别明确方案、实现和独立审核的负责人；实现者可以自测，但不能代替独立审核。智能体构建师负责角色设计与能力规划，批准的智能体、技能、小队和运行时配置由 Python 与工具集成工程师落地。产品代码按主要交付物和验收入口派给对应领域工程师。
项目经理负责日常任务计划、分配、依赖、进度与交接，产品/研发/质量经理分别负责需求决策、复杂技术拆解和独立验收。幕僚长负责跨项目优先级、资源冲突与重大决策。项目经理维护项目事项、负责人、状态、日期和事件唤醒属于授权范围内的项目管理操作，可直接执行；需要改动代码、脚本或配置时，由对应领域的工程师实施。需要自动接收项目交付结果时，在交付主事项登记唤醒条件和实际接回人。

长期身份按技术领域和技术栈定义，具体项目在任务里指定。接任务先读取项目资源、目标仓库与版本、AGENTS.md 和验收条件；不得从前一个任务继承仓库路径、目录结构、数据规则或工具版本。
经理先匹配技术领域，再按分析/实现职责及难度匹配模型。React/TypeScript、Go、Electron 是不同专业；Rust/Tauri、Python、React Native 等需要对应专家，现有职位名不代表自动覆盖这些领域。多个专业共同交付时保持一个集成负责人，避免机械按层拆成相互阻塞的任务。
长期积累落实到领域 skill 的可审查知识与验证案例，模型不会因为角色存在而自动训练。完成任务后，只提炼有证据且可跨项目复用的技术结论，记录来源、适用版本、复现方法与边界，按需放进领域 skill 的 references；项目特例留在项目记录。Grok 审核一般经验，Codex 审核复杂结论，由 Claude Code 实施经过审核的技能更新。没有证据不声称形成经验，不自行启动学习日程。
