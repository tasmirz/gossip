package coord

import (
	"context"
	"errors"
	"time"
)

const maxEventWait = 25 * time.Second

type EventWaitResult struct {
	Events   []Event `json:"events"`
	TimedOut bool    `json:"timed_out"`
}

// waitEvents holds one adapter call open until visible events arrive, the
// bounded wait expires, or the caller cancels. It never advances a checkpoint.
func waitEvents(ctx context.Context, backend Backend, project, token string, after int64, limit int, wait time.Duration) (EventWaitResult, error) {
	var result EventWaitResult
	if after < 0 || limit < 1 || limit > 200 || wait < 0 || wait > maxEventWait {
		return result, errors.New("invalid event cursor, limit, or wait duration")
	}
	events, err := backend.ReadEvents(ctx, project, token, after, limit)
	if err != nil || len(events) > 0 || wait == 0 {
		result.Events = events
		return result, err
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-timer.C:
			result.TimedOut = true
			return result, nil
		case <-ticker.C:
			events, err = backend.ReadEvents(ctx, project, token, after, limit)
			if err != nil {
				return result, err
			}
			if len(events) > 0 {
				result.Events = events
				return result, nil
			}
		}
	}
}
