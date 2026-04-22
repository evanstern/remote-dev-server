package messages

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evanstern/coda/internal/db"
)

type stubTransport struct {
	calls    []stubCall
	fail     bool
	failOnID int64
	failErr  error
}

type stubCall struct {
	Port      int
	SessionID string
	Text      string
}

func (s *stubTransport) Deliver(ctx context.Context, port int, sessionID, text string) error {
	s.calls = append(s.calls, stubCall{Port: port, SessionID: sessionID, Text: text})
	if s.failErr != nil {
		return s.failErr
	}
	if s.fail {
		return errors.New("stub deliver fail")
	}
	if s.failOnID > 0 {
		idx := int64(len(s.calls))
		if idx == s.failOnID {
			return errors.New("stub deliver fail on nth call")
		}
	}
	return nil
}

type stubOrchs struct {
	port      int
	sessionID string
	running   bool
	err       error
}

func (s *stubOrchs) Lookup(ctx context.Context, name string) (int, string, bool, error) {
	if s.err != nil {
		return 0, "", false, s.err
	}
	return s.port, s.sessionID, s.running, nil
}

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "coda.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func newTestManager(t *testing.T, transport Transport, orchs OrchLookup) (*Manager, *sql.DB) {
	t.Helper()
	d := openTestDB(t)
	m := New(d, transport, orchs)
	m.Now = func() time.Time { return time.Unix(1000, 0) }
	return m, d
}

func validBody(t Type) string {
	switch t {
	case TypeBrief:
		return `{"card_id":"149","card_title":"msg bus","project":"coda","branch":"149","brief_path":"/tmp/x"}`
	case TypeStatus:
		return `{"feature":"msgs","progress":"halfway"}`
	case TypeCompletion:
		return `{"feature":"msgs","status":"done"}`
	case TypeEscalation:
		return `{"from_feature":"msgs","blocker":"x","blocking":"y"}`
	case TypeNote:
		return `{"text":"hello"}`
	}
	return `{}`
}

func TestSend_ValidatesType(t *testing.T) {
	m, _ := newTestManager(t, nil, nil)
	_, err := m.Send(context.Background(), "ash", "zach", Type("nope"), `{"text":"x"}`, 0)
	if !errors.Is(err, ErrInvalidType) {
		t.Fatalf("want ErrInvalidType, got %v", err)
	}
}

func TestSend_ValidatesBodyJSON(t *testing.T) {
	m, _ := newTestManager(t, nil, nil)
	_, err := m.Send(context.Background(), "ash", "zach", TypeNote, `not json`, 0)
	if !errors.Is(err, ErrInvalidBody) {
		t.Fatalf("want ErrInvalidBody, got %v", err)
	}
}

func TestSend_RequiresTypeSpecificFields(t *testing.T) {
	cases := []struct {
		name string
		t    Type
		body string
	}{
		{"brief missing card_id", TypeBrief, `{"card_title":"t","project":"p","branch":"b","brief_path":"/x"}`},
		{"status missing progress", TypeStatus, `{"feature":"f"}`},
		{"completion missing status", TypeCompletion, `{"feature":"f"}`},
		{"escalation missing blocking", TypeEscalation, `{"from_feature":"f","blocker":"x"}`},
		{"note missing text", TypeNote, `{}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, _ := newTestManager(t, nil, nil)
			_, err := m.Send(context.Background(), "ash", "zach", c.t, c.body, 0)
			if !errors.Is(err, ErrInvalidBody) {
				t.Fatalf("want ErrInvalidBody, got %v", err)
			}
		})
	}
}

func TestSend_RejectsBadCompletionStatus(t *testing.T) {
	m, _ := newTestManager(t, nil, nil)
	_, err := m.Send(context.Background(), "ash", "zach", TypeCompletion,
		`{"feature":"f","status":"maybe"}`, 0)
	if !errors.Is(err, ErrInvalidBody) {
		t.Fatalf("want ErrInvalidBody, got %v", err)
	}
}

func TestSend_RejectsOversizeBody(t *testing.T) {
	m, _ := newTestManager(t, nil, nil)
	big := `{"text":"` + strings.Repeat("a", MaxBodyBytes) + `"}`
	_, err := m.Send(context.Background(), "ash", "zach", TypeNote, big, 0)
	if !errors.Is(err, ErrInvalidBody) {
		t.Fatalf("want ErrInvalidBody, got %v", err)
	}
}

func TestSend_StoresRowWithNullDeliveredWhenNoTransport(t *testing.T) {
	m, d := newTestManager(t, nil, nil)
	msg, err := m.Send(context.Background(), "ash", "zach", TypeNote, `{"text":"hi"}`, 0)
	if err != nil {
		t.Fatal(err)
	}
	var delivered sql.NullInt64
	if err := d.QueryRow(`SELECT delivered_at FROM messages WHERE id=?`, msg.ID).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if delivered.Valid {
		t.Fatalf("delivered_at = %v, want NULL", delivered)
	}
}

func TestSend_SetsDeliveredOnSuccess(t *testing.T) {
	tr := &stubTransport{}
	orchs := &stubOrchs{port: 4096, sessionID: "ses1", running: true}
	m, d := newTestManager(t, tr, orchs)

	msg, err := m.Send(context.Background(), "ash", "zach", TypeNote, `{"text":"hi"}`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !msg.DeliveredAt.Valid {
		t.Fatalf("delivered_at not set in returned struct")
	}
	var delivered sql.NullInt64
	if err := d.QueryRow(`SELECT delivered_at FROM messages WHERE id=?`, msg.ID).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if !delivered.Valid {
		t.Fatalf("delivered_at not persisted")
	}
	if len(tr.calls) != 1 {
		t.Fatalf("want 1 deliver call, got %d", len(tr.calls))
	}
}

func TestSend_LeavesUndeliveredOnTransportFailure(t *testing.T) {
	tr := &stubTransport{fail: true}
	orchs := &stubOrchs{port: 4096, sessionID: "ses1", running: true}
	m, d := newTestManager(t, tr, orchs)

	msg, err := m.Send(context.Background(), "ash", "zach", TypeNote, `{"text":"hi"}`, 0)
	if err != nil {
		t.Fatalf("transport failure must not propagate, got %v", err)
	}
	if msg.DeliveredAt.Valid {
		t.Fatalf("delivered_at should be NULL on transport failure")
	}
	var delivered sql.NullInt64
	if err := d.QueryRow(`SELECT delivered_at FROM messages WHERE id=?`, msg.ID).Scan(&delivered); err != nil {
		t.Fatal(err)
	}
	if delivered.Valid {
		t.Fatalf("persisted delivered_at should be NULL")
	}
}

func TestSend_QueuesWhenRecipientNotRunning(t *testing.T) {
	tr := &stubTransport{}
	orchs := &stubOrchs{running: false}
	m, _ := newTestManager(t, tr, orchs)

	msg, err := m.Send(context.Background(), "ash", "zach", TypeNote, `{"text":"hi"}`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if msg.DeliveredAt.Valid {
		t.Fatalf("should not be delivered when recipient not running")
	}
	if len(tr.calls) != 0 {
		t.Fatalf("transport should not be invoked when not running")
	}
}

func TestSend_QueuesWhenRecipientUnknown(t *testing.T) {
	tr := &stubTransport{}
	orchs := &stubOrchs{err: ErrNotFound}
	m, _ := newTestManager(t, tr, orchs)

	msg, err := m.Send(context.Background(), "ash", "ghost", TypeNote, `{"text":"hi"}`, 0)
	if err != nil {
		t.Fatalf("unknown recipient should not error, got %v", err)
	}
	if msg.DeliveredAt.Valid {
		t.Fatalf("should be queued")
	}
}

func TestSend_ValidatesParentIDExists(t *testing.T) {
	m, _ := newTestManager(t, nil, nil)
	_, err := m.Send(context.Background(), "ash", "zach", TypeNote, `{"text":"hi"}`, 999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestSend_AcceptsParentIDWhenExists(t *testing.T) {
	m, _ := newTestManager(t, nil, nil)
	ctx := context.Background()
	parent, err := m.Send(ctx, "ash", "zach", TypeNote, `{"text":"hi"}`, 0)
	if err != nil {
		t.Fatal(err)
	}
	child, err := m.Send(ctx, "zach", "ash", TypeNote, `{"text":"back"}`, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !child.ParentID.Valid || child.ParentID.Int64 != parent.ID {
		t.Fatalf("child ParentID = %v, want %d", child.ParentID, parent.ID)
	}
}

func TestSend_AcceptsBroadcastRecipient(t *testing.T) {
	tr := &stubTransport{}
	orchs := &stubOrchs{port: 4096, sessionID: "ses", running: true}
	m, _ := newTestManager(t, tr, orchs)

	msg, err := m.Send(context.Background(), "ash", BroadcastRecipient, TypeNote, `{"text":"hi"}`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if msg.DeliveredAt.Valid {
		t.Fatalf("broadcast should not deliver")
	}
	if len(tr.calls) != 0 {
		t.Fatalf("broadcast should not invoke transport")
	}
}

func seedMessage(t *testing.T, d *sql.DB, sender, recipient string, createdAt int64, acked bool) int64 {
	t.Helper()
	var ackedArg any
	if acked {
		ackedArg = createdAt + 1
	}
	res, err := d.Exec(
		`INSERT INTO messages (sender, recipient, type, body, created_at, acked_at)
		 VALUES (?, ?, 'note', '{"text":"x"}', ?, ?)`,
		sender, recipient, createdAt, ackedArg)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func TestDrain_DeliversInOrder(t *testing.T) {
	tr := &stubTransport{}
	orchs := &stubOrchs{port: 4096, sessionID: "ses1", running: true}
	m, d := newTestManager(t, tr, orchs)

	id1 := seedMessage(t, d, "ash", "zach", 100, false)
	id2 := seedMessage(t, d, "ash", "zach", 200, false)
	id3 := seedMessage(t, d, "ash", "zach", 300, false)

	n, err := m.Drain(context.Background(), "zach")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("drained %d, want 3", n)
	}
	if len(tr.calls) != 3 {
		t.Fatalf("want 3 transport calls, got %d", len(tr.calls))
	}
	var ids []int64
	for _, call := range tr.calls {
		for _, token := range strings.Split(call.Text, " ") {
			if strings.HasPrefix(token, "id=") {
				var n int64
				if _, err := fmt.Sscanf(token, "id=%d", &n); err != nil {
					t.Fatal(err)
				}
				ids = append(ids, n)
			}
		}
	}
	want := []int64{id1, id2, id3}
	for i, id := range ids {
		if id != want[i] {
			t.Fatalf("drain order[%d] = %d, want %d", i, id, want[i])
		}
	}
}

func TestDrain_StopsAtFirstFailure(t *testing.T) {
	tr := &stubTransport{failOnID: 2}
	orchs := &stubOrchs{port: 4096, sessionID: "ses1", running: true}
	m, d := newTestManager(t, tr, orchs)

	id1 := seedMessage(t, d, "ash", "zach", 100, false)
	_ = seedMessage(t, d, "ash", "zach", 200, false)
	_ = seedMessage(t, d, "ash", "zach", 300, false)

	n, err := m.Drain(context.Background(), "zach")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("drained %d, want 1", n)
	}
	var del sql.NullInt64
	if err := d.QueryRow(`SELECT delivered_at FROM messages WHERE id=?`, id1).Scan(&del); err != nil {
		t.Fatal(err)
	}
	if !del.Valid {
		t.Fatalf("msg 1 should be delivered")
	}
}

func TestDrain_NoopWhenOrchNotRunning(t *testing.T) {
	tr := &stubTransport{}
	orchs := &stubOrchs{running: false}
	m, d := newTestManager(t, tr, orchs)
	seedMessage(t, d, "ash", "zach", 100, false)

	n, err := m.Drain(context.Background(), "zach")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("drained %d, want 0", n)
	}
	if len(tr.calls) != 0 {
		t.Fatalf("transport should not be called")
	}
}

func TestRecv_ReturnsNewestFirst(t *testing.T) {
	m, d := newTestManager(t, nil, nil)
	seedMessage(t, d, "ash", "zach", 100, false)
	seedMessage(t, d, "ash", "zach", 200, false)
	id3 := seedMessage(t, d, "ash", "zach", 300, false)

	msgs, err := m.Recv(context.Background(), "zach", false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d, want 3", len(msgs))
	}
	if msgs[0].ID != id3 {
		t.Fatalf("first = %d, want %d (newest)", msgs[0].ID, id3)
	}
}

func TestRecv_UnackedOnly(t *testing.T) {
	m, d := newTestManager(t, nil, nil)
	ackedID := seedMessage(t, d, "ash", "zach", 100, true)
	unackedID := seedMessage(t, d, "ash", "zach", 200, false)

	msgs, err := m.Recv(context.Background(), "zach", true, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].ID != unackedID {
		t.Fatalf("unacked filter got %d msgs, first=%d, want 1 msg id=%d",
			len(msgs), func() int64 {
				if len(msgs) > 0 {
					return msgs[0].ID
				}
				return 0
			}(), unackedID)
	}
	_ = ackedID
}

func TestRecv_SinceID(t *testing.T) {
	m, d := newTestManager(t, nil, nil)
	id1 := seedMessage(t, d, "ash", "zach", 100, false)
	seedMessage(t, d, "ash", "zach", 200, false)
	seedMessage(t, d, "ash", "zach", 300, false)

	msgs, err := m.Recv(context.Background(), "zach", false, id1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("got %d, want 2", len(msgs))
	}
	for _, msg := range msgs {
		if msg.ID <= id1 {
			t.Fatalf("msg id %d not > %d", msg.ID, id1)
		}
	}
}

func TestRecv_LimitDefaults(t *testing.T) {
	m, d := newTestManager(t, nil, nil)
	for i := int64(0); i < 60; i++ {
		seedMessage(t, d, "ash", "zach", 1000+i, false)
	}
	msgs, err := m.Recv(context.Background(), "zach", false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 50 {
		t.Fatalf("got %d, want 50 (default limit)", len(msgs))
	}
}

func TestAck_SetsAckedAt(t *testing.T) {
	m, d := newTestManager(t, nil, nil)
	id := seedMessage(t, d, "ash", "zach", 100, false)
	if err := m.Ack(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	var acked sql.NullInt64
	if err := d.QueryRow(`SELECT acked_at FROM messages WHERE id=?`, id).Scan(&acked); err != nil {
		t.Fatal(err)
	}
	if !acked.Valid {
		t.Fatalf("acked_at not set")
	}
}

func TestAck_Idempotent(t *testing.T) {
	m, d := newTestManager(t, nil, nil)
	id := seedMessage(t, d, "ash", "zach", 100, false)
	if err := m.Ack(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if err := m.Ack(context.Background(), id); err != nil {
		t.Fatalf("second ack should be no-op, got %v", err)
	}
}

func TestAck_NotFound(t *testing.T) {
	m, _ := newTestManager(t, nil, nil)
	err := m.Ack(context.Background(), 999)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestUnackedCounts_AggregatesByRecipient(t *testing.T) {
	m, d := newTestManager(t, nil, nil)
	seedMessage(t, d, "ash", "zach", 100, false)
	seedMessage(t, d, "ash", "zach", 101, false)
	seedMessage(t, d, "ash", "riley", 102, false)
	seedMessage(t, d, "ash", "riley", 103, true)

	counts, err := m.UnackedCounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if counts["zach"] != 2 {
		t.Fatalf("zach = %d, want 2", counts["zach"])
	}
	if counts["riley"] != 1 {
		t.Fatalf("riley = %d, want 1 (acked one excluded)", counts["riley"])
	}
}

func TestDBOrchLookup_NotFound(t *testing.T) {
	d := openTestDB(t)
	l := &DBOrchLookup{DB: d}
	_, _, _, err := l.Lookup(context.Background(), "ghost")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestDBOrchLookup_RunningRequiresCoords(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO orchestrators (name, config_dir, state, created_at, updated_at)
		 VALUES ('zach', '/tmp/z', 'running', 0, 0)`,
	); err != nil {
		t.Fatal(err)
	}
	l := &DBOrchLookup{DB: d}
	_, _, running, err := l.Lookup(context.Background(), "zach")
	if err != nil {
		t.Fatal(err)
	}
	if running {
		t.Fatalf("running should be false when port/session_id missing")
	}
}

func TestDBOrchLookup_ReturnsCoords(t *testing.T) {
	d := openTestDB(t)
	if _, err := d.Exec(
		`INSERT INTO orchestrators (name, config_dir, state, port, session_id, created_at, updated_at)
		 VALUES ('zach', '/tmp/z', 'running', 4096, 'ses1', 0, 0)`,
	); err != nil {
		t.Fatal(err)
	}
	l := &DBOrchLookup{DB: d}
	port, session, running, err := l.Lookup(context.Background(), "zach")
	if err != nil {
		t.Fatal(err)
	}
	if port != 4096 || session != "ses1" || !running {
		t.Fatalf("Lookup = (%d, %q, %v), want (4096, ses1, true)", port, session, running)
	}
}
