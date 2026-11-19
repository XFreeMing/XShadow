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

func agileTemplate() Template {
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

func upgradeAgile(m Template) Template {
	legacy := legacyAgile()
	if m.Kind != legacy.Kind || len(m.Steps) != len(legacy.Steps) {
		return m
	}
	for i, s := range m.Steps {
		s.Owner, s.Reviewer = nil, nil
		if !reflect.DeepEqual(s, legacy.Steps[i]) {
			return m
		}
	}
	updated := agileTemplate()
	for i := range updated.Steps {
		updated.Steps[i].Owner = m.Steps[i].Owner
		updated.Steps[i].Reviewer = m.Steps[i].Reviewer
	}
	m.Steps = updated.Steps
	if m.Review == legacy.Review {
		m.Review = updated.Review
	}
	m.Revision++
	return m
}

func (r Run) EvidenceGate() string {
	if r.Method == nil || r.Method.Kind != "development" || r.Step < 0 || r.Step >= len(r.Method.Steps) {
		return ""
	}
	return r.Method.Steps[r.Step].Gate
}
