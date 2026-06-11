package domain

import (
	"fmt"
	"sort"
	"strings"
)

// Status is a ticket's position in the fixed workflow. The state machine is
// intentionally not configurable in v0.
type Status string

const (
	Backlog    Status = "backlog"
	Todo       Status = "todo"
	InProgress Status = "in_progress"
	InReview   Status = "in_review"
	Blocked    Status = "blocked"
	Done       Status = "done"
	WontDo     Status = "wont_do"
)

// transitions maps each status to the set of statuses reachable from it.
// Done and WontDo are terminal.
var transitions = map[Status][]Status{
	Backlog:    {Todo, WontDo},
	Todo:       {InProgress, Blocked, WontDo},
	InProgress: {InReview, Done, Blocked, Todo},
	InReview:   {Done, InProgress},
	Blocked:    {Todo, InProgress, WontDo},
	Done:       {},
	WontDo:     {},
}

// allStatuses is the set of valid status values, for input validation.
var allStatuses = map[Status]bool{
	Backlog: true, Todo: true, InProgress: true, InReview: true,
	Blocked: true, Done: true, WontDo: true,
}

// ParseStatus validates a status string.
func ParseStatus(s string) (Status, error) {
	st := Status(strings.ToLower(strings.TrimSpace(s)))
	if !allStatuses[st] {
		return "", fmt.Errorf("unknown status %q: valid statuses are %s",
			s, strings.Join(allStatusNames(), ", "))
	}
	return st, nil
}

func allStatusNames() []string {
	names := make([]string, 0, len(allStatuses))
	for st := range allStatuses {
		names = append(names, string(st))
	}
	sort.Strings(names)
	return names
}

// IsTerminal reports whether no transitions leave this status.
func (s Status) IsTerminal() bool {
	return len(transitions[s]) == 0
}

// CanTransition reports whether moving from s to to is legal.
func (s Status) CanTransition(to Status) bool {
	for _, allowed := range transitions[s] {
		if allowed == to {
			return true
		}
	}
	return false
}

// ValidateTransition returns nil if the move is legal, otherwise an error
// whose message tells the agent what it can do instead. Every validation
// error must be self-correcting prompt material.
func ValidateTransition(key string, from, to Status) error {
	if from == to {
		return nil // no-op, allowed
	}
	if from.CanTransition(to) {
		return nil
	}
	if from.IsTerminal() {
		return fmt.Errorf(
			"cannot move %s from %s to %s: %s is terminal. Create a new ticket instead.",
			key, from, to, from)
	}
	legal := transitions[from]
	names := make([]string, len(legal))
	for i, st := range legal {
		names[i] = string(st)
	}
	return fmt.Errorf(
		"cannot move %s from %s to %s: legal transitions from %s are: %s",
		key, from, to, from, strings.Join(names, ", "))
}
