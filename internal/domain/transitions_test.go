package domain

import (
	"strings"
	"testing"
)

func TestValidateTransition_AllPairs(t *testing.T) {
	all := []Status{Backlog, Todo, InProgress, InReview, Blocked, Done, WontDo}
	for _, from := range all {
		for _, to := range all {
			from, to := from, to
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				err := ValidateTransition("T-1", from, to)
				wantAllowed := from == to || from.CanTransition(to)
				if wantAllowed && err != nil {
					t.Fatalf("expected %s->%s allowed, got error: %v", from, to, err)
				}
				if !wantAllowed && err == nil {
					t.Fatalf("expected %s->%s denied, got nil", from, to)
				}
			})
		}
	}
}

func TestDenialMessagesListLegalMoves(t *testing.T) {
	// From a non-terminal state, the message must name the legal targets.
	err := ValidateTransition("T-7", Backlog, InReview)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "todo") || !strings.Contains(msg, "wont_do") {
		t.Fatalf("denial message should list legal moves (todo, wont_do); got: %s", msg)
	}
}

func TestTerminalDenialSuggestsNewTicket(t *testing.T) {
	err := ValidateTransition("T-42", Done, InProgress)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "terminal") ||
		!strings.Contains(err.Error(), "new ticket") {
		t.Fatalf("terminal denial should explain terminal + suggest new ticket; got: %s", err.Error())
	}
}

func TestTerminalStates(t *testing.T) {
	if !Done.IsTerminal() {
		t.Error("done should be terminal")
	}
	if !WontDo.IsTerminal() {
		t.Error("wont_do should be terminal")
	}
	if InProgress.IsTerminal() {
		t.Error("in_progress should not be terminal")
	}
}

func TestParseStatus(t *testing.T) {
	if _, err := ParseStatus("IN_PROGRESS"); err != nil {
		t.Errorf("case-insensitive parse failed: %v", err)
	}
	if _, err := ParseStatus("nonsense"); err == nil {
		t.Error("expected error for bad status")
	}
}

func TestParsePriority(t *testing.T) {
	p, err := ParsePriority("")
	if err != nil || p != Normal {
		t.Errorf("empty priority should default to normal, got %v err %v", p, err)
	}
	if _, err := ParsePriority("urgent"); err == nil {
		t.Error("expected error for unknown priority")
	}
}
