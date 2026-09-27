package manager

import (
	"context"
	"errors"
	"sync"
	"time"
)

type hierarchicalLimiter struct {
	mu sync.Mutex

	globalRate     float64
	downloadRate   float64
	globalTokens   float64
	downloadTokens map[string]float64
	last           time.Time
}

func newHierarchicalLimiter(globalBytesPerSecond, downloadBytesPerSecond float64) *hierarchicalLimiter {
	if globalBytesPerSecond <= 0 {
		globalBytesPerSecond = 1
	}
	if downloadBytesPerSecond <= 0 {
		downloadBytesPerSecond = globalBytesPerSecond
	}
	now := time.Now()
	return &hierarchicalLimiter{
		globalRate: globalBytesPerSecond, downloadRate: downloadBytesPerSecond,
		globalTokens: globalBytesPerSecond, downloadTokens: make(map[string]float64), last: now,
	}
}

func (l *hierarchicalLimiter) WaitN(ctx context.Context, downloadID string, bytes int64) error {
	if bytes < 0 {
		return errors.New("limiter bytes cannot be negative")
	}
	if bytes == 0 {
		return nil
	}
	for {
		l.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(l.last).Seconds()
		if elapsed > 0 {
			l.globalTokens = minFloat(l.globalRate, l.globalTokens+elapsed*l.globalRate)
			for id, tokens := range l.downloadTokens {
				l.downloadTokens[id] = minFloat(l.downloadRate, tokens+elapsed*l.downloadRate)
			}
			l.last = now
		}
		downloadTokens, ok := l.downloadTokens[downloadID]
		if !ok {
			downloadTokens = l.downloadRate
		}
		if l.globalTokens >= float64(bytes) && downloadTokens >= float64(bytes) {
			l.globalTokens -= float64(bytes)
			downloadTokens -= float64(bytes)
			l.downloadTokens[downloadID] = downloadTokens
			l.mu.Unlock()
			return nil
		}
		globalWait := (float64(bytes) - l.globalTokens) / l.globalRate
		downloadWait := (float64(bytes) - downloadTokens) / l.downloadRate
		wait := time.Duration(maxFloat(globalWait, downloadWait) * float64(time.Second))
		l.mu.Unlock()
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
