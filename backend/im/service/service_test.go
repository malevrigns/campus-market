package service_test

import (
	"context"
	"testing"

	"qingfly/edu-mall/im/model"
	"qingfly/edu-mall/im/service"
)

// nopPusher satisfies service.Pusher for tests (no real fan-out).
type nopPusher struct{}
func (nopPusher) Push(context.Context, string, string, *model.Message) error { return nil }

// TestIMSeqMonotonicAndIdempotent proves Part-3 invariants:
//   - server assigns strictly increasing seq per conversation (never client time),
//   - resending the same clientMsgID is deduped (no duplicate message).
func TestIMSeqMonotonicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	svc := service.NewService(nopPusher{})
	if _, err := svc.EnsureConversation(ctx, "c1", "p2p", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	m1, _, err := svc.SendMessage(ctx, "a", "c1", "cmid-1", model.MsgText, "hi")
	if err != nil {
		t.Fatal(err)
	}
	m2, _, err := svc.SendMessage(ctx, "a", "c1", "cmid-2", model.MsgText, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if m2.Seq <= m1.Seq {
		t.Fatalf("seq must be strictly increasing: m1=%d m2=%d", m1.Seq, m2.Seq)
	}
	// Resend the SAME clientMsgID: must be deduped -> returns the ORIGINAL message
	// (no new seq consumed) and signals the duplicate via service.ErrDupClientMsg.
	m3, _, err := svc.SendMessage(ctx, "a", "c1", "cmid-1", model.MsgText, "hi")
	if err != service.ErrDupClientMsg {
		t.Fatalf("expected ErrDupClientMsg on resend, got %v", err)
	}
	if m3.Seq != m1.Seq || m3.ID != m1.ID {
		t.Fatalf("dedup must return original message: got id=%s seq=%d want id=%s seq=%d", m3.ID, m3.Seq, m1.ID, m1.Seq)
	}
	// Sync from afterSeq=0 returns both distinct messages in order.
	msgs, lastSeq, err := svc.Sync(ctx, "b", "c1", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages after dedup, got %d", len(msgs))
	}
	if msgs[0].Seq > msgs[1].Seq {
		t.Fatal("sync must return ascending seq")
	}
	_ = lastSeq
}
