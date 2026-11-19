package workmethod

import "reflect"

// The original definition identifies workspaces that only rebound the roles.
// Edited procedures and the snapshots on existing issues keep their contract.
func legacyAgile() Template {
	return Template{Revision: 1, ID: "agile", Name: "敏捷研发", Kind: "development", Keywords: []string{"研发", "开发", "实现", "修复", "交付功能", "交付软件", "开发功能", "发布版本", "缺陷修复", "软件迭代", "软件增量", "implement", "develop", "fix bug", "fix the bug", "ship a feature", "software delivery", "agile"},
		Steps: []Step{
			{Name: "定义增量", Output: "明确用户场景、迭代目标，并列出可观察的验收标准。",
				Inputs: "需求原文、用户反馈和当前行为", Instructions: "找出本次受益者和一个可独立演示的变化，把成功与失败场景写成可观察的标准。",
				Checks: []string{"每条标准指明触发条件与预期行为", "本轮范围和延期项已列明"}},
			{Name: "规划迭代", Output: "确定本次交付范围、用例、依赖及负责人。",
				Inputs: "增量目标、验收标准与交付约束", Instructions: "为每条标准分解开发与验证工作，确认依赖、优先级及执行顺序。",
				Checks: []string{"每条标准关联至少一个验证场景", "依赖和负责人可追溯"}},
			{Name: "实现并验证", Output: "关联实现所在仓库、分支、提交和验收用例；登记执行证据。",
				Inputs: "迭代计划、目标分支和验收用例", Instructions: "分批实现改动，在候选提交上执行关联用例，保留失败记录与修复后的结果。",
				Checks: []string{"实现定位到仓库、分支和提交", "执行证据标明用例、版本与结果"}},
			{Name: "演示与回顾", Output: "在交付入口复验每条标准，记录反馈和下一轮改进。",
				Inputs: "候选版本、用例结果与验收标准", Instructions: "从实际使用入口演示关键场景，收集反馈，将改进建议分派到后续迭代。",
				Checks: []string{"反馈注明对应标准及处理结论", "待跟进事项有负责人"}},
		}, Review: "核对冻结的标准、候选提交、用例结果和演示反馈，明确接收本次增量或说明未达标项。", ReworkStep: 2}
}

func evidenceGatedAgile() Template {
	m := legacyAgile()
	m.Revision++
	m.Steps = []Step{
		{Gate: "criteria", Name: "定义增量与验收标准", Inputs: "用户故事、交付入口和范围", Instructions: "在本事项逐条登记带稳定编号的验收标准。写清触发条件、预期变化、边界和验证入口。调整同一条标准时沿用编号，保留旧版本。", Output: "可从事项读取的标准编号、版本和本轮边界。", Checks: []string{"标准记录可按编号查询", "每条标准都有可观察的成功条件"}},
		{Gate: "coverage", Name: "设计测试用例", Inputs: "已评审的标准编号和版本", Instructions: "在每条标准下登记可复现的用例，给出前置条件、操作与预期结果，覆盖正常与适用的异常路径。变更标准时核查已有用例和需补测范围。", Output: "标准到测试用例的一对多清单和覆盖说明。", Checks: []string{"每条标准至少关联一条测试用例", "执行者能按用例复现并判断结果"}},
		{Gate: "candidate", Name: "实现并验证候选提交", Inputs: "验收标准、关联用例和开发仓库", Instructions: "实现后逐条登记仓库、分支与完整提交 SHA。每条标准以最近登记的当前版本实现为候选，在该提交上运行全部关联用例，登记通过或失败及可复核证据。更换候选后重新验证。", Output: "标准、用例、实现分支、候选提交与实测结果的对应记录。", Checks: []string{"每条标准均关联当前版本的仓库、分支和提交", "每条关联用例均在对应候选提交上通过且有证据"}},
		{Gate: "delivery", Name: "演示与独立终验", Inputs: "已验证的候选版本、用例结果与交付入口", Instructions: "从实际入口复验用户故事，沿标准编号反查测试记录和实现提交。写明采纳或退回的理由。终验通过后事项自动完成。", Output: "交付入口、独立验收结论、遗留问题和跟进人。", Checks: []string{"交付入口可体验且证据对应本次候选提交", "终验记录包含结论和需要跟进的事项"}},
	}
	m.Review = "逐项核对当前标准、关联用例、候选提交及实测结果，再记录独立验收决定。"
	return m
}

func agileTemplate() Template {
	m := legacyAgile()
	m.Revision = 3
	m.Steps = []Step{
		{
			Gate: "criteria", Name: "定义增量与验收标准",
			Inputs:       "用户故事、真实业务场景、现有规则与交付入口",
			Instructions: "请用业务方的词讲清谁在什么条件下做什么、谁确认结果。区分履约权责和待求解的领域规则，优先复用已有模型。同一术语在不同角色下含义不同时，标出各自适用的上下文。涉及跨角色履约时，用请求与确认凭证串起成功和拒绝路径。简单改动写清受影响的一条规则即可。",
			Output:       "逐条登记带稳定编号和版本的验收标准，写明业务术语、规则边界和可观察的结果。修改标准时沿用编号。",
			Checks:       []string{"按标准编号可找到受益角色、触发条件和可观察结果", "用正例和反例解释规则边界，同名异义或跨角色交接处已说明归属"},
		},
		{
			Gate: "coverage", Name: "设计测试用例",
			Inputs:       "当前版本的验收标准、业务规则与已有测试",
			Instructions: "每条标准关联可复现的用例，写明前置状态、操作和从哪个入口观察结果。挑一个看似合理却违反规则的实现，选能识别它的输入和断言；有行为分界时选两侧最接近的例子。标出哪些是新增行为，哪些是已有行为的回归。修改旧代码而缺少测试时，先用表征测试记录需要保留的实际表现，再单列本次要改变的行为。",
			Output:       "按标准编号和版本排列的用例、预期结果、可识别的错误以及需要保留的行为基线。",
			Checks:       []string{"每条标准关联至少一条有前置条件、操作和独立预期的用例", "能指出哪条断言会识别具体错误，适用的边界两侧均有样例", "区分新增与既有行为，涉及遗留代码时写明保留和改变的范围"},
		},
		{
			Gate: "candidate", Name: "实现并验证候选提交",
			Inputs:       "已验收用例、行为基线、开发仓库与测试入口",
			Instructions: "一次选一条新增行为。先运行测试并核对预期值、实际值和失败断言；若编译或环境失败，先排除阻碍再观察。用最小改动使它通过，跑相关回归，在绿灯下重构并复跑。已有行为的用例首次通过时说明它保护的规则。按用例保留基线、命令与红绿输出；涉及外部依赖时分别验证核心规则、真实适配器和交付配置。为每条标准登记仓库、分支、完整提交 SHA，并在当前候选上登记全部关联用例的实测结果。",
			Output:       "按用例可复核的失败断言或已有行为基线、修复及重构后的运行证据，以及候选分支和提交。",
			Checks:       []string{"新增行为有实现前的目标断言失败证据，已有行为有基线和保留理由", "最小实现及后续重构均有对应的回归结果，外部依赖的验证范围明确", "每条标准的当前版本已关联仓库、分支和完整提交，全部关联用例在该候选上有通过证据"},
		},
		{
			Gate: "delivery", Name: "演示与独立终验",
			Inputs:       "交付入口、业务场景、候选提交与用例证据",
			Instructions: "验收人从实际入口复演一条关键规则及其边界反例，用与标准一致的业务术语核对结果。按标准编号追到用例、真实执行记录和当前提交。涉及跨系统交接时，核查请求与确认能相互追溯；用到替身时，抽查真实依赖的装配证据并标明未验证范围。指出缺口对应的标准、用例和负责步骤，退回补正，或逐项批准后完成。",
			Output:       "可体验入口、正反例实测结果、可回查的候选提交、独立结论及遗留事项负责人。",
			Checks:       []string{"用业务术语从交付入口复验关键规则和反例，可反查用例与本次提交", "验收结论写明已验证范围和遗留问题，需补正的记录有标准编号及负责步骤"},
		},
	}
	m.Review = "按当前标准版本和候选提交逐项核验。业务词义或权责不清退回定义标准，断言无法识别错误退回用例设计，红绿证据或真实依赖验证缺失退回候选实现。记录独立结论后完成。"
	return m
}

func upgradeAgile(m Template) Template {
	current := agileTemplate()
	for _, previous := range []Template{legacyAgile(), evidenceGatedAgile()} {
		if m.Kind != previous.Kind || len(m.Steps) != len(previous.Steps) {
			continue
		}
		for i, s := range m.Steps {
			previous.Steps[i].Owner, previous.Steps[i].Reviewer = s.Owner, s.Reviewer
		}
		if !reflect.DeepEqual(m.Steps, previous.Steps) {
			continue
		}
		for i, s := range m.Steps {
			current.Steps[i].Owner, current.Steps[i].Reviewer = s.Owner, s.Reviewer
		}
		m.Steps = current.Steps
		if m.Review == previous.Review {
			m.Review = current.Review
		}
		// Workspace revisions include local edits such as role assignments.
		m.Revision += current.Revision - previous.Revision
		return m
	}
	return m
}

func (r Run) EvidenceGate() string {
	if r.Method == nil || r.Method.Kind != "development" || r.Step < 0 || r.Step >= len(r.Method.Steps) {
		return ""
	}
	return r.Method.Steps[r.Step].Gate
}
