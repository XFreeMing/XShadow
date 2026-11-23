package workmethod

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
)

const AdoptRevision = "adopt_revision"

// AcceptsTaskRevision keeps a task bound to its handoff across explicit guidance
// updates. An actual transition, even back to the same step, ends that binding.
func (r Run) AcceptsTaskRevision(revision int) bool {
	if revision == r.Revision {
		return true
	}
	delta := r.Revision - revision
	if revision < 1 || delta < 1 || delta > len(r.History) || r.State != "working" {
		return false
	}
	for _, event := range r.History[len(r.History)-delta:] {
		if event.Action != AdoptRevision || event.Step != r.Step {
			return false
		}
	}
	return true
}

func (r *Run) Adopt(c Change, catalog []Template, actor, at string) error {
	if c.Action != AdoptRevision || c.BaseRevision == nil || *c.BaseRevision < 1 || c.MethodRevision == nil || *c.MethodRevision < 1 || c.MethodID == "" ||
		c.TargetStep != nil || len(c.CheckedItems) != 0 || !validText(c.Evidence, 12000) {
		return errors.New("采用新版须提供实例修订、方法编号、目标模板修订及变更依据")
	}
	if r.State != "working" || r.Method == nil || r.Step < 0 || r.Step >= len(r.Method.Steps) || c.MethodID != r.Method.ID {
		return fmt.Errorf("%w：仅可在当前方法执行中采用同一方法的新版", ErrConflict)
	}
	i := slices.IndexFunc(catalog, func(m Template) bool { return m.ID == c.MethodID })
	if i < 0 || catalog[i].Revision != *c.MethodRevision || *c.MethodRevision < r.Method.Revision {
		return ErrConflict
	}
	if *c.BaseRevision != r.Revision {
		if *c.MethodRevision == r.Method.Revision && *c.BaseRevision == r.Revision-1 && r.AcceptsTaskRevision(*c.BaseRevision) {
			return nil
		}
		return ErrConflict
	}
	if *c.MethodRevision == r.Method.Revision {
		return nil
	}
	next := catalog[i]
	if err := Validate(next); err != nil {
		return err
	}
	old := r.Method
	if next.Kind != old.Kind || next.ReworkStep != old.ReworkStep || len(next.Steps) != len(old.Steps) {
		return fmt.Errorf("%w：工作方法的类型、步骤数或返工目标发生改变", ErrConflict)
	}
	for i, prior := range old.Steps {
		s := next.Steps[i]
		if prior.Name != s.Name || prior.Gate != s.Gate || prior.SelfClaim != s.SelfClaim || !reflect.DeepEqual(prior.Owner, s.Owner) ||
			!reflect.DeepEqual(prior.Reviewer, s.Reviewer) || len(prior.Checks) != len(s.Checks) {
			return fmt.Errorf("%w：第 %d 步的顺序、门槛、分工或验收项结构不兼容", ErrConflict, i+1)
		}
	}
	r.Method = snapshot(next)
	r.History = append(r.History, Event{Action: AdoptRevision, Step: r.Step, Actor: actor, At: at,
		PreviousMethod: old, MethodRevision: next.Revision, Evidence: strings.TrimSpace(c.Evidence)})
	r.Revision++
	return nil
}
