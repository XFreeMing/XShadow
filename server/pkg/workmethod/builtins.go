package workmethod

// Builtins returns independently editable values. A running issue keeps its own snapshot.
func Builtins() []Template {
	return []Template{
		{ID: "agile", Name: "敏捷研发", Kind: "development", Keywords: []string{"研发", "开发", "实现", "修复", "交付功能", "交付软件", "开发功能", "发布版本", "缺陷修复", "软件迭代", "软件增量", "implement", "develop", "fix bug", "fix the bug", "ship a feature", "software delivery", "agile"},
			Steps: []Step{
				{"定义增量", "明确用户场景、迭代目标，并列出可观察的验收标准。"},
				{"规划迭代", "确定本次交付范围、用例、依赖及负责人。"},
				{"实现并验证", "关联实现所在仓库、分支、提交和验收用例；登记执行证据。"},
				{"演示与回顾", "在交付入口复验每条标准，记录反馈和下一轮改进。"},
			}, Review: "核对冻结的标准、候选提交、用例结果和演示反馈，明确接收本次增量或说明未达标项。", ReworkStep: 2},
		{ID: "seven-step", Name: "麦肯锡七步问题解决法", Kind: "research", Keywords: []string{"调研", "研究", "调查", "可行性分析", "决策建议", "研究报告", "竞争分析", "竞品分析", "比较方案", "分析原因", "问题诊断", "research", "investigate", "feasibility study", "compare options", "root cause", "mckinsey"},
			Steps: []Step{
				{"界定问题", "写明需要作出的决策、受益者、范围、约束和判断依据。"},
				{"拆解问题", "建立问题树，列出关键假设和需要验证的子问题。"},
				{"确定优先级", "根据影响、可控性和不确定性选出优先验证的分支。"},
				{"制定研究计划", "为优先问题确定方法、数据来源、负责人和时间。"},
				{"执行分析", "逐项记录数据来源、分析结果、反例和局限。"},
				{"综合发现", "汇总有证据支持的结论，说明不确定性及其对决策的影响。"},
				{"沟通建议", "交付建议、备选方案和行动计划，记录决策者反馈。"},
			}, Review: "核对问题、来源、推理与建议的对应关系，记录决策者反馈；证据不足时返回相应研究步骤。", ReworkStep: 4},
		{ID: "okr", Name: "OKR 目标管理", Kind: "outcome", Keywords: []string{"okr", "关键结果", "达成率", "目标达成", "提升留存", "提高留存", "提升转化", "提高转化", "降低流失", "提升指标", "key result", "key results", "increase retention", "increase conversion", "reduce churn"},
			Steps: []Step{
				{"明确目标", "写明希望带来的结果、目标周期、受益者和负责人。"},
				{"定义关键结果", "逐项登记可衡量的关键结果、基线、目标值和数据来源。"},
				{"执行与检视", "按周期登记实际进展，解释偏差并调整行动。"},
				{"计分与复盘", "逐项对照基线、目标和实际结果，记录得分、原因及后续行动。"},
			}, Review: "复核关键结果的数据来源、口径和达成情况，确认本周期结论及下一周期行动。", ReworkStep: 2},
	}
}
