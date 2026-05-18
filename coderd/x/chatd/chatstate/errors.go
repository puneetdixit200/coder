package chatstate

import (
	"fmt"

	"golang.org/x/xerrors"
)

// Sentinel errors returned by chatstate transitions and helpers.
// Callers should use errors.Is to test for these.
var (
	// ErrTransitionNotAllowed is returned when a transition is applied
	// to a chat whose current execution state does not permit it. The
	// concrete error returned by transition methods is a
	// *TransitionError that wraps this sentinel.
	ErrTransitionNotAllowed = xerrors.New("chat state transition not allowed")

	// ErrInvalidState is returned when the chat row, queue, and
	// archive flag together produce a combination outside the 13
	// valid execution states described in the RFC.
	ErrInvalidState = xerrors.New("chat is in an invalid execution state")

	// ErrQueuedMessageNotFound is returned by queue-targeting
	// transitions (delete, promote) when the supplied queued message
	// ID does not match a row on the chat.
	ErrQueuedMessageNotFound = xerrors.New("queued message not found")

	// ErrMessageNotFound is returned by [Tx.EditMessage] when the
	// target chat_messages row is missing or belongs to another chat.
	ErrMessageNotFound = xerrors.New("chat message not found")

	// ErrChatNotFound is returned when a non-create transition is
	// applied to a chat row that does not exist (or has been deleted
	// since the transition started).
	ErrChatNotFound = xerrors.New("chat not found")
)

// TransitionError carries the structured detail for a rejected
// transition. It always wraps [ErrTransitionNotAllowed] so callers can
// match with errors.Is without losing context.
type TransitionError struct {
	Transition Transition
	From       ExecutionState
	Reason     string
}

// Error implements the error interface.
func (e *TransitionError) Error() string {
	if e.Reason == "" {
		return fmt.Sprintf(
			"chat state transition %s not allowed from state %s",
			e.Transition, e.From,
		)
	}
	return fmt.Sprintf(
		"chat state transition %s not allowed from state %s: %s",
		e.Transition, e.From, e.Reason,
	)
}

// Unwrap returns [ErrTransitionNotAllowed] so callers can match on the
// sentinel without losing the structured fields above.
func (*TransitionError) Unwrap() error { return ErrTransitionNotAllowed }

// newTransitionError constructs a typed TransitionError. Returning the
// pointer type lets callers inspect the structured fields when needed.
func newTransitionError(t Transition, from ExecutionState, reason string) *TransitionError {
	return &TransitionError{Transition: t, From: from, Reason: reason}
}
