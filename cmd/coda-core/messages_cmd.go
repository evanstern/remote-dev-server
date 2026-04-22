package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/evanstern/coda/internal/db"
	"github.com/evanstern/coda/internal/messages"
)

func openMessages() (*sql.DB, *messages.Manager, error) {
	path, err := db.DefaultPath()
	if err != nil {
		return nil, nil, err
	}
	d, err := db.Open(path)
	if err != nil {
		return nil, nil, err
	}
	m := messages.New(d, messages.NewHTTPTransport(), &messages.DBOrchLookup{DB: d})
	return d, m, nil
}

func runSend(args []string) error {
	fs := flag.NewFlagSet("send", flag.ContinueOnError)
	from := fs.String("from", os.Getenv("USER"), "sender name")
	to := fs.String("to", "", "recipient orchestrator name (required)")
	typ := fs.String("type", "", "message type (required)")
	body := fs.String("body", "", "inline JSON body")
	bodyFile := fs.String("body-file", "", "read JSON body from file")
	parentID := fs.Int64("parent-id", 0, "thread reply target (optional)")
	if err := parseInterleaved(fs, args); err != nil {
		return userError("%v", err)
	}
	if *to == "" || *typ == "" {
		return userError("usage: coda-core send --from <name> --to <name> --type <type> --body <json>")
	}
	if *body == "" && *bodyFile == "" {
		return userError("either --body or --body-file is required")
	}
	if *body != "" && *bodyFile != "" {
		return userError("--body and --body-file are mutually exclusive")
	}
	bodyText := *body
	if *bodyFile != "" {
		b, err := os.ReadFile(*bodyFile)
		if err != nil {
			return userError("read body file: %v", err)
		}
		bodyText = string(b)
	}

	d, mgr, err := openMessages()
	if err != nil {
		return dbError(err)
	}
	defer d.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	msg, err := mgr.Send(ctx, *from, *to, messages.Type(*typ), bodyText, *parentID)
	if err != nil {
		if errors.Is(err, messages.ErrInvalidType) ||
			errors.Is(err, messages.ErrInvalidBody) ||
			errors.Is(err, messages.ErrNotFound) {
			return userError("%v", err)
		}
		return dbError(err)
	}
	fmt.Printf("sent message id=%d delivered=%v\n", msg.ID, msg.DeliveredAt.Valid)
	return nil
}

func runRecv(args []string) error {
	fs := flag.NewFlagSet("recv", flag.ContinueOnError)
	recipient := fs.String("recipient", "", "recipient name (required)")
	unacked := fs.Bool("unacked", false, "only show unacked")
	sinceID := fs.Int64("since-id", 0, "only show id > since-id")
	limit := fs.Int("limit", 0, "max rows (default 50)")
	asJSON := fs.Bool("json", false, "emit JSON")
	if err := parseInterleaved(fs, args); err != nil {
		return userError("%v", err)
	}
	if *recipient == "" {
		return userError("usage: coda-core recv --recipient <name> [--unacked] [--since-id N] [--limit N] [--json]")
	}
	d, mgr, err := openMessages()
	if err != nil {
		return dbError(err)
	}
	defer d.Close()

	msgs, err := mgr.Recv(context.Background(), *recipient, *unacked, *sinceID, *limit)
	if err != nil {
		return dbError(err)
	}
	if *asJSON {
		return writeJSON(os.Stdout, messagesToJSON(msgs))
	}
	printMessagesTable(os.Stdout, msgs)
	return nil
}

func runAck(args []string) error {
	if len(args) != 1 {
		return userError("usage: coda-core ack <id>")
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return userError("invalid id %q: %v", args[0], err)
	}
	d, mgr, err := openMessages()
	if err != nil {
		return dbError(err)
	}
	defer d.Close()

	if err := mgr.Ack(context.Background(), id); err != nil {
		if errors.Is(err, messages.ErrNotFound) {
			return userError("%v", err)
		}
		return dbError(err)
	}
	fmt.Printf("acked %d\n", id)
	return nil
}

func runDrain(args []string) error {
	fs := flag.NewFlagSet("drain", flag.ContinueOnError)
	recipient := fs.String("recipient", "", "recipient name (required)")
	if err := parseInterleaved(fs, args); err != nil {
		return userError("%v", err)
	}
	if *recipient == "" {
		return userError("usage: coda-core drain --recipient <name>")
	}

	d, mgr, err := openMessages()
	if err != nil {
		return dbError(err)
	}
	defer d.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	n, err := mgr.Drain(ctx, *recipient)
	if err != nil {
		return dbError(err)
	}
	fmt.Printf("drained %d messages\n", n)
	return nil
}

func messagesToJSON(msgs []*messages.Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, msg := range msgs {
		m := map[string]any{
			"id":         msg.ID,
			"sender":     msg.Sender,
			"recipient":  msg.Recipient,
			"type":       string(msg.Type),
			"body":       json.RawMessage(msg.Body),
			"created_at": msg.CreatedAt,
		}
		if msg.ParentID.Valid {
			m["parent_id"] = msg.ParentID.Int64
		}
		if msg.DeliveredAt.Valid {
			m["delivered_at"] = msg.DeliveredAt.Int64
		}
		if msg.AckedAt.Valid {
			m["acked_at"] = msg.AckedAt.Int64
		}
		out = append(out, m)
	}
	return out
}

func printMessagesTable(w io.Writer, msgs []*messages.Message) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tFROM\tTYPE\tCREATED\tDELIVERED\tACKED\tPREVIEW")
	for _, msg := range msgs {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			msg.ID, msg.Sender, msg.Type,
			time.Unix(msg.CreatedAt, 0).UTC().Format(time.RFC3339),
			yesNo(msg.DeliveredAt.Valid), yesNo(msg.AckedAt.Valid),
			previewBody(msg.Body))
	}
	if len(msgs) == 0 {
		fmt.Fprintln(tw, "(no messages)")
	}
	tw.Flush()
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func previewBody(body string) string {
	flat := strings.ReplaceAll(body, "\n", " ")
	flat = strings.ReplaceAll(flat, "\t", " ")
	const max = 60
	if len(flat) > max {
		return flat[:max] + "…"
	}
	return flat
}

// printUnackedTable renders unacked message counts for the live
// orchestrators in orchs. Only orchestrators with count > 0 are
// shown; zero rows are noise. Messages addressed to recipients not
// in orchs (deleted or unknown orchestrators) are intentionally not
// surfaced here — they remain readable via
// `coda-core recv --recipient <name>`.
func printUnackedTable(w io.Writer, orchs []string, unacked map[string]int) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "RECIPIENT\tUNACKED")
	any := false
	for _, name := range orchs {
		n := unacked[name]
		if n == 0 {
			continue
		}
		fmt.Fprintf(tw, "%s\t%d\n", name, n)
		any = true
	}
	if !any {
		return
	}
	tw.Flush()
}
