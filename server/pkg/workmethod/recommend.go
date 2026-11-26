package workmethod

import (
	"fmt"
	"slices"
	"strings"
)

type match struct {
	method Template
	word   string
	score  int
}

// Whole-word boundaries apply to Latin keywords. Chinese keywords often appear
// adjacent to an object, so "调研用户需求" must still match "调研".
func wordScore(text, word string) int {
	word = strings.ToLower(strings.TrimSpace(word))
	if word == "" {
		return 0
	}
	text = strings.ToLower(strings.TrimSpace(text))
	isASCIIWord := func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '_' }
	for i := 0; i < len(text); {
		offset := strings.Index(text[i:], word)
		if offset < 0 {
			break
		}
		start := i + offset
		end := start + len(word)
		if (start == 0 || !isASCIIWord(word[0]) || !isASCIIWord(text[start-1])) && (end == len(text) || !isASCIIWord(word[len(word)-1]) || !isASCIIWord(text[end])) {
			if start == 0 {
				// A title explicitly asking for two deliverables must not be
				// decided by which one is mentioned first.
				tail := strings.TrimSpace(text[end:])
				if !strings.HasPrefix(tail, "与") && !strings.HasPrefix(tail, "和") && !strings.HasPrefix(tail, "及") && !strings.HasPrefix(tail, "、") && !strings.HasPrefix(tail, "and ") {
					return 2
				}
			}
			return 1
		}
		i = end
	}
	return 0
}

func matchMethods(methods []Template, input string) []match {
	var result []match
	best := 0
	for _, m := range methods {
		if IsRetired(m.ID) {
			continue
		}
		pick := match{method: m}
		for _, w := range m.Keywords {
			score := wordScore(input, w)
			if score > pick.score || score > 0 && score == pick.score && len(w) > len(pick.word) {
				pick.score, pick.word = score, w
			}
		}
		if pick.score == 0 || pick.score < best {
			continue
		}
		if pick.score > best {
			best = pick.score
			result = nil
		}
		result = append(result, pick)
	}
	return result
}

func taskOutcome(body string) string {
	lines := strings.Split(body, "\n")
	var outcomes []string
	for i, line := range lines {
		s := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#*- "))
		label, value, ok := strings.Cut(strings.ReplaceAll(s, "：", ":"), ":")
		if !ok {
			label = s
			value = ""
		}
		switch strings.ToLower(strings.TrimSpace(label)) {
		case "交付物", "预期产出", "预期结果", "任务目标", "交付目标", "交付结果", "成果", "deliverable", "expected outcome", "expected result", "objective":
			if strings.TrimSpace(value) == "" && i+1 < len(lines) {
				value = lines[i+1]
			}
			outcomes = append(outcomes, strings.TrimSpace(value))
		default:
			if strings.HasPrefix(s, "作为") || strings.HasPrefix(strings.ToLower(s), "as a ") {
				outcomes = append(outcomes, s)
			} else if i == 0 {
				for _, prefix := range []string{"请", "需要", "希望", "please "} {
					if strings.HasPrefix(strings.ToLower(s), prefix) {
						outcomes = append(outcomes, strings.TrimSpace(s[len(prefix):]))
						break
					}
				}
			}
		}
	}
	return strings.Join(outcomes, "\n")
}

func RecommendForIssueType(methods []Template, issueTypeID, taskTitle, taskBody, goalText string) Run {
	if issueTypeID != "" {
		for _, method := range methods {
			if !IsRetired(method.ID) && method.UsesIssueType(issueTypeID) {
				return Run{State: "ready", Source: "issue_type", Method: snapshot(method), History: []Event{},
					Reason: "任务类型使用「" + method.Name + "」。"}
			}
		}
	}
	return Recommend(methods, taskTitle, taskBody, goalText)
}

func (m Template) UsesIssueType(id string) bool {
	return (m.PlanTask != nil && m.PlanTask.IssueTypeID == id) || slices.ContainsFunc(m.Steps, func(step Step) bool {
		return step.TableGate != nil && step.TableGate.IssueTypeID == id
	})
}

// ResolvePlanMethod maps a plan's template to its execution definition.
// Method IDs remain readable for plans saved before task templates.
func ResolvePlanMethod(methods []Template, templateID, legacyID string) (Template, bool) {
	for _, method := range methods {
		if templateID != "" {
			if method.PlanTask != nil && method.PlanTask.IssueTypeID == templateID {
				return method, true
			}
		} else if method.ID == legacyID {
			return method, true
		}
	}
	return Template{}, false
}

func Recommend(methods []Template, taskTitle, taskBody, goalText string) Run {
	ready := Run{State: "ready", History: []Event{}, Reason: "尚无明确的目标或交付物匹配，可手动选择工作方法。"}
	// Avoid converting background examples, test logs and implementation notes
	// embedded in a task description into a different work method.
	sources := []struct{ key, label, text string }{
		{"title", "事项标题", taskTitle}, {"deliverable", "事项交付物", taskOutcome(taskBody)}, {"goal", "关联目标", goalText},
	}
	for _, source := range sources {
		text := strings.TrimSpace(source.text)
		matches := matchMethods(methods, text)
		switch len(matches) {
		case 0:
			continue
		case 1:
			res := matches[0]
			ready.Method = snapshot(res.method)
			ready.Source = source.key
			ready.Reason = fmt.Sprintf("%s匹配“%s”，选用「%s」。", source.label, res.word, res.method.Name)
		default:
			names := make([]string, 0, len(matches))
			for _, res := range matches {
				names = append(names, res.method.Name)
			}
			slices.Sort(names)
			ready.Reason = fmt.Sprintf("%s同时匹配%s，请选择本次交付所用的方法。", source.label, strings.Join(names, "、"))
		}
		return ready
	}
	return ready
}
