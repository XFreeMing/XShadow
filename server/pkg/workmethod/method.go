// Package workmethod owns goal-based method selection and issue-level delivery reviews.
package workmethod

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

type Template struct {
	ID         string   `json:"id"`
	Revision   int      `json:"revision"`
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Keywords   []string `json:"keywords"`
	Steps      []Step   `json:"steps"`
	Review     string   `json:"review"`
	ReworkStep int      `json:"rework_step"`
}

type Actor struct {
	Kind string `json:"kind"`
	ID   string `json:"id,omitempty"`
}

type Step struct {
	Gate         string   `json:"acceptance_gate,omitempty"`
	Name         string   `json:"name"`
	Output       string   `json:"output"`
	Instructions string   `json:"instructions,omitempty"`
	Inputs       string   `json:"inputs,omitempty"`
	Checks       []string `json:"checks,omitempty"`
	Owner        *Actor   `json:"owner,omitempty"`
	Reviewer     *Actor   `json:"reviewer,omitempty"`
}

type Run struct {
	GoalExecution bool      `json:"goal_execution,omitempty"`
	CurrentActor  *Actor    `json:"current_actor,omitempty"`
	Source        string    `json:"source,omitempty"`
	Revision      int       `json:"revision"`
	State         string    `json:"state"`
	Method        *Template `json:"method"`
	Step          int       `json:"step"`
	Reason        string    `json:"reason"`
	History       []Event   `json:"history"`
}

type Event struct {
	PreviousMethod *Template `json:"previous_method,omitempty"`
	MethodRevision int       `json:"method_revision,omitempty"`
	CheckedItems   []int     `json:"checked_items,omitempty"`
	FromStep       *int      `json:"from_step,omitempty"`
	Action         string    `json:"action"`
	Step           int       `json:"step"`
	Evidence       string    `json:"evidence"`
	Actor          string    `json:"actor"`
	At             string    `json:"at"`
}

type Change struct {
	MethodRevision *int   `json:"method_revision,omitempty"`
	CheckedItems   []int  `json:"checked_items,omitempty"`
	Action         string `json:"action"`
	BaseRevision   *int   `json:"base_revision"`
	MethodID       string `json:"method_id,omitempty"`
	Evidence       string `json:"evidence,omitempty"`
	TargetStep     *int   `json:"target_step,omitempty"`
}

var (
	ErrConflict = errors.New("工作方法记录已更新，请重新读取")
	methodID    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,79}$`)
)

func validText(s string, limit int) bool {
	return strings.TrimSpace(s) != "" && utf8.RuneCountInString(s) <= limit
}

func validActor(a *Actor) bool {
	switch a.Kind {
	case "creator", "project_lead":
		return a.ID == ""
	case "member", "agent", "squad":
		id, err := uuid.Parse(a.ID)
		return err == nil && len(a.ID) == 36 && id != uuid.Nil
	}
	return false
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
		switch step.Gate {
		case "":
		case "criteria", "coverage", "candidate", "delivery":
			if m.Kind != "development" {
				return errors.New("研发验收门槛仅适用于研发方法")
			}
		default:
			return errors.New("未知验收门槛")
		}
		if !validText(step.Name, 100) || !validText(step.Output, 2000) {
			return fmt.Errorf("第 %d 步须填写名称和交付要求", i+1)
		}
		if utf8.RuneCountInString(step.Inputs) > 4000 || utf8.RuneCountInString(step.Instructions) > 4000 {
			return fmt.Errorf("第 %d 步的输入与执行指南各限 4000 个字符", i+1)
		}
		if len(step.Checks) > 32 {
			return fmt.Errorf("第 %d 步最多可设 32 条验收要点", i+1)
		}
		for _, check := range step.Checks {
			if !validText(check, 1000) {
				return fmt.Errorf("第 %d 步的每条验收要点须为 1 到 1000 个字符", i+1)
			}
		}
		if (step.Owner == nil) != (step.Reviewer == nil) ||
			(step.Owner != nil && (!validActor(step.Owner) || !validActor(step.Reviewer))) {
			return fmt.Errorf("第 %d 步须同时指定有效的执行人和验收人", i+1)
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
	for i := range m.Steps {
		step := &m.Steps[i]
		step.Checks = slices.Clone(step.Checks)
		if step.Owner != nil {
			a := *step.Owner
			step.Owner = &a
		}
		if step.Reviewer != nil {
			a := *step.Reviewer
			step.Reviewer = &a
		}
	}
	if m.Revision == 0 {
		m.Revision = 1
	}
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
	if c.Action != "select" && c.Action != "start" && c.MethodID != "" {
		return errors.New("只有选择或开始方法时可指定 method_id")
	}
	if c.MethodRevision != nil && c.Action != "select" {
		if r.State != "ready" || (c.Action != "advance" && c.Action != "start") {
			return errors.New("只有首次采用方法时可以指定模板版本")
		}
		if r.Method == nil || r.Method.Revision != *c.MethodRevision {
			return ErrConflict
		}
	}
	if c.Action != "rework" && c.TargetStep != nil {
		return errors.New("只有退回时可指定返工步骤")
	}
	evidence := strings.TrimSpace(c.Evidence)
	if c.Action != "select" && c.Action != "start" && !validText(evidence, 12000) {
		return errors.New("须填写本次交付、评审或退回的证据，最多 12000 个字符")
	}
	event := Event{Action: c.Action, Step: r.Step, Evidence: evidence, Actor: actor, At: at}
	if c.Action == "approve" || c.Action == "rework" {
		if r.State != "review" || r.Method == nil || r.Step < 0 || r.Step >= len(r.Method.Steps) {
			return errors.New("当前步骤尚未提交评审")
		}
		step := r.Method.Steps[r.Step]
		passed := make([]bool, len(step.Checks))
		for _, id := range c.CheckedItems {
			if id < 0 || id >= len(passed) || passed[id] {
				return errors.New("验收项序号重复或超出本步骤范围")
			}
			passed[id] = true
		}
		if c.Action == "approve" && step.Reviewer != nil && len(c.CheckedItems) != len(step.Checks) {
			return errors.New("通过本步前须逐项确认验收清单")
		}
		event.CheckedItems = slices.Clone(c.CheckedItems)
	} else if len(c.CheckedItems) > 0 {
		return errors.New("只有评审时可以勾选验收项")
	}
	switch c.Action {
	case "start":
		if r.State != "ready" {
			return errors.New("只有待开始的推荐方法可以开始")
		}
		if c.MethodID != "" && (r.Method == nil || r.Method.ID != c.MethodID) {
			return ErrConflict
		}
		if r.Method == nil {
			return errors.New("请先选择工作方法")
		}
		r.State = "working"
		event.Evidence = r.Reason
	case "select":
		if r.State != "ready" && r.State != "working" {
			return errors.New("当前阶段不能更换方法")
		}
		for _, e := range r.History {
			if e.Action != "select" {
				return errors.New("方法已开始执行，无法更换版本")
			}
		}
		idx := slices.IndexFunc(methods, func(m Template) bool { return m.ID == c.MethodID })
		if idx < 0 {
			return errors.New("找不到所选工作方法")
		}
		m := methods[idx]
		if c.MethodRevision != nil && m.Revision != *c.MethodRevision {
			return ErrConflict
		}
		if err := Validate(m); err != nil {
			return err
		}
		r.Method = snapshot(m)
		r.Source = "manual"
		r.State, r.Step, r.Reason = "working", 0, "手动选择「"+m.Name+"」"
		event.Evidence = r.Reason
	case "advance":
		if (r.State != "working" && r.State != "ready") || r.Method == nil {
			return errors.New("请先选择方法，或按当前评审状态操作")
		}
		if r.Step < 0 || r.Step >= len(r.Method.Steps) {
			return errors.New("当前步骤无效")
		}
		if r.Method.Steps[r.Step].Reviewer != nil || r.Step+1 == len(r.Method.Steps) {
			r.State = "review"
		} else {
			r.Step++
			r.State = "working"
		}
	case "approve":
		if r.Step+1 == len(r.Method.Steps) {
			r.State = "closed"
		} else {
			r.State = "working"
			r.Step++
		}
	case "rework":
		target := r.Step
		if r.Step+1 == len(r.Method.Steps) {
			target = r.Method.ReworkStep
		}
		if c.TargetStep != nil {
			target = *c.TargetStep
		}
		if target < 0 || target > r.Step {
			return errors.New("返工步骤超出方法范围")
		}
		from := r.Step
		event.FromStep = &from
		r.State, r.Step = "working", target
		event.Step = target
	default:
		return errors.New("未知工作方法操作")
	}
	r.History = append(r.History, event)
	r.Revision++
	return nil
}
