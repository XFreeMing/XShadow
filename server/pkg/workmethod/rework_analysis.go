package workmethod

// ReworkCycle identifies repeated returns from one review before it passes.
type ReworkCycle struct {
	ReviewStep int
	FirstIndex int
	First      Event
	Latest     Event
}

func (r Run) RepeatedRework() (ReworkCycle, bool) {
	if r.Method == nil || r.Method.Kind != "development" || len(r.History) < 2 {
		return ReworkCycle{}, false
	}
	latestIndex := len(r.History) - 1
	latest := r.History[latestIndex]
	if latest.Action != "rework" || latest.FromStep == nil {
		return ReworkCycle{}, false
	}
	reviewStep := *latest.FromStep
	if reviewStep < 0 || reviewStep >= len(r.Method.Steps) {
		return ReworkCycle{}, false
	}
	first := latestIndex
	for i := latestIndex - 1; i >= 0; i-- {
		event := r.History[i]
		if event.Action == AdoptRevision || (event.Action == "approve" && event.Step == reviewStep) {
			break
		}
		if event.Action == "rework" && event.FromStep != nil && *event.FromStep == reviewStep {
			first = i
		}
	}
	if first == latestIndex {
		return ReworkCycle{}, false
	}
	return ReworkCycle{ReviewStep: reviewStep, FirstIndex: first, First: r.History[first], Latest: latest}, true
}
