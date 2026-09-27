package engine

import (
	"errors"
	"fmt"
)

type byteRange struct {
	Start int64
	End   int64
}

func (r byteRange) Length() int64 {
	return r.End - r.Start + 1
}

func (r byteRange) Overlaps(other byteRange) bool {
	return r.Start <= other.End && other.Start <= r.End
}

type intervalScheduler struct {
	available []byteRange
	assigned  map[byteRange]struct{}
	minimum   int64
	completed int64
}

func newIntervalScheduler(size, minimum int64) (*intervalScheduler, error) {
	if size <= 0 {
		return nil, errors.New("interval size must be positive")
	}
	if minimum <= 0 {
		return nil, errors.New("minimum interval size must be positive")
	}
	return &intervalScheduler{
		available: []byteRange{{Start: 0, End: size - 1}},
		assigned:  make(map[byteRange]struct{}),
		minimum:   minimum,
	}, nil
}
func (s *intervalScheduler) Assign() (byteRange, error) {
	if len(s.available) == 0 {
		return byteRange{}, errors.New("no interval available")
	}
	index := 0
	for i := 1; i < len(s.available); i++ {
		if s.available[i].Length() > s.available[index].Length() {
			index = i
		}
	}
	rangeToAssign := s.available[index]
	s.available = append(s.available[:index], s.available[index+1:]...)
	if rangeToAssign.Length() >= 2*s.minimum {
		midpoint := rangeToAssign.Start + rangeToAssign.Length()/2 - 1
		s.available = append(s.available,
			byteRange{Start: midpoint + 1, End: rangeToAssign.End},
		)
		rangeToAssign.End = midpoint
	}
	s.assigned[rangeToAssign] = struct{}{}
	return rangeToAssign, nil
}
func (s *intervalScheduler) Complete(completed byteRange) error {
	if completed.Start < 0 || completed.End < completed.Start {
		return fmt.Errorf("invalid completed interval %+v", completed)
	}
	if _, ok := s.assigned[completed]; !ok {
		return fmt.Errorf("interval %+v is not assigned", completed)
	}
	delete(s.assigned, completed)
	s.completed += completed.Length()
	return nil
}

func (s *intervalScheduler) Pending() int {
	return len(s.available) + len(s.assigned)
}
func (s *intervalScheduler) CompletedBytes() int64 {
	return s.completed
}
