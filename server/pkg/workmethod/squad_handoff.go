package workmethod

import "fmt"

type SquadHandoff struct {
	TaskID      string `json:"task_id"`
	FromSquadID string `json:"from_squad_id"`
	ToSquadID   string `json:"to_squad_id"`
	FromStep    int    `json:"from_step"`
	ToStep      int    `json:"to_step"`
	RunRevision int    `json:"run_revision"`
}

// RecordSquadHandoff links the method decision to the receiving squad's
// queue entry. The caller commits the method and queue entry together.
func (r *Run) RecordSquadHandoff(before Run, taskID string) *SquadHandoff {
	if r.CurrentActor == nil || r.CurrentActor.Kind != "squad" ||
		r.Revision != before.Revision+1 || len(r.History) == 0 ||
		(before.State != "working" && before.State != "review") {
		return nil
	}
	event := &r.History[len(r.History)-1]
	switch event.Action {
	case "advance", "approve", "rework":
	default:
		return nil
	}
	from := before.CurrentActor
	if (from == nil || from.Kind != "squad") && event.Action == "approve" &&
		before.State == "review" && before.Method != nil && before.Step >= 0 && before.Step < len(before.Method.Steps) {
		// An individual reviewer releases the delivering squad's approved
		// output. The reviewer's identity stays in Event.Actor.
		from = before.Method.Steps[before.Step].Owner
	}
	if from == nil || from.Kind != "squad" || from.ID == r.CurrentActor.ID {
		return nil
	}
	event.SquadHandoff = &SquadHandoff{
		TaskID: taskID, FromSquadID: from.ID, ToSquadID: r.CurrentActor.ID,
		FromStep: before.Step, ToStep: r.Step, RunRevision: r.Revision,
	}
	return event.SquadHandoff
}

func (h SquadHandoff) Brief() string {
	return fmt.Sprintf("Squad handoff event for task %s: squad %s, step %d → squad %s, step %d (run revision %d). Read this issue's method history for the verified deliverable and review evidence.",
		h.TaskID, h.FromSquadID, h.FromStep+1, h.ToSquadID, h.ToStep+1, h.RunRevision)
}
