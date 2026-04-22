// Package messages implements the v2 typed message bus: a persistent
// SQLite-backed store with optional HTTP delivery. It replaces the
// inbox.md + coda orch send + 50-orch-notify hook plumbing with a
// single surface: Send, Recv, Ack, Drain.
package messages

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// Type is the validated message type.
type Type string

const (
	TypeBrief      Type = "brief"
	TypeStatus     Type = "status"
	TypeCompletion Type = "completion"
	TypeEscalation Type = "escalation"
	TypeNote       Type = "note"
)

// BroadcastRecipient is the sentinel recipient value that stores the
// message but skips the delivery attempt. Real broadcast fan-out is
// parked (design doc §Open questions §1).
const BroadcastRecipient = "broadcast"

// MaxBodyBytes caps the stored body size per design doc §Open
// questions §2.
const MaxBodyBytes = 65536

// Message is one row of the messages table.
type Message struct {
	ID          int64
	Sender      string
	Recipient   string
	Type        Type
	Body        string
	ParentID    sql.NullInt64
	CreatedAt   int64
	DeliveredAt sql.NullInt64
	AckedAt     sql.NullInt64
}

// Transport abstracts HTTP delivery. A nil Transport on the Manager
// disables delivery attempts; the row is still stored durably.
type Transport interface {
	Deliver(ctx context.Context, port int, sessionID, text string) error
}

// OrchLookup fetches delivery coordinates for a recipient name.
// running=false means the orchestrator exists but is not in
// state=running (message queues). err=ErrNotFound means no such
// orchestrator.
type OrchLookup interface {
	Lookup(ctx context.Context, name string) (port int, sessionID string, running bool, err error)
}

var (
	ErrNotFound    = errors.New("not found")
	ErrInvalidBody = errors.New("invalid body for message type")
	ErrInvalidType = errors.New("invalid message type")
)

// Manager is the state + delivery surface.
type Manager struct {
	DB        *sql.DB
	Transport Transport
	Orchs     OrchLookup
	Now       func() time.Time
}

// New constructs a Manager. Either Transport or Orchs may be nil —
// Send will just skip the delivery attempt and leave delivered_at
// NULL for a later Drain.
func New(db *sql.DB, t Transport, o OrchLookup) *Manager {
	return &Manager{DB: db, Transport: t, Orchs: o, Now: time.Now}
}

func (m *Manager) now() int64 { return m.Now().Unix() }

var requiredFields = map[Type][]string{
	TypeBrief:      {"card_id", "card_title", "project", "branch", "brief_path"},
	TypeStatus:     {"feature", "progress"},
	TypeCompletion: {"feature", "status"},
	TypeEscalation: {"from_feature", "blocker", "blocking"},
	TypeNote:       {"text"},
}

var completionStatuses = map[string]bool{
	"done": true, "failed": true, "partial": true, "blocked": true,
}

// Send writes a message row and, if the recipient is running, attempts
// delivery. Delivery failure does NOT return an error; the message is
// durably stored and will be drained on recipient restart.
// parentID=0 means no parent; nonzero must reference an existing row.
func (m *Manager) Send(ctx context.Context, sender, recipient string, t Type, body string, parentID int64) (*Message, error) {
	if _, ok := requiredFields[t]; !ok {
		return nil, fmt.Errorf("%w: %q", ErrInvalidType, t)
	}
	if len(body) > MaxBodyBytes {
		return nil, fmt.Errorf("%w: body exceeds 64KB (%d bytes)", ErrInvalidBody, len(body))
	}
	if !json.Valid([]byte(body)) {
		return nil, fmt.Errorf("%w: not valid JSON", ErrInvalidBody)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidBody, err)
	}
	for _, field := range requiredFields[t] {
		if _, present := parsed[field]; !present {
			return nil, fmt.Errorf("%w: missing %q", ErrInvalidBody, field)
		}
	}
	if t == TypeCompletion {
		s, _ := parsed["status"].(string)
		if !completionStatuses[s] {
			return nil, fmt.Errorf("%w: completion.status must be one of done|failed|partial|blocked", ErrInvalidBody)
		}
	}

	if parentID != 0 {
		var exists int
		err := m.DB.QueryRowContext(ctx, `SELECT 1 FROM messages WHERE id=?`, parentID).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: parent message %d", ErrNotFound, parentID)
		}
		if err != nil {
			return nil, err
		}
	}

	now := m.now()
	var parentArg any
	if parentID != 0 {
		parentArg = parentID
	}
	res, err := m.DB.ExecContext(ctx,
		`INSERT INTO messages (sender, recipient, type, body, parent_id, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		sender, recipient, string(t), body, parentArg, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	msg := &Message{
		ID: id, Sender: sender, Recipient: recipient, Type: t,
		Body: body, CreatedAt: now,
	}
	if parentID != 0 {
		msg.ParentID = sql.NullInt64{Int64: parentID, Valid: true}
	}

	if recipient == BroadcastRecipient {
		return msg, nil
	}
	if m.Orchs == nil || m.Transport == nil {
		return msg, nil
	}

	port, sessionID, running, err := m.Orchs.Lookup(ctx, recipient)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return msg, nil
		}
		return nil, err
	}
	if !running {
		return msg, nil
	}

	if derr := m.Transport.Deliver(ctx, port, sessionID, renderDelivery(msg)); derr != nil {
		fmt.Fprintf(os.Stderr, "messages: deliver id=%d to %q failed: %v\n", id, recipient, derr)
		return msg, nil
	}

	if _, err := m.DB.ExecContext(ctx,
		`UPDATE messages SET delivered_at=? WHERE id=?`, now, id); err != nil {
		return nil, err
	}
	msg.DeliveredAt = sql.NullInt64{Int64: now, Valid: true}
	return msg, nil
}

// Drain delivers all undelivered messages for recipient in created_at
// order. It stops at the first delivery failure so ordering is
// preserved across restarts. Returns (delivered_count, error). A
// transport failure is NOT propagated as an error — the caller logs
// and moves on.
func (m *Manager) Drain(ctx context.Context, recipient string) (int, error) {
	if m.Orchs == nil || m.Transport == nil {
		return 0, nil
	}
	port, sessionID, running, err := m.Orchs.Lookup(ctx, recipient)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return 0, nil
		}
		return 0, err
	}
	if !running {
		return 0, nil
	}

	rows, err := m.DB.QueryContext(ctx,
		`SELECT id, sender, recipient, type, body, parent_id, created_at, delivered_at, acked_at
		 FROM messages
		 WHERE recipient=? AND delivered_at IS NULL
		 ORDER BY created_at ASC, id ASC`, recipient)
	if err != nil {
		return 0, err
	}
	var pending []*Message
	for rows.Next() {
		msg := &Message{}
		var typ string
		if err := rows.Scan(&msg.ID, &msg.Sender, &msg.Recipient, &typ, &msg.Body,
			&msg.ParentID, &msg.CreatedAt, &msg.DeliveredAt, &msg.AckedAt); err != nil {
			rows.Close()
			return 0, err
		}
		msg.Type = Type(typ)
		pending = append(pending, msg)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}

	delivered := 0
	for _, msg := range pending {
		if err := m.Transport.Deliver(ctx, port, sessionID, renderDelivery(msg)); err != nil {
			fmt.Fprintf(os.Stderr, "messages: drain id=%d to %q failed: %v\n", msg.ID, recipient, err)
			return delivered, nil
		}
		now := m.now()
		if _, err := m.DB.ExecContext(ctx,
			`UPDATE messages SET delivered_at=? WHERE id=?`, now, msg.ID); err != nil {
			return delivered, err
		}
		delivered++
	}
	return delivered, nil
}

// Recv returns messages for recipient, newest first, limited to limit
// rows (0 = default 50). If unackedOnly, filters acked_at IS NULL. If
// sinceID > 0, filters id > sinceID.
func (m *Manager) Recv(ctx context.Context, recipient string, unackedOnly bool, sinceID int64, limit int) ([]*Message, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, sender, recipient, type, body, parent_id, created_at, delivered_at, acked_at
	      FROM messages WHERE recipient=?`
	args := []any{recipient}
	if unackedOnly {
		q += ` AND acked_at IS NULL`
	}
	if sinceID > 0 {
		q += ` AND id > ?`
		args = append(args, sinceID)
	}
	q += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := m.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Message
	for rows.Next() {
		msg := &Message{}
		var typ string
		if err := rows.Scan(&msg.ID, &msg.Sender, &msg.Recipient, &typ, &msg.Body,
			&msg.ParentID, &msg.CreatedAt, &msg.DeliveredAt, &msg.AckedAt); err != nil {
			return nil, err
		}
		msg.Type = Type(typ)
		out = append(out, msg)
	}
	return out, rows.Err()
}

// Ack sets acked_at on the given message id. Idempotent — re-acking
// is a no-op success. Returns ErrNotFound if no row matches.
func (m *Manager) Ack(ctx context.Context, id int64) error {
	var existingAck sql.NullInt64
	err := m.DB.QueryRowContext(ctx,
		`SELECT acked_at FROM messages WHERE id=?`, id).Scan(&existingAck)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: message %d", ErrNotFound, id)
	}
	if err != nil {
		return err
	}
	if existingAck.Valid {
		return nil
	}
	_, err = m.DB.ExecContext(ctx,
		`UPDATE messages SET acked_at=? WHERE id=?`, m.now(), id)
	return err
}

// UnackedCounts returns a map of recipient -> count of unacked
// messages, used by coda-core status.
func (m *Manager) UnackedCounts(ctx context.Context) (map[string]int, error) {
	rows, err := m.DB.QueryContext(ctx,
		`SELECT recipient, COUNT(*) FROM messages WHERE acked_at IS NULL GROUP BY recipient`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}

// renderDelivery formats a message for HTTP delivery: a one-line
// header ([message type=... id=... from=...]) followed by the raw
// JSON body on line 2. Shared by Send and Drain.
func renderDelivery(msg *Message) string {
	body := strings.ReplaceAll(msg.Body, "\n", " ")
	return fmt.Sprintf("[message type=%s id=%d from=%s]\n%s",
		msg.Type, msg.ID, msg.Sender, body)
}

// DBOrchLookup resolves recipient names via the orchestrators table.
type DBOrchLookup struct {
	DB *sql.DB
}

// Lookup implements OrchLookup by querying the orchestrators row.
func (l *DBOrchLookup) Lookup(ctx context.Context, name string) (int, string, bool, error) {
	var port sql.NullInt64
	var sessionID sql.NullString
	var state string
	err := l.DB.QueryRowContext(ctx,
		`SELECT port, session_id, state FROM orchestrators WHERE name=?`,
		name).Scan(&port, &sessionID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, ErrNotFound
	}
	if err != nil {
		return 0, "", false, err
	}
	running := state == "running" && port.Valid && sessionID.Valid
	p := 0
	if port.Valid {
		p = int(port.Int64)
	}
	s := ""
	if sessionID.Valid {
		s = sessionID.String
	}
	return p, s, running, nil
}
