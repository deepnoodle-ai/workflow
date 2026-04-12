package worker

import (
	"context"
	"encoding/json"
	"time"
)

// --- Lifecycle hooks called from execute() ---

func (w *Worker) emitEvent(runID string, eventType string, attempt int, payload map[string]any) {
	if w.cfg.EventStore == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.cfg.EventStore.AppendEvent(ctx, &Event{
		RunID:     runID,
		EventType: eventType,
		Attempt:   attempt,
		WorkerID:  w.cfg.WorkerID,
		Payload:   payload,
		CreatedAt: w.cfg.Clock(),
	}); err != nil {
		w.cfg.Logger.Error("emit event failed", "run_id", runID, "type", eventType, "error", err)
	}
}

func (w *Worker) debitCredits(claim *Claim) bool {
	if w.cfg.CreditStore == nil || claim.CreditCost <= 0 {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.cfg.CreditStore.Debit(ctx, claim.OrgID, claim.ID, claim.WorkflowType, claim.CreditCost); err != nil {
		w.cfg.Logger.Error("debit credits failed", "run_id", claim.ID, "error", err)
		return false
	}
	return true
}

func (w *Worker) refundCredits(claim *Claim) {
	if w.cfg.CreditStore == nil || claim.CreditCost <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := w.cfg.CreditStore.Refund(ctx, claim.OrgID, claim.ID, claim.WorkflowType, claim.CreditCost); err != nil {
		w.cfg.Logger.Error("refund credits failed", "run_id", claim.ID, "error", err)
	}
}

func (w *Worker) writeTriggers(claim *Claim, outcome Outcome) {
	if w.cfg.TriggerStore == nil || len(outcome.Triggers) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	triggers := make([]Trigger, len(outcome.Triggers))
	for i, child := range outcome.Triggers {
		triggers[i] = Trigger{
			ID:          w.cfg.IDGenerator(),
			ParentRunID: claim.ID,
			ChildSpec:   child,
			Status:      TriggerPending,
			CreatedAt:   w.cfg.Clock(),
		}
	}
	if err := w.cfg.TriggerStore.InsertTriggers(ctx, triggers); err != nil {
		w.cfg.Logger.Error("write triggers failed", "run_id", claim.ID, "error", err)
		return
	}
	w.notifyTriggers()
}

func (w *Worker) enqueueWebhook(claim *Claim, outcome Outcome) {
	if w.cfg.WebhookStore == nil || claim.CallbackURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payload, _ := json.Marshal(map[string]any{
		"run_id":        claim.ID,
		"status":        string(outcome.Status),
		"org_id":        claim.OrgID,
		"workflow_type": claim.WorkflowType,
		"attempt":       claim.Attempt,
	})
	if err := w.cfg.WebhookStore.EnqueueWebhook(ctx, &WebhookDelivery{
		ID:        w.cfg.IDGenerator(),
		RunID:     claim.ID,
		URL:       claim.CallbackURL,
		EventType: "workflow." + string(outcome.Status),
		Payload:   payload,
		Status:    "pending",
		CreatedAt: w.cfg.Clock(),
	}); err != nil {
		w.cfg.Logger.Error("enqueue webhook failed", "run_id", claim.ID, "error", err)
	}
}

func (w *Worker) afterComplete(claim *Claim, outcome Outcome, debited bool) {
	w.emitEvent(claim.ID, string(outcome.Status), claim.Attempt, nil)
	if outcome.Status == StatusCompleted {
		w.writeTriggers(claim, outcome)
	}
	w.enqueueWebhook(claim, outcome)
	if outcome.Status == StatusFailed && debited {
		w.refundCredits(claim)
	}
}

func (w *Worker) notifyTriggers() {
	select {
	case w.triggerNotify <- struct{}{}:
	default:
	}
}

// --- Background loops ---

func (w *Worker) triggerLoop(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.TriggerInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processTriggers(ctx)
		case <-w.triggerNotify:
			w.processTriggers(ctx)
		}
	}
}

func (w *Worker) processTriggers(ctx context.Context) {
	pending, err := w.cfg.TriggerStore.ListPendingTriggers(ctx, 10)
	if err != nil {
		w.cfg.Logger.Error("list pending triggers", "error", err)
		return
	}
	for _, t := range pending {
		if t.Attempts >= w.cfg.TriggerMaxAttempts {
			if err := w.cfg.TriggerStore.MarkTriggerFailed(ctx, t.ID, "exceeded max trigger attempts"); err != nil {
				w.cfg.Logger.Error("mark trigger failed", "trigger_id", t.ID, "error", err)
			}
			continue
		}
		if err := w.cfg.TriggerStore.MarkTriggerProcessing(ctx, t.ID); err != nil {
			w.cfg.Logger.Error("mark trigger processing", "trigger_id", t.ID, "error", err)
			continue
		}
		if err := w.store.Enqueue(ctx, t.ChildSpec); err != nil {
			if err2 := w.cfg.TriggerStore.IncrementTriggerAttempts(ctx, t.ID, err.Error()); err2 != nil {
				w.cfg.Logger.Error("increment trigger attempts", "trigger_id", t.ID, "error", err2)
			}
			continue
		}
		if err := w.cfg.TriggerStore.MarkTriggerCompleted(ctx, t.ID, t.ChildSpec.ID); err != nil {
			w.cfg.Logger.Error("mark trigger completed", "trigger_id", t.ID, "error", err)
		}
		w.Notify()
	}
}

func (w *Worker) webhookLoop(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.WebhookInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.processWebhooks(ctx)
		}
	}
}

func (w *Worker) processWebhooks(ctx context.Context) {
	pending, err := w.cfg.WebhookStore.ListPendingWebhooks(ctx, 10)
	if err != nil {
		w.cfg.Logger.Error("list pending webhooks", "error", err)
		return
	}
	for _, d := range pending {
		if d.Attempts >= w.cfg.WebhookMaxAttempts {
			if err := w.cfg.WebhookStore.MarkWebhookFailed(ctx, d.ID, "exceeded max delivery attempts"); err != nil {
				w.cfg.Logger.Error("mark webhook failed", "webhook_id", d.ID, "error", err)
			}
			continue
		}
		if err := w.cfg.WebhookStore.MarkWebhookProcessing(ctx, d.ID); err != nil {
			w.cfg.Logger.Debug("webhook already claimed", "webhook_id", d.ID, "error", err)
			continue
		}
		deliverCtx, deliverCancel := context.WithTimeout(ctx, 30*time.Second)
		err := w.cfg.WebhookDeliverer.Deliver(deliverCtx, d.URL, d.Payload)
		deliverCancel()
		if err != nil {
			if err2 := w.cfg.WebhookStore.IncrementWebhookAttempts(ctx, d.ID, err.Error()); err2 != nil {
				w.cfg.Logger.Error("increment webhook attempts", "webhook_id", d.ID, "error", err2)
			}
			continue
		}
		if err := w.cfg.WebhookStore.MarkWebhookDelivered(ctx, d.ID); err != nil {
			w.cfg.Logger.Error("mark webhook delivered", "webhook_id", d.ID, "error", err)
		}
	}
}

func (w *Worker) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.ReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.reconcileCredits(ctx)
		}
	}
}

func (w *Worker) reconcileCredits(ctx context.Context) {
	failed, err := w.store.ListFailedWithCredits(ctx, 50)
	if err != nil {
		w.cfg.Logger.Error("list failed with credits", "error", err)
		return
	}
	for _, f := range failed {
		if f.CreditCost <= 0 {
			continue
		}
		refunded, err := w.cfg.CreditStore.HasRefund(ctx, f.OrgID, f.ID)
		if err != nil {
			w.cfg.Logger.Error("has refund check", "run_id", f.ID, "error", err)
			continue
		}
		if refunded {
			continue
		}
		if err := w.cfg.CreditStore.Refund(ctx, f.OrgID, f.ID, f.WorkflowType, f.CreditCost); err != nil {
			w.cfg.Logger.Error("reconcile refund failed", "run_id", f.ID, "error", err)
		}
	}
}
