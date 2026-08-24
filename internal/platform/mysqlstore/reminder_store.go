package mysqlstore

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/reminder"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type reminderRow struct {
	ID, Content, ContentHash, Timezone, Status, SourceType, SourceID, LastErrorCode string
	TenantID, UserID, RowVersion                                                    uint64
	NextFireAt, CreatedAt, UpdatedAt                                                time.Time
	ClaimToken                                                                      *string
	LeaseUntil                                                                      *time.Time
}

func (reminderRow) TableName() string { return "reminders" }

type reminderMemoryRefRow struct {
	ReminderID            string
	TenantID, UserID      uint64
	MemoryID, ContentHash string
	LineageVersion        uint64
	CreatedAt             time.Time
}

func (reminderMemoryRefRow) TableName() string { return "reminder_memory_refs" }

type reminderEventRow struct {
	ID                                                   string
	TenantID, UserID                                     uint64
	ReminderID, EventType                                string
	OldStatus                                            *string
	NewStatus, Actor, ReasonCode, ExecutionID, InputHash string
	OccurredAt                                           time.Time
}

func (reminderEventRow) TableName() string { return "reminder_events" }

type reminderDeliveryRow struct {
	ID, ReminderID                             string
	TenantID, UserID                           uint64
	OccurrenceID, DeliveryKey, Content, Status string
	Attempt                                    int
	AvailableAt                                time.Time
	ClaimToken                                 *string
	LeaseUntil, ProcessedAt                    *time.Time
	LastErrorCode                              string
	CreatedAt                                  time.Time
}

func (reminderDeliveryRow) TableName() string { return "reminder_delivery_outbox" }

func (s *Store) Create(ctx context.Context, input reminder.CreateInput) (result reminder.MutationResult, err error) {
	value := input.Reminder
	if input.IdempotencyKey == "" || input.InputHash == "" || !value.Owner.Valid() {
		return result, reminder.ErrInvalidInput
	}
	if err := value.Validate(value.CreatedAt, reminder.DefaultMaxHorizon); err != nil {
		return result, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if replay, found, loadErr := loadReminderIdempotency(tx, value.Owner, input.IdempotencyKey, input.InputHash); loadErr != nil {
			return loadErr
		} else if found {
			replay.Replayed = true
			result = replay
			return nil
		}
		if err := tx.Create(reminderToRow(value)).Error; err != nil {
			return mapReminderWriteError(err)
		}
		if err := replaceReminderRefs(tx, value); err != nil {
			return err
		}
		if err := insertReminderEvent(tx, value.Owner, value.ID, "created", nil, value.Status, input.Actor, input.ReasonCode, input.IdempotencyKey, input.InputHash, value.CreatedAt); err != nil {
			return err
		}
		result = reminder.MutationResult{Reminder: value}
		return nil
	})
	return
}

func (s *Store) Get(ctx context.Context, owner reminder.Owner, id string) (reminder.Reminder, error) {
	var row reminderRow
	err := s.db.WithContext(ctx).Where("tenant_id=? AND user_id=? AND id=?", owner.TenantID, owner.UserID, id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return reminder.Reminder{}, reminder.ErrNotFound
	}
	if err != nil {
		return reminder.Reminder{}, err
	}
	return s.reminderFromRow(ctx, row)
}

func (s *Store) List(ctx context.Context, query reminder.Query) (reminder.Page, error) {
	if err := query.Validate(); err != nil {
		return reminder.Page{}, err
	}
	db := s.db.WithContext(ctx).Where("tenant_id=? AND user_id=?", query.Owner.TenantID, query.Owner.UserID)
	if len(query.Statuses) > 0 {
		db = db.Where("status IN ?", query.Statuses)
	}
	if query.From != nil {
		db = db.Where("next_fire_at>=?", query.From.UTC())
	}
	if query.Until != nil {
		db = db.Where("next_fire_at<?", query.Until.UTC())
	}
	if query.Label != "" {
		db = db.Where("content LIKE ?", "%"+escapeLike(query.Label)+"%")
	}
	if query.CursorAt != nil {
		db = db.Where("next_fire_at>? OR (next_fire_at=? AND id>?)", query.CursorAt.UTC(), query.CursorAt.UTC(), query.CursorID)
	}
	var rows []reminderRow
	if err := db.Order("next_fire_at ASC,id ASC").Limit(query.Limit + 1).Find(&rows).Error; err != nil {
		return reminder.Page{}, err
	}
	page := reminder.Page{}
	if len(rows) > query.Limit {
		rows = rows[:query.Limit]
		page.Truncated = true
		last := rows[len(rows)-1]
		cursor := last.NextFireAt
		page.NextAt, page.NextID = &cursor, last.ID
	}
	for _, row := range rows {
		value, err := s.reminderFromRow(ctx, row)
		if err != nil {
			return reminder.Page{}, err
		}
		page.Items = append(page.Items, value)
	}
	return page, nil
}

func (s *Store) Update(ctx context.Context, input reminder.MutationInput) (reminder.MutationResult, error) {
	return s.mutateReminder(ctx, input, false)
}
func (s *Store) Cancel(ctx context.Context, input reminder.MutationInput) (reminder.MutationResult, error) {
	return s.mutateReminder(ctx, input, true)
}

func (s *Store) mutateReminder(ctx context.Context, input reminder.MutationInput, cancel bool) (result reminder.MutationResult, err error) {
	if !input.Owner.Valid() || input.Target.Validate() != nil || input.IdempotencyKey == "" || input.InputHash == "" {
		return result, reminder.ErrInvalidInput
	}
	now := input.OccurredAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if replay, found, loadErr := loadReminderIdempotency(tx, input.Owner, input.IdempotencyKey, input.InputHash); loadErr != nil {
			return loadErr
		} else if found {
			replay.Replayed = true
			result = replay
			return nil
		}
		value, loadErr := loadReminderLocked(tx, input.Owner, input.Target.ID)
		if loadErr != nil {
			return loadErr
		}
		if value.RowVersion != input.Target.RowVersion || value.Status != reminder.StatusScheduled {
			return reminder.ErrStateConflict
		}
		old := value.Status
		updates := map[string]any{"row_version": gorm.Expr("row_version+1"), "updated_at": now}
		eventType := "updated"
		if cancel {
			value.Status = reminder.StatusCancelled
			value.Claim = nil
			updates["status"], updates["claim_token"], updates["lease_until"], eventType = value.Status, nil, nil, "cancelled"
		} else {
			normalized, normalizeErr := reminder.NormalizeContent(input.Content)
			if normalizeErr != nil || normalized != input.Content {
				if normalizeErr != nil {
					return normalizeErr
				}
				return reminder.ErrInvalidInput
			}
			hash, hashErr := reminder.ComputeContentHash(input.Content, input.Timezone, input.NextFireAt, input.MemoryRefs)
			if hashErr != nil || hash != input.ReplacementHash || !input.NextFireAt.After(now) {
				return reminder.ErrInvalidInput
			}
			value.Content, value.Timezone, value.NextFireAt, value.ContentHash, value.MemoryRefs = input.Content, input.Timezone, input.NextFireAt.UTC(), hash, append([]reminder.MemoryRef(nil), input.MemoryRefs...)
			updates["content"], updates["timezone"], updates["next_fire_at"], updates["content_hash"] = value.Content, value.Timezone, value.NextFireAt, value.ContentHash
		}
		write := tx.Model(&reminderRow{}).Where("tenant_id=? AND user_id=? AND id=? AND status=? AND row_version=?", input.Owner.TenantID, input.Owner.UserID, value.ID, old, input.Target.RowVersion).Updates(updates)
		if write.Error != nil {
			return mapReminderWriteError(write.Error)
		}
		if write.RowsAffected != 1 {
			return reminder.ErrStateConflict
		}
		value.RowVersion++
		value.UpdatedAt = now
		if !cancel {
			if err := replaceReminderRefs(tx, value); err != nil {
				return err
			}
		}
		if err := insertReminderEvent(tx, input.Owner, value.ID, eventType, stringPtr(string(old)), value.Status, input.Actor, input.ReasonCode, input.IdempotencyKey, input.InputHash, now); err != nil {
			return err
		}
		result = reminder.MutationResult{Reminder: value}
		return nil
	})
	return
}

func (s *Store) ClaimDue(ctx context.Context, request reminder.DueClaimRequest) (out []reminder.Reminder, err error) {
	if request.Limit < 1 || request.Limit > reminder.MaxPageSize || request.Now.IsZero() || request.LeaseDuration <= 0 || request.Token == "" {
		return nil, reminder.ErrInvalidInput
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []reminderRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status='scheduled' AND next_fire_at<=? AND (lease_until IS NULL OR lease_until<=?)", request.Now.UTC(), request.Now.UTC()).Order("next_fire_at ASC,id ASC").Limit(request.Limit).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			until := request.Now.Add(request.LeaseDuration).UTC()
			write := tx.Model(&reminderRow{}).Where("id=? AND row_version=? AND status='scheduled'", row.ID, row.RowVersion).Updates(map[string]any{"claim_token": request.Token, "lease_until": until, "row_version": gorm.Expr("row_version+1"), "updated_at": request.Now.UTC()})
			if write.Error != nil {
				return write.Error
			}
			if write.RowsAffected != 1 {
				continue
			}
			row.ClaimToken, row.LeaseUntil, row.RowVersion, row.UpdatedAt = &request.Token, &until, row.RowVersion+1, request.Now.UTC()
			value, convErr := s.reminderFromRowWith(tx, row)
			if convErr != nil {
				return convErr
			}
			out = append(out, value)
		}
		return nil
	})
	return
}

func (s *Store) RenewClaim(ctx context.Context, id string, version uint64, token string, until time.Time) error {
	write := s.db.WithContext(ctx).Model(&reminderRow{}).Where("id=? AND row_version=? AND status='scheduled' AND claim_token=? AND lease_until>?", id, version, token, time.Now().UTC()).Update("lease_until", until.UTC())
	if write.Error != nil {
		return write.Error
	}
	if write.RowsAffected != 1 {
		return reminder.ErrLeaseLost
	}
	return nil
}

func (s *Store) CommitOccurrence(ctx context.Context, input reminder.CommitOccurrenceInput) (delivery reminder.Delivery, replayed bool, err error) {
	if input.ReminderID == "" || input.ExpectedRowVersion == 0 || input.ClaimToken == "" || input.OccurrenceID == "" || input.OccurredAt.IsZero() {
		return delivery, false, reminder.ErrInvalidInput
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var existing reminderDeliveryRow
		find := tx.Where("occurrence_id=?", input.OccurrenceID).First(&existing).Error
		if find == nil {
			delivery = deliveryFromRow(existing)
			replayed = true
			return nil
		}
		if !errors.Is(find, gorm.ErrRecordNotFound) {
			return find
		}
		var row reminderRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", input.ReminderID).First(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return reminder.ErrNotFound
		} else if err != nil {
			return err
		}
		if row.RowVersion != input.ExpectedRowVersion || row.Status != string(reminder.StatusScheduled) || row.ClaimToken == nil || *row.ClaimToken != input.ClaimToken || row.LeaseUntil == nil || !row.LeaseUntil.After(input.OccurredAt) {
			return reminder.ErrLeaseLost
		}
		write := tx.Model(&reminderRow{}).Where("id=? AND row_version=? AND status='scheduled' AND claim_token=?", row.ID, row.RowVersion, input.ClaimToken).Updates(map[string]any{"status": reminder.StatusProcessing, "row_version": gorm.Expr("row_version+1"), "claim_token": nil, "lease_until": nil, "updated_at": input.OccurredAt.UTC()})
		if write.Error != nil {
			return write.Error
		}
		if write.RowsAffected != 1 {
			return reminder.ErrLeaseLost
		}
		delivery = reminder.Delivery{ID: input.OccurrenceID, ReminderID: row.ID, Owner: reminder.Owner{TenantID: row.TenantID, UserID: row.UserID}, Content: row.Content, DeliveryKey: input.OccurrenceID, Status: reminder.DeliveryPending, AvailableAt: input.OccurredAt.UTC()}
		if err := tx.Create(deliveryToRow(delivery, input.OccurrenceID, input.OccurredAt.UTC())).Error; err != nil {
			return mapReminderWriteError(err)
		}
		if err := insertReminderEvent(tx, delivery.Owner, row.ID, "triggered", stringPtr(row.Status), reminder.StatusProcessing, "system", "due", "occurrence:"+input.OccurrenceID, row.ContentHash, input.OccurredAt.UTC()); err != nil {
			return err
		}
		return nil
	})
	return
}

func (s *Store) ClaimDeliveries(ctx context.Context, limit int, now time.Time, lease time.Duration, token string) (out []reminder.Delivery, err error) {
	if limit < 1 || limit > reminder.MaxPageSize || now.IsZero() || lease <= 0 || token == "" {
		return nil, reminder.ErrInvalidInput
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []reminderDeliveryRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status IN ? AND available_at<=? AND (lease_until IS NULL OR lease_until<=?)", []string{string(reminder.DeliveryPending), string(reminder.DeliveryProcessing)}, now.UTC(), now.UTC()).Order("available_at ASC,id ASC").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			until := now.Add(lease).UTC()
			write := tx.Model(&reminderDeliveryRow{}).Where("id=? AND status=?", row.ID, row.Status).Updates(map[string]any{"status": reminder.DeliveryProcessing, "attempt": gorm.Expr("attempt+1"), "claim_token": token, "lease_until": until})
			if write.Error != nil {
				return write.Error
			}
			if write.RowsAffected != 1 {
				continue
			}
			row.Status, row.Attempt, row.ClaimToken, row.LeaseUntil = string(reminder.DeliveryProcessing), row.Attempt+1, &token, &until
			out = append(out, deliveryFromRow(row))
		}
		return nil
	})
	return
}

func (s *Store) CompleteDelivery(ctx context.Context, id, token string, now time.Time) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row reminderDeliveryRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id=?", id).First(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return reminder.ErrNotFound
		} else if err != nil {
			return err
		}
		if row.Status == string(reminder.DeliveryCompleted) {
			return nil
		}
		if row.Status != string(reminder.DeliveryProcessing) || row.ClaimToken == nil || *row.ClaimToken != token {
			return reminder.ErrLeaseLost
		}
		if err := tx.Model(&reminderDeliveryRow{}).Where("id=? AND claim_token=?", id, token).Updates(map[string]any{"status": reminder.DeliveryCompleted, "claim_token": nil, "lease_until": nil, "processed_at": now.UTC(), "last_error_code": ""}).Error; err != nil {
			return err
		}
		write := tx.Model(&reminderRow{}).Where("id=? AND status='processing'", row.ReminderID).Updates(map[string]any{"status": reminder.StatusFired, "row_version": gorm.Expr("row_version+1"), "updated_at": now.UTC()})
		if write.Error != nil {
			return write.Error
		}
		if write.RowsAffected != 1 {
			return reminder.ErrStateConflict
		}
		return insertReminderEvent(tx, reminder.Owner{TenantID: row.TenantID, UserID: row.UserID}, row.ReminderID, "delivered", stringPtr(string(reminder.StatusProcessing)), reminder.StatusFired, "system", "delivered", "delivery:"+row.DeliveryKey, strings.Repeat("0", 64), now.UTC())
	})
}

func (s *Store) FailDelivery(ctx context.Context, failure reminder.DeliveryFailure) error {
	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		status := reminder.DeliveryPending
		updates := map[string]any{"status": status, "available_at": failure.NextAvailable.UTC(), "claim_token": nil, "lease_until": nil, "last_error_code": failure.ErrorCode}
		if failure.Permanent {
			status = reminder.DeliveryPermanentFailed
			updates["status"] = status
		}
		write := tx.Model(&reminderDeliveryRow{}).Where("id=? AND status='processing' AND claim_token=?", failure.ID, failure.ClaimToken).Updates(updates)
		if write.Error != nil {
			return write.Error
		}
		if write.RowsAffected != 1 {
			return reminder.ErrLeaseLost
		}
		if !failure.Permanent {
			return nil
		}
		var row reminderDeliveryRow
		if err := tx.Where("id=?", failure.ID).First(&row).Error; err != nil {
			return err
		}
		write = tx.Model(&reminderRow{}).Where("id=? AND status='processing'", row.ReminderID).Updates(map[string]any{"status": reminder.StatusFailed, "last_error_code": failure.ErrorCode, "row_version": gorm.Expr("row_version+1"), "updated_at": failure.Now.UTC()})
		if write.Error != nil {
			return write.Error
		}
		if write.RowsAffected != 1 {
			return reminder.ErrStateConflict
		}
		return insertReminderEvent(tx, reminder.Owner{TenantID: row.TenantID, UserID: row.UserID}, row.ReminderID, "delivery_failed", stringPtr(string(reminder.StatusProcessing)), reminder.StatusFailed, "system", failure.ErrorCode, "delivery-failed:"+row.DeliveryKey, strings.Repeat("0", 64), failure.Now.UTC())
	})
}

func loadReminderLocked(tx *gorm.DB, owner reminder.Owner, id string) (reminder.Reminder, error) {
	var row reminderRow
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND user_id=? AND id=?", owner.TenantID, owner.UserID, id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return reminder.Reminder{}, reminder.ErrNotFound
	}
	if err != nil {
		return reminder.Reminder{}, err
	}
	return reminderFromRowDB(tx, row)
}
func loadReminderIdempotency(tx *gorm.DB, owner reminder.Owner, key, hash string) (reminder.MutationResult, bool, error) {
	var event reminderEventRow
	err := tx.Where("tenant_id=? AND user_id=? AND execution_id=?", owner.TenantID, owner.UserID, key).First(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return reminder.MutationResult{}, false, nil
	}
	if err != nil {
		return reminder.MutationResult{}, false, err
	}
	if event.InputHash != hash {
		return reminder.MutationResult{}, false, reminder.ErrIdempotencyConflict
	}
	var row reminderRow
	if err := tx.Where("tenant_id=? AND user_id=? AND id=?", owner.TenantID, owner.UserID, event.ReminderID).First(&row).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return reminder.MutationResult{}, false, reminder.ErrNotFound
	} else if err != nil {
		return reminder.MutationResult{}, false, err
	}
	value, err := reminderFromRowDB(tx, row)
	return reminder.MutationResult{Reminder: value}, true, err
}
func (s *Store) reminderFromRow(ctx context.Context, row reminderRow) (reminder.Reminder, error) {
	return s.reminderFromRowWith(s.db.WithContext(ctx), row)
}
func (s *Store) reminderFromRowWith(db *gorm.DB, row reminderRow) (reminder.Reminder, error) {
	return reminderFromRowDB(db, row)
}
func reminderFromRowDB(db *gorm.DB, row reminderRow) (reminder.Reminder, error) {
	var refs []reminderMemoryRefRow
	if err := db.Where("reminder_id=? AND tenant_id=? AND user_id=?", row.ID, row.TenantID, row.UserID).Order("memory_id").Find(&refs).Error; err != nil {
		return reminder.Reminder{}, err
	}
	value := reminder.Reminder{ID: row.ID, Owner: reminder.Owner{TenantID: row.TenantID, UserID: row.UserID}, Content: row.Content, ContentHash: row.ContentHash, Timezone: row.Timezone, NextFireAt: row.NextFireAt, Status: reminder.Status(row.Status), RowVersion: row.RowVersion, Source: reminder.SourceRef{Type: row.SourceType, ID: row.SourceID}, LastErrorCode: row.LastErrorCode, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.ClaimToken != nil && row.LeaseUntil != nil {
		value.Claim = &reminder.Claim{Token: *row.ClaimToken, LeaseUntil: *row.LeaseUntil}
	}
	for _, ref := range refs {
		value.MemoryRefs = append(value.MemoryRefs, reminder.MemoryRef{ID: ref.MemoryID, LineageVersion: ref.LineageVersion, ContentHash: ref.ContentHash})
	}
	return value, nil
}
func reminderToRow(value reminder.Reminder) *reminderRow {
	row := &reminderRow{ID: value.ID, TenantID: value.Owner.TenantID, UserID: value.Owner.UserID, Content: value.Content, ContentHash: value.ContentHash, Timezone: value.Timezone, NextFireAt: value.NextFireAt.UTC(), Status: string(value.Status), RowVersion: value.RowVersion, SourceType: value.Source.Type, SourceID: value.Source.ID, LastErrorCode: value.LastErrorCode, CreatedAt: value.CreatedAt.UTC(), UpdatedAt: value.UpdatedAt.UTC()}
	if value.Claim != nil {
		row.ClaimToken, row.LeaseUntil = &value.Claim.Token, &value.Claim.LeaseUntil
	}
	return row
}
func replaceReminderRefs(tx *gorm.DB, value reminder.Reminder) error {
	if err := tx.Where("reminder_id=?", value.ID).Delete(&reminderMemoryRefRow{}).Error; err != nil {
		return err
	}
	for _, ref := range value.MemoryRefs {
		if err := tx.Create(&reminderMemoryRefRow{ReminderID: value.ID, TenantID: value.Owner.TenantID, UserID: value.Owner.UserID, MemoryID: ref.ID, LineageVersion: ref.LineageVersion, ContentHash: ref.ContentHash, CreatedAt: value.UpdatedAt.UTC()}).Error; err != nil {
			return mapReminderWriteError(err)
		}
	}
	return nil
}
func insertReminderEvent(tx *gorm.DB, owner reminder.Owner, id, event string, old *string, next reminder.Status, actor, reason, execution, hash string, now time.Time) error {
	actor, reason = normalizeReminderAudit(actor, reason)
	return mapReminderWriteError(tx.Create(&reminderEventRow{ID: uuid.NewString(), TenantID: owner.TenantID, UserID: owner.UserID, ReminderID: id, EventType: event, OldStatus: old, NewStatus: string(next), Actor: actor, ReasonCode: reason, ExecutionID: execution, InputHash: hash, OccurredAt: now.UTC()}).Error)
}
func normalizeReminderAudit(actor, reason string) (string, string) {
	actor, reason = strings.TrimSpace(actor), strings.TrimSpace(reason)
	if actor == "" {
		actor = "system"
	}
	if reason == "" {
		reason = "unspecified"
	}
	if len(actor) > 191 {
		actor = actor[:191]
	}
	if len(reason) > 128 {
		reason = reason[:128]
	}
	return actor, reason
}
func deliveryToRow(value reminder.Delivery, occurrence string, now time.Time) *reminderDeliveryRow {
	return &reminderDeliveryRow{ID: value.ID, ReminderID: value.ReminderID, TenantID: value.Owner.TenantID, UserID: value.Owner.UserID, OccurrenceID: occurrence, DeliveryKey: value.DeliveryKey, Content: value.Content, Status: string(value.Status), Attempt: value.Attempt, AvailableAt: value.AvailableAt, LastErrorCode: value.LastErrorCode, CreatedAt: now}
}
func deliveryFromRow(row reminderDeliveryRow) reminder.Delivery {
	return reminder.Delivery{ID: row.ID, ReminderID: row.ReminderID, Owner: reminder.Owner{TenantID: row.TenantID, UserID: row.UserID}, Content: row.Content, DeliveryKey: row.DeliveryKey, Status: reminder.DeliveryStatus(row.Status), Attempt: row.Attempt, AvailableAt: row.AvailableAt, ClaimToken: stringValue(row.ClaimToken), LeaseUntil: row.LeaseUntil, LastErrorCode: row.LastErrorCode}
}
func mapReminderWriteError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return reminder.ErrStateConflict
	}
	return err
}
func escapeLike(value string) string {
	return strings.NewReplacer("\\", "\\\\", "%", "\\%", "_", "\\_").Replace(value)
}
