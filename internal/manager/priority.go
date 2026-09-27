package manager

import (
	"sort"
	"time"

	"github.com/zichuanxu/godl/internal/download"
)

type Priority int

const (
	PriorityLow Priority = iota
	PriorityNormal
	PriorityHigh
)

type queuedItem struct {
	item     download.Item
	priority Priority
	sequence uint64
}

type priorityQueue struct {
	items    []queuedItem
	sequence uint64
}

func (q *priorityQueue) Push(item download.Item, priority Priority) {
	q.sequence++
	q.items = append(q.items, queuedItem{item: item, priority: priority, sequence: q.sequence})
	sort.SliceStable(q.items, func(i, j int) bool {
		if q.items[i].priority != q.items[j].priority {
			return q.items[i].priority > q.items[j].priority
		}
		if !q.items[i].item.CreatedAt.Equal(q.items[j].item.CreatedAt) {
			return q.items[i].item.CreatedAt.Before(q.items[j].item.CreatedAt)
		}
		return q.items[i].sequence < q.items[j].sequence
	})
}

func (q *priorityQueue) Pop() (download.Item, bool) {
	if len(q.items) == 0 {
		return download.Item{}, false
	}
	item := q.items[0].item
	q.items[0] = queuedItem{}
	q.items = q.items[1:]
	return item, true
}

func (q *priorityQueue) Len() int { return len(q.items) }

func normalizePriority(priority Priority) Priority {
	if priority < PriorityLow {
		return PriorityLow
	}
	if priority > PriorityHigh {
		return PriorityHigh
	}
	return priority
}

func queueTime(item download.Item) time.Time {
	return item.CreatedAt
}
