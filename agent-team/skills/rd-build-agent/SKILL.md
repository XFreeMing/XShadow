---
name: rd-build-agent
description: Create or refine specialist agent roles and skill assignments when a user authorizes team expansion.
---

Separate role design from configuration implementation. Grok designs ordinary and medium-complexity roles; Codex handles difficult role architecture. The Claude Code toolsmith applies the resulting agent, skill and squad configuration. The designer reads back and reviews the result.
Inspect existing agents, squads, skills and online runtime catalogs first. Reuse an adequate role before creating another. A new role needs one accountable technical domain, supported stack, explicit boundaries, an output contract and a reason the existing roster cannot cover it. Keep expert identity independent of projects. Add language/framework specialists when a real task needs them; do not make one role claim every backend or desktop stack. Store target-specific paths and rules with the project and pass them in the task brief.
Honor standing user configuration: Claude Code uses deepseek-v4.1-flash with high effort; Codex and Grok are selected from the actual runtime catalogs according to task complexity. Cursor is deferred until the user resumes setup. Start new roles at one concurrent run. Reuse existing runtime limits and never raise them without a concrete workload need and user direction.
Use multica-platform's agent/skill/squad commands. Create an agent, explicitly assign its workspace skills, add it to the relevant squad with a meaningful role, and read back the stored instructions, model, effort, permissions and skills. A squad member role label is not a skill assignment. Names and descriptions alone do not define behavior.
Creation requests authorize the necessary local configuration; avoid asking again for already specified choices. Ask only for missing consequential scope or access. Do not create recurring schedules, arbitrary MCP integrations or an expanding self-replicating hierarchy as a side effect. Test a new provider through one bounded, harmless task before assigning production work. Record created IDs and evidence.

用户指定的分工：代码、脚本、工具、配置和测试实现统一交给 Claude Code（deepseek-v4.1-flash / high）。分析、研究、设计、审核、沟通中，普通及中等难度交给 Grok；高难度、重大不确定性、跨模块架构或复杂争议交给 Codex。实现困难时先请 Codex 分析或设计，再由 Claude Code 实现，不能把困难实现直接转给 Codex。
按具体任务难度分流，不以职位高低或工作量大小单独决定。Grok 研究/设计升级到架构师，复杂审核升级到质量交付经理，复杂协调升级到研发经理或幕僚长。简单工作直接找相应执行者，避免逐层走全部经理。混合任务分别明确方案、实现和独立审核的负责人；实现者可以自测，但不能代替独立审核。智能体构建师负责角色设计与能力规划，创建或修改平台配置、脚本、工具和仓库文件均交给CLI / MCP 集成工程师实施。
项目经理负责日常任务计划、分配、依赖、进度与交接，产品/研发/质量经理分别负责需求决策、复杂技术拆解和独立验收。幕僚长负责跨项目优先级、资源冲突与重大决策。项目经理维护项目事项、负责人、状态、日期和事件唤醒属于授权范围内的项目管理操作，可直接执行；代码、脚本及智能体/运行时配置实现仍由 Claude Code 负责。需要自动接收项目交付结果时，将交付主事项分配给“项目推进”小队，不能只依赖项目负责人字段。

长期身份按技术领域和技术栈定义，具体项目在任务里指定。接任务先读取项目资源、目标仓库与版本、AGENTS.md 和验收条件；不得从前一个任务继承仓库路径、目录结构、数据规则或工具版本。
经理先匹配技术领域，再按分析/实现职责及难度匹配模型。React/TypeScript、Go、Electron 是不同专业；Rust/Tauri、Python、React Native 等需要对应专家，现有职位名不代表自动覆盖这些领域。多个专业共同交付时保持一个集成负责人，避免机械按层拆成相互阻塞的任务。
长期积累落实到领域 skill 的可审查知识与验证案例，模型不会因为角色存在而自动训练。完成任务后，只提炼有证据且可跨项目复用的技术结论，记录来源、适用版本、复现方法与边界，按需放进领域 skill 的 references；项目特例留在项目记录。Grok 审核一般经验，Codex 审核复杂结论，由 Claude Code 实施经过审核的技能更新。没有证据不声称形成经验，不自行启动学习日程。
