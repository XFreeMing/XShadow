package scheduler

import (
	"context"
	"time"
)

type IssueWakeupDispatcher interface{ Tick(context.Context) error }

// Receipts and due times persist independently of the scheduler's plan bucket.
// A missed bucket therefore cannot drop a one-shot wakeup.
func IssueWakeupJob(dispatcher IssueWakeupDispatcher) JobSpec {
	return issueDispatchJob("issue_wakeup_dispatch", dispatcher)
}

func IssueContinuationJob(dispatcher IssueWakeupDispatcher) JobSpec {
	return issueDispatchJob("issue_continuation", dispatcher)
}

func issueDispatchJob(name string, dispatcher IssueWakeupDispatcher) JobSpec {
	return JobSpec{
		Name: name, Cadence: 30 * time.Second, CatchUpMode: CatchUpLatestOnly, CatchUpWindow: time.Hour,
		RunTimeout: 45 * time.Second, StaleTimeout: time.Minute, HeartbeatInterval: 10 * time.Second,
		AllowStaleReentry: true, MaxAttempts: 1, Scopes: StaticScopes(ScopeGlobal),
		Handler: func(ctx context.Context, _ HandlerInput) (HandlerResult, error) {
			return HandlerResult{}, dispatcher.Tick(ctx)
		},
	}
}
