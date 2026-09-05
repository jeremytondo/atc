package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jeremytondo/atc/internal/store/gen"
)

// WebhookDelivery is one inbox row in domain terms: an accepted delivery
// awaiting or under processing. Payload is what the Integration chose to
// preserve; it is nil once the delivery is done and only the receipt
// remains.
type WebhookDelivery struct {
	// ID is the ATC-minted identity, stable for the delivery's life.
	ID            string
	IntegrationID string
	// Route is the path the delivery arrived on; processing dispatches by
	// it.
	Route string
	// DeliveryID is the Integration's protocol identity for the delivery;
	// unique per Integration.
	DeliveryID    string
	Payload       []byte
	Attempts      int
	NextAttemptAt time.Time
	AcceptedAt    time.Time
}

// ErrInboxFull refuses an acceptance that would take the inbox past its
// pending capacity. Accepted work is never evicted to make room; the
// sender is told to retry.
var ErrInboxFull = errors.New("webhook inbox is at capacity")

// Webhooks is the durable inbox repository. Reads go to the read pool,
// mutations to the single-writer pool; db is that pool's handle, for the
// capacity-checked acceptance transaction.
type Webhooks struct {
	reads  *gen.Queries
	writes *gen.Queries
	db     *sql.DB
}

// Webhooks returns the inbox repository.
func (s *Store) Webhooks() *Webhooks {
	return &Webhooks{reads: gen.New(s.reads), writes: gen.New(s.writes), db: s.writes}
}

// Accept stores a pending delivery and reports true; false means a
// delivery with the same Integration-scoped identity already exists
// (pending or as a receipt), so the redelivery is acknowledged without a
// new row — at capacity too, since nothing new is stored. The insert and
// the capacity check share one immediate write transaction, so concurrent
// redeliveries and a full inbox are decided atomically under the single
// writer; ErrInboxFull rolls the insert back.
func (w *Webhooks) Accept(ctx context.Context, record WebhookDelivery, capacity int) (bool, error) {
	tx, err := w.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := gen.New(tx)
	n, err := queries.InsertWebhookDelivery(ctx, gen.InsertWebhookDeliveryParams{
		ID:            record.ID,
		IntegrationID: record.IntegrationID,
		Route:         record.Route,
		DeliveryID:    record.DeliveryID,
		Payload:       record.Payload,
		NextAttemptAt: formatTime(record.NextAttemptAt),
		AcceptedAt:    formatTime(record.AcceptedAt),
	})
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, tx.Commit()
	}
	pending, err := queries.CountPendingWebhookDeliveries(ctx)
	if err != nil {
		return false, err
	}
	if pending > int64(capacity) {
		return false, ErrInboxFull
	}
	return true, tx.Commit()
}

// Due lists pending deliveries whose next attempt is at or before now,
// oldest attempt first, at most limit.
func (w *Webhooks) Due(ctx context.Context, now time.Time, limit int) ([]WebhookDelivery, error) {
	rows, err := w.reads.ListDueWebhookDeliveries(ctx, gen.ListDueWebhookDeliveriesParams{
		NextAttemptAt: formatTime(now), Limit: int64(limit),
	})
	if err != nil {
		return nil, err
	}
	records := make([]WebhookDelivery, 0, len(rows))
	for _, row := range rows {
		record, err := webhookDeliveryFrom(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// Complete marks a pending delivery done, dropping its payload and keeping
// the receipt; false means no pending delivery with that id.
func (w *Webhooks) Complete(ctx context.Context, id string, at time.Time) (bool, error) {
	n, err := w.writes.CompleteWebhookDelivery(ctx, gen.CompleteWebhookDeliveryParams{CompletedAt: nullString(formatTime(at)), ID: id})
	return n > 0, err
}

// Fail records a failed processing attempt and when to try again; false
// means no pending delivery with that id.
func (w *Webhooks) Fail(ctx context.Context, id string, attempts int, next time.Time) (bool, error) {
	n, err := w.writes.FailWebhookDelivery(ctx, gen.FailWebhookDeliveryParams{
		Attempts: int64(attempts), NextAttemptAt: formatTime(next), ID: id,
	})
	return n > 0, err
}

// Pending counts accepted deliveries not yet done.
func (w *Webhooks) Pending(ctx context.Context) (int, error) {
	n, err := w.reads.CountPendingWebhookDeliveries(ctx)
	return int(n), err
}

// Prune bounds the receipts: those completed before cutoff go, and past
// keep of them the oldest go too. Pending deliveries are never pruned.
func (w *Webhooks) Prune(ctx context.Context, cutoff time.Time, keep int) error {
	if _, err := w.writes.PruneAgedWebhookReceipts(ctx, nullString(formatTime(cutoff))); err != nil {
		return err
	}
	_, err := w.writes.PruneExcessWebhookReceipts(ctx, int64(keep))
	return err
}

func webhookDeliveryFrom(row gen.WebhookDelivery) (WebhookDelivery, error) {
	next, err := parseTime(row.NextAttemptAt)
	if err != nil {
		return WebhookDelivery{}, err
	}
	accepted, err := parseTime(row.AcceptedAt)
	if err != nil {
		return WebhookDelivery{}, err
	}
	return WebhookDelivery{
		ID:            row.ID,
		IntegrationID: row.IntegrationID,
		Route:         row.Route,
		DeliveryID:    row.DeliveryID,
		Payload:       row.Payload,
		Attempts:      int(row.Attempts),
		NextAttemptAt: next,
		AcceptedAt:    accepted,
	}, nil
}
