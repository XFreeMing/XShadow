package workmethod

// Builtins returns independently editable values. A running issue keeps its own snapshot.
func Builtins() []Template {
	return []Template{
		{Revision: 1, ID: "agile", Name: "敏捷研发", Kind: "development", Keywords: []string{"研发", "开发", "实现", "修复", "交付功能", "交付软件", "开发功能", "发布版本", "缺陷修复", "软件迭代", "软件增量", "implement", "develop", "fix bug", "fix the bug", "ship a feature", "software delivery", "agile"},
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
			}, Review: "核对冻结的标准、候选提交、用例结果和演示反馈，明确接收本次增量或说明未达标项。", ReworkStep: 2},
		{Revision: 1, ID: "seven-step", Name: "麦肯锡七步问题解决法", Kind: "research", Keywords: []string{"调研", "研究", "调查", "可行性分析", "决策建议", "研究报告", "竞争分析", "竞品分析", "比较方案", "分析原因", "问题诊断", "research", "investigate", "feasibility study", "compare options", "root cause", "mckinsey"},
			Steps: []Step{
				{Name: "界定问题", Output: "写明需要作出的决策、受益者、范围、约束和判断依据。",
					Inputs: "决策者的原始诉求、背景与时限", Instructions: "与决策者确认本次要回答的问题，约定问题边界、成功条件及不可改变的约束。",
					Checks: []string{"问题能够表述为一个具体决策", "判断标准和决策时限已明确"}},
				{Name: "拆解问题", Output: "建立问题树，列出关键假设和需要验证的子问题。",
					Inputs: "已确认的问题定义与初步事实", Instructions: "将主问题拆成可检验的分支，标出各分支假设和可能推翻假设的证据。",
					Checks: []string{"每个末级问题可以通过观察或数据作答", "已检查重复分支和遗漏的主要因素"}},
				{Name: "确定优先级", Output: "根据影响、可控性和不确定性选出优先验证的分支。",
					Inputs: "问题树、资源上限与已知风险", Instructions: "比较各分支对决策的影响及验证成本，确定先分析的少数问题。",
					Checks: []string{"优先项附取舍依据", "暂缓项及其风险有记录"}},
				{Name: "制定研究计划", Output: "为优先问题确定方法、数据来源、负责人和时间。",
					Inputs: "优先问题与可获取的数据清单", Instructions: "为每个问题指定分析方法、取样范围、交付人、截止时间与备用证据来源。",
					Checks: []string{"每项分析关联一个待验证假设", "计划可在给定时间和资源内执行"}},
				{Name: "执行分析", Output: "逐项记录数据来源、分析结果、反例和局限。",
					Inputs: "研究计划、样本及原始材料", Instructions: "依据约定的方法采集与分析数据，记录出处、时间、样本口径及反例。",
					Checks: []string{"关键结果能追溯到原始材料", "样本偏差和缺失信息已说明"}},
				{Name: "综合发现", Output: "汇总有证据支持的结论，说明不确定性及其对决策的影响。",
					Inputs: "各分支结果、反例与研究局限", Instructions: "对照原问题整合发现，区分事实、推断与未决假设，说明不同解释如何影响选择。",
					Checks: []string{"每个主要结论引用对应证据", "不确定性及其决策影响已说明"}},
				{Name: "沟通建议", Output: "交付建议、备选方案和行动计划，记录决策者反馈。",
					Inputs: "综合结论、备选方案及决策约束", Instructions: "向决策者说明推荐行动、代价与风险，记录采纳或保留意见，以及下一次跟进安排。",
					Checks: []string{"建议回答最初的决策问题", "后续行动注明责任人及日期"}},
			}, Review: "核对问题、来源、推理与建议的对应关系，记录决策者反馈；证据不足时返回相应研究步骤。", ReworkStep: 4},
		{Revision: 1, ID: "okr", Name: "OKR 目标管理", Kind: "outcome", Keywords: []string{"okr", "关键结果", "达成率", "目标达成", "提升留存", "提高留存", "提升转化", "提高转化", "降低流失", "提升指标", "key result", "key results", "increase retention", "increase conversion", "reduce churn"},
			Steps: []Step{
				{Name: "明确目标", Output: "写明希望带来的结果、目标周期、受益者和负责人。",
					Inputs: "业务愿景、现状与目标周期", Instructions: "用结果描述期望改变的状态，与受益方确认边界、负责人及周期终点。",
					Checks: []string{"目标明确受益方和期望变化", "负责人及截止时间已确认"}},
				{Name: "定义关键结果", Output: "逐项登记可衡量的关键结果、基线、目标值和数据来源。",
					Inputs: "已确认的目标、历史指标和数据来源", Instructions: "选择能反映目标达成的指标，逐项写明基线、目标值、统计窗口和计量口径。",
					Checks: []string{"每个关键结果均有基线和目标值", "数据来源和统计口径可复查"}},
				{Name: "执行与检视", Output: "按周期登记实际进展，解释偏差并调整行动。",
					Inputs: "关键结果清单、周期计划与最新数据", Instructions: "按约定节奏更新实际值，排查偏差，为纠偏行动指定负责人和下次检视时间。",
					Checks: []string{"每个实际值附观测日期和来源", "偏离目标的结果有跟进行动"}},
				{Name: "计分与复盘", Output: "逐项对照基线、目标和实际结果，记录得分、原因及后续行动。",
					Inputs: "全部观测值、评分规则和行动记录", Instructions: "用相同口径核对期末实际值与目标，解释差距并确认下一周期采用的改进。",
					Checks: []string{"评分可由登记的数据重算", "未达成项附原因和下周期安排"}},
			}, Review: "复核关键结果的数据来源、口径和达成情况，确认本周期结论及下一周期行动。", ReworkStep: 2},
	}
}

// Catalog overlays workspace definitions by stable method ID while preserving display order.
func Catalog(stored []Template) []Template {
	methods := Builtins()
	byID := make(map[string]int, len(methods)+len(stored))
	for i, m := range methods {
		byID[m.ID] = i
	}
	for _, m := range stored {
		if m.Revision == 0 {
			m.Revision = 1
		}
		if i, ok := byID[m.ID]; ok {
			methods[i] = m
		} else {
			byID[m.ID] = len(methods)
			methods = append(methods, m)
		}
	}
	return methods
}
