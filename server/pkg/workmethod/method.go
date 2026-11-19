// Package workmethod owns goal-based method selection and issue-level delivery reviews.
package workmethod

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"
)

type Template struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Keywords   []string `json:"keywords"`
	Steps      []Step   `json:"steps"`
	Review     string   `json:"review"`
	ReworkStep int      `json:"rework_step"`
}

type Step struct {
	Name   string `json:"name"`
	Output string `json:"output"`
}

type Run struct {
	Revision int       `json:"revision"`
	State    string    `json:"state"`
	Method   *Template `json:"method"`
	Step     int       `json:"step"`
	Reason   string    `json:"reason"`
	History  []Event   `json:"history"`
}

type Event struct {
	Action   string `json:"action"`
	Step     int    `json:"step"`
	Evidence string `json:"evidence"`
	Actor    string `json:"actor"`
	At       string `json:"at"`
}

type Change struct {
	Action       string `json:"action"`
	BaseRevision *int   `json:"base_revision"`
	MethodID     string `json:"method_id,omitempty"`
	Evidence     string `json:"evidence,omitempty"`
	TargetStep   *int   `json:"target_step,omitempty"`
}

var (
	ErrConflict = errors.New("工作方法记录已更新，请重新读取")
	methodID    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$`)
)

func validText(s string, limit int) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= limit
}

func Validate(m Template) error {
	if !methodID.MatchString(m.ID) {
		return errors.New("方法编号无效")
	}
	if !validText(m.Name, 100) {
		return errors.New("方法名称须为 1 到 100 个字符")
	}
	switch m.Kind {
	case "development", "research", "outcome":
	default:
		return errors.New("未知任务类型")
	}
	if len(m.Keywords) > 60 {
		return errors.New("匹配词最多 60 个")
	}
	for _, word := range m.Keywords {
		if !validText(word, 100) {
			return errors.New("匹配词须为 1 到 100 个字符")
		}
	}
	if len(m.Steps) < 1 || len(m.Steps) > 32 {
		return errors.New("方法须包含 1 到 32 个交付步骤")
	}
	for i, step := range m.Steps {
		if !validText(step.Name, 100) || !validText(step.Output, 2000) {
			return fmt.Errorf("第 %d 步须填写名称和交付要求", i+1)
		}
	}
	if !validText(m.Review, 2000) {
		return errors.New("须说明最终评审依据")
	}
	if m.ReworkStep < 0 || m.ReworkStep >= len(m.Steps) {
		return errors.New("返工步骤超出方法范围")
	}
	return nil
}

func snapshot(m Template) *Template {
	m.Steps = slices.Clone(m.Steps)
	m.Keywords = append([]string{}, m.Keywords...)
	return &m
}

func (r *Run) Apply(c Change, methods []Template, actor, at string) error {
	if c.BaseRevision == nil {
		return errors.New("base_revision is required")
	}
	if *c.BaseRevision != r.Revision {
		return ErrConflict
	}
	if r.State == "closed" {
		return errors.New("方法已完成评审并关闭")
	}
	if !validText(actor, 500) || strings.TrimSpace(at) == "" {
		return errors.New("须记录操作人与操作时间")
	}
	if c.Action != "select" && c.MethodID != "" {
		return errors.New("只有选择方法时可指定 method_id")
	}
	if c.Action != "rework" && c.TargetStep != nil {
		return errors.New("只有退回时可指定返工步骤")
	}
	evidence := strings.TrimSpace(c.Evidence)
	if c.Action != "select" && !validText(evidence, 12000) {
		return errors.New("须填写本次交付、评审或退回的证据，最多 12000 个字符")
	}
	event := Event{Action: c.Action, Step: r.Step, Evidence: evidence, Actor: actor, At: at}
	switch c.Action {
	case "select":
		if r.State != "ready" && r.State != "working" {
			return errors.New("当前阶段不能更换方法")
		}
		for _, e := range r.History {
			if e.Action != "select" {
				return errors.New("已提交步骤产物，方法版本已冻结")
			}
		}
		idx := slices.IndexFunc(methods, func(m Template) bool { return m.ID == c.MethodID })
		if idx < 0 {
			return errors.New("找不到所选工作方法")
		}
		m := methods[idx]
		if err := Validate(m); err != nil {
			return err
		}
		r.Method = snapshot(m)
		r.State, r.Step, r.Reason = "working", 0, "手动选择「"+m.Name+"」"
		event.Evidence = r.Reason
	case "advance":
		if (r.State != "working" && r.State != "ready") || r.Method == nil {
			return errors.New("请先选择方法，或按当前评审状态操作")
		}
		if r.Step < 0 || r.Step >= len(r.Method.Steps) {
			return errors.New("当前步骤无效")
		}
		if r.Step+1 == len(r.Method.Steps) {
			r.State = "review"
		} else {
			r.Step++
			r.State = "working"
		}
	case "approve":
		if r.State != "review" || r.Method == nil {
			return errors.New("须先完成每个步骤，再进行最终评审")
		}
		r.State = "closed"
	case "rework":
		if r.State != "review" || r.Method == nil {
			return errors.New("只有待评审的方法可退回")
		}
		target := r.Method.ReworkStep
		if c.TargetStep != nil {
			target = *c.TargetStep
		}
		if target < 0 || target >= len(r.Method.Steps) {
			return errors.New("返工步骤超出方法范围")
		}
		r.State, r.Step = "working", target
		event.Step = target
	default:
		return errors.New("未知工作方法操作")
	}
	r.History = append(r.History, event)
	r.Revision++
	return nil
}
