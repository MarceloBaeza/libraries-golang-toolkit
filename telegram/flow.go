package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	// ErrNoSession means there is no active (unexpired) flow.
	ErrNoSession = errors.New("telegram: no active flow session")
	// ErrStale means the flow changed since the caller read it (another
	// button was pressed, the flow was replaced, ended or expired).
	ErrStale = errors.New("telegram: stale flow session")
	// ErrBadCallback means callback data is not a well-formed flow button.
	ErrBadCallback = errors.New("telegram: malformed flow callback")
)

// Session is the single active guided flow of a user in a private chat.
// The store keys sessions by the application's own user identifier
// (userKey, e.g. a UUID) resolved from the sender, plus the chat ID.
type Session struct {
	FlowID    string
	Type      string
	Step      string
	Revision  int
	State     json.RawMessage
	ExpiresAt time.Time
}

// FlowStore persists sessions keyed by (user, chat). Every write that
// advances a session is a compare-and-swap on (flow id, revision), so a
// repeated or old button can never apply twice.
type FlowStore interface {
	// Get returns ErrNoSession when absent or expired at now.
	Get(ctx context.Context, userKey string, chatID int64, now time.Time) (*Session, error)
	// Start replaces any previous session with a new flow at revision 0.
	Start(ctx context.Context, userKey string, chatID int64, flowType, step string, state json.RawMessage, expiresAt time.Time) (*Session, error)
	// Advance moves the flow to step/state and increments the revision,
	// only if flowID and revision still match and it has not expired.
	// Otherwise it returns ErrStale.
	Advance(ctx context.Context, userKey string, chatID int64, flowID string, revision int, step string, state json.RawMessage, now, expiresAt time.Time) (*Session, error)
	// End deletes the session if it is still flowID. Ending a flow that no
	// longer exists is not an error.
	End(ctx context.Context, userKey string, chatID int64, flowID string) error
}

// flowShort is the part of the flow id carried in buttons (callback data is
// limited to 64 bytes). Eight hex characters are enough: the revision and
// the per-user lookup do the real binding.
func flowShort(flowID string) string {
	s := strings.ReplaceAll(flowID, "-", "")
	if len(s) > 8 {
		s = s[:8]
	}
	return s
}

// FlowCallback builds the callback data of a button that belongs to s at its
// current revision. choice must not be empty; the whole value must fit in
// Telegram's 64-byte limit.
func FlowCallback(s *Session, choice string) string {
	return fmt.Sprintf("f:%s:%d:%s", flowShort(s.FlowID), s.Revision, choice)
}

// FlowButton is a convenience for an inline button bound to s.
func FlowButton(s *Session, text, choice string) InlineKeyboardButton {
	return InlineKeyboardButton{Text: text, CallbackData: FlowCallback(s, choice)}
}

// IsFlowCallback reports whether data looks like a flow button.
func IsFlowCallback(data string) bool { return strings.HasPrefix(data, "f:") }

// FlowChoice is a parsed flow button.
type FlowChoice struct {
	FlowShort string
	Revision  int
	Choice    string
}

// ParseFlowCallback parses "f:<flow>:<rev>:<choice>". The choice may itself
// contain ':'.
func ParseFlowCallback(data string) (FlowChoice, error) {
	parts := strings.SplitN(data, ":", 4)
	if len(parts) != 4 || parts[0] != "f" || parts[1] == "" || parts[3] == "" {
		return FlowChoice{}, ErrBadCallback
	}
	rev, err := strconv.Atoi(parts[2])
	if err != nil || rev < 0 {
		return FlowChoice{}, ErrBadCallback
	}
	return FlowChoice{FlowShort: parts[1], Revision: rev, Choice: parts[3]}, nil
}

// Matches reports whether the button was issued for s at its current
// revision. A false result means the button is old, repeated or foreign.
func (c FlowChoice) Matches(s *Session) bool {
	return s != nil && c.FlowShort == flowShort(s.FlowID) && c.Revision == s.Revision
}
