package mysqlstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
	mysqlDriver "github.com/go-sql-driver/mysql"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) BatchGet(ctx context.Context, owner memory.Owner, ids []string) ([]memory.Record, error) {
	if !owner.Valid() || len(ids) == 0 {
		return []memory.Record{}, nil
	}
	if len(ids) > 200 {
		return nil, memory.ErrInvalidInput
	}
	var rows []memoryRecordRow
	if err := s.db.WithContext(ctx).Where("tenant_id=? AND user_id=? AND id IN ?", owner.TenantID, owner.UserID, ids).Find(&rows).Error; err != nil {
		return nil, err
	}
	byID := make(map[string]memory.Record, len(rows))
	for _, row := range rows {
		value, err := memoryFromRow(row)
		if err != nil {
			return nil, err
		}
		byID[value.ID] = value
	}
	out := make([]memory.Record, 0, len(rows))
	for _, id := range ids {
		if value, ok := byID[id]; ok {
			out = append(out, value)
		}
	}
	return out, nil
}

func (s *Store) FindExact(ctx context.Context, q memory.ExactQuery) ([]memory.Record, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}
	if !q.HasSelector() {
		return []memory.Record{}, nil
	}
	where := []string{"tenant_id=?", "user_id=?", "scope_type=?", "scope_id=?"}
	args := []any{q.Owner.TenantID, q.Owner.UserID, q.Scope.Type, q.Scope.ID}
	if q.ActiveAt != nil {
		where = append(where, "status='active'", "(expires_at IS NULL OR expires_at>?)")
		args = append(args, q.ActiveAt.UTC())
	}
	if len(q.Layers) > 0 {
		where = append(where, "layer IN ?")
		args = append(args, q.Layers)
	}
	if len(q.Kinds) > 0 {
		where = append(where, "kind IN ?")
		args = append(args, q.Kinds)
	}
	var matches []string
	if q.ScopeOnly {
		matches = append(matches, "1=1")
	}
	if q.ContentHash != "" {
		matches = append(matches, "content_hash=?")
		args = append(args, q.ContentHash)
	}
	if q.Namespace != "" && q.SlotKey != "" {
		matches = append(matches, "(namespace=? AND slot_key=?)")
		args = append(args, q.Namespace, q.SlotKey)
	}
	if !q.Entity.Empty() {
		matches = append(matches, "(entity_type=? AND entity_id=?)")
		args = append(args, q.Entity.Type, q.Entity.ID)
	}
	for _, hash := range q.ContentHashes {
		matches = append(matches, "content_hash=?")
		args = append(args, hash)
	}
	for _, slot := range q.Slots {
		matches = append(matches, "(namespace=? AND slot_key=?)")
		args = append(args, slot.Namespace, slot.SlotKey)
	}
	for _, entity := range q.Entities {
		matches = append(matches, "(entity_type=? AND entity_id=?)")
		args = append(args, entity.Type, entity.ID)
	}
	for _, ref := range q.Refs {
		matches = append(matches, "(id=? AND lineage_version=? AND content_hash=?)")
		args = append(args, ref.ID, ref.LineageVersion, ref.ContentHash)
	}
	if len(matches) == 0 {
		return []memory.Record{}, nil
	}
	where = append(where, "("+strings.Join(matches, " OR ")+")")
	query := s.db.WithContext(ctx).Model(&memoryRecordRow{}).Where(strings.Join(where, " AND "), args...).Order("created_at DESC,id ASC").Limit(q.Limit)
	var rows []memoryRecordRow
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]memory.Record, 0, len(rows))
	for _, row := range rows {
		value, err := memoryFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}

func (s *Store) CommitMutation(ctx context.Context, m memory.Mutation) (result memory.MutationResult, err error) {
	if m.NewMemory == nil || !m.Owner.Valid() || m.IdempotencyKey == "" || m.InputHash == "" {
		return memory.MutationResult{}, memory.ErrInvalidInput
	}
	now := m.OccurredAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	value := *m.NewMemory
	if err := value.Validate(now); err != nil {
		return memory.MutationResult{}, err
	}
	if value.Owner != m.Owner {
		return memory.MutationResult{}, memory.ErrNotFound
	}
	actor, reason, err := memory.NormalizeAuditFields(m.Actor, m.ReasonCode)
	if err != nil {
		return memory.MutationResult{}, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if replay, found, loadErr := loadMemoryIdempotency(tx, m.Owner, m.IdempotencyKey, m.InputHash); loadErr != nil {
			return loadErr
		} else if found {
			replay.Replayed = true
			result = replay
			return nil
		}
		for _, target := range m.Targets {
			old, loadErr := loadMemoryLocked(tx, m.Owner, target.ID)
			if loadErr != nil {
				return loadErr
			}
			if old.RowVersion != target.ExpectedRowVersion || !old.Status.CanTransition(target.NewStatus) {
				return memory.ErrStateConflict
			}
			updates := map[string]any{"status": target.NewStatus, "row_version": gorm.Expr("row_version+1"), "updated_at": now}
			if target.NewStatus == memory.StatusSuperseded {
				updates["superseded_by"] = value.ID
			}
			write := tx.Model(&memoryRecordRow{}).Where("tenant_id=? AND user_id=? AND id=? AND row_version=? AND status=?", m.Owner.TenantID, m.Owner.UserID, target.ID, target.ExpectedRowVersion, old.Status).Updates(updates)
			if write.Error != nil {
				return mapMemoryWriteError(write.Error)
			}
			if write.RowsAffected != 1 {
				return memory.ErrStateConflict
			}
		}
		if err := insertMemory(tx, value, now); err != nil {
			return mapMemoryWriteError(err)
		}
		for _, relation := range m.Relations {
			_, relationReason, validationErr := memory.NormalizeAuditFields("system", relation.ReasonCode)
			if validationErr != nil {
				return validationErr
			}
			if err := tx.Create(&memoryRelationRow{ID: uuid.NewString(), TenantID: m.Owner.TenantID, UserID: m.Owner.UserID, FromMemoryID: relation.FromID, ToMemoryID: relation.ToID, RelationType: string(relation.Type), ReasonCode: relationReason, CreatedAt: now}).Error; err != nil {
				return mapMemoryWriteError(err)
			}
		}
		if value.Status == memory.StatusActive {
			if err := s.insertProjection(tx, value, now); err != nil {
				return err
			}
		}
		eventType := "created"
		var oldStatus *string
		if len(m.Targets) > 0 {
			eventType = "superseded"
			oldStatus = stringPtr(string(memory.StatusActive))
		}
		if err := tx.Create(&memoryEventRow{ID: uuid.NewString(), TenantID: m.Owner.TenantID, UserID: m.Owner.UserID, MemoryID: value.ID, EventType: eventType, OldStatus: oldStatus, NewStatus: string(value.Status), Actor: actor, ReasonCode: reason, ExecutionID: m.IdempotencyKey, InputHash: m.InputHash, ResultMemoryID: value.ID, OccurredAt: now}).Error; err != nil {
			return mapMemoryWriteError(err)
		}
		result = memory.MutationResult{Memory: value, Relations: append([]memory.Relation(nil), m.Relations...)}
		return nil
	})
	return
}

func insertMemory(tx *gorm.DB, v memory.Record, now time.Time) error {
	row, err := memoryToRow(v, now)
	if err != nil {
		return err
	}
	return tx.Create(&row).Error
}
func (s *Store) insertProjection(tx *gorm.DB, v memory.Record, now time.Time) error {
	if s.projectionVersion == "" {
		return fmt.Errorf("%w: projection version is not configured", memory.ErrInvalidInput)
	}
	return tx.Create(&memoryProjectionRow{ID: projectionOutboxID(v.ID, v.ContentHash, s.projectionVersion), TenantID: v.Owner.TenantID, UserID: v.Owner.UserID, MemoryID: v.ID, ContentHash: v.ContentHash, ModelVersion: s.projectionVersion, Status: string(memory.ProjectionPending), AvailableAt: now, CreatedAt: now}).Error
}
func projectionOutboxID(memoryID, contentHash, version string) string {
	sum := sha256.Sum256([]byte(memoryID + "\x00" + contentHash + "\x00" + version))
	return hex.EncodeToString(sum[:])
}

func loadMemoryIdempotency(tx *gorm.DB, owner memory.Owner, key, inputHash string) (memory.MutationResult, bool, error) {
	var event memoryEventRow
	err := tx.Where("tenant_id=? AND user_id=? AND execution_id=?", owner.TenantID, owner.UserID, key).First(&event).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return memory.MutationResult{}, false, nil
	}
	if err != nil {
		return memory.MutationResult{}, false, err
	}
	if event.InputHash != inputHash {
		return memory.MutationResult{}, false, memory.ErrIdempotencyConflict
	}
	var row memoryRecordRow
	err = tx.Where("tenant_id=? AND user_id=? AND id=?", owner.TenantID, owner.UserID, event.ResultMemoryID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return memory.MutationResult{}, false, memory.ErrNotFound
	}
	if err != nil {
		return memory.MutationResult{}, false, err
	}
	value, err := memoryFromRow(row)
	return memory.MutationResult{Memory: value}, true, err
}

func (s *Store) TransitionMemory(ctx context.Context, owner memory.Owner, id string, expected uint64, to memory.Status, actor, reason, key, inputHash string, now time.Time) (result memory.MutationResult, err error) {
	if !owner.Valid() || id == "" || key == "" || inputHash == "" {
		return memory.MutationResult{}, memory.ErrInvalidInput
	}
	actor, reason, err = normalizeAudit(actor, reason)
	if err != nil {
		return memory.MutationResult{}, err
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if replay, found, loadErr := loadMemoryIdempotency(tx, owner, key, inputHash); loadErr != nil {
			return loadErr
		} else if found {
			replay.Replayed = true
			result = replay
			return nil
		}
		value, loadErr := loadMemoryLocked(tx, owner, id)
		if loadErr != nil {
			return loadErr
		}
		if value.RowVersion != expected || !value.Status.CanTransition(to) {
			return memory.ErrStateConflict
		}
		old := value.Status
		write := tx.Model(&memoryRecordRow{}).Where("tenant_id=? AND user_id=? AND id=? AND status=? AND row_version=?", owner.TenantID, owner.UserID, id, old, expected).Updates(map[string]any{"status": to, "row_version": gorm.Expr("row_version+1"), "updated_at": now})
		if write.Error != nil {
			return mapMemoryWriteError(write.Error)
		}
		if write.RowsAffected != 1 {
			return memory.ErrStateConflict
		}
		value.Status, value.RowVersion, value.UpdatedAt = to, value.RowVersion+1, now
		if to == memory.StatusActive {
			if err := s.insertProjection(tx, value, now); err != nil {
				return err
			}
		}
		if err := tx.Create(&memoryEventRow{ID: uuid.NewString(), TenantID: owner.TenantID, UserID: owner.UserID, MemoryID: id, EventType: string(to), OldStatus: stringPtr(string(old)), NewStatus: string(to), Actor: actor, ReasonCode: reason, ExecutionID: key, InputHash: inputHash, ResultMemoryID: id, OccurredAt: now}).Error; err != nil {
			return mapMemoryWriteError(err)
		}
		result = memory.MutationResult{Memory: value}
		return nil
	})
	return
}

func (s *Store) ActivateCandidateSuperseding(ctx context.Context, a memory.CandidateActivation) (result memory.MutationResult, err error) {
	if !a.Owner.Valid() || a.CandidateID == "" || a.SupersededID == "" || a.CandidateID == a.SupersededID || a.CandidateVersion == 0 || a.TargetVersion == 0 || a.IdempotencyKey == "" || a.InputHash == "" {
		return memory.MutationResult{}, memory.ErrInvalidInput
	}
	actor, reason, err := memory.NormalizeAuditFields(a.Actor, a.ReasonCode)
	if err != nil {
		return memory.MutationResult{}, err
	}
	now := a.OccurredAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if replay, found, loadErr := loadMemoryIdempotency(tx, a.Owner, a.IdempotencyKey, a.InputHash); loadErr != nil {
			return loadErr
		} else if found {
			replay.Replayed = true
			result = replay
			return nil
		}
		var rows []memoryRecordRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND user_id=? AND id IN ?", a.Owner.TenantID, a.Owner.UserID, []string{a.CandidateID, a.SupersededID}).Order("id").Find(&rows).Error; err != nil {
			return err
		}
		locked := map[string]memory.Record{}
		for _, row := range rows {
			value, err := memoryFromRow(row)
			if err != nil {
				return err
			}
			locked[value.ID] = value
		}
		candidate, candidateOK := locked[a.CandidateID]
		target, targetOK := locked[a.SupersededID]
		if !candidateOK || !targetOK {
			return memory.ErrNotFound
		}
		if candidate.RowVersion != a.CandidateVersion || target.RowVersion != a.TargetVersion || candidate.Status != memory.StatusCandidate || target.Status != memory.StatusActive || !candidate.Status.CanTransition(memory.StatusActive) || !target.Status.CanTransition(memory.StatusSuperseded) || candidate.SupersedesID != target.ID || candidate.LineageID != target.LineageID || candidate.LineageVersion <= target.LineageVersion {
			return memory.ErrStateConflict
		}
		write := tx.Model(&memoryRecordRow{}).Where("tenant_id=? AND user_id=? AND id=? AND status='active' AND row_version=?", a.Owner.TenantID, a.Owner.UserID, target.ID, a.TargetVersion).Updates(map[string]any{"status": "superseded", "superseded_by": candidate.ID, "row_version": gorm.Expr("row_version+1"), "updated_at": now})
		if write.Error != nil {
			return mapMemoryWriteError(write.Error)
		}
		if write.RowsAffected != 1 {
			return memory.ErrStateConflict
		}
		write = tx.Model(&memoryRecordRow{}).Where("tenant_id=? AND user_id=? AND id=? AND status='candidate' AND row_version=?", a.Owner.TenantID, a.Owner.UserID, candidate.ID, a.CandidateVersion).Updates(map[string]any{"status": "active", "row_version": gorm.Expr("row_version+1"), "updated_at": now})
		if write.Error != nil {
			return mapMemoryWriteError(write.Error)
		}
		if write.RowsAffected != 1 {
			return memory.ErrStateConflict
		}
		candidate.Status, candidate.RowVersion, candidate.UpdatedAt = memory.StatusActive, candidate.RowVersion+1, now
		if err := tx.Create(&memoryRelationRow{ID: uuid.NewString(), TenantID: a.Owner.TenantID, UserID: a.Owner.UserID, FromMemoryID: candidate.ID, ToMemoryID: target.ID, RelationType: string(memory.RelationSupersedes), ReasonCode: reason, CreatedAt: now}).Error; err != nil {
			return mapMemoryWriteError(err)
		}
		if err := s.insertProjection(tx, candidate, now); err != nil {
			return err
		}
		events := []memoryEventRow{{ID: uuid.NewString(), TenantID: a.Owner.TenantID, UserID: a.Owner.UserID, MemoryID: candidate.ID, EventType: "activated", OldStatus: stringPtr(string(memory.StatusCandidate)), NewStatus: string(memory.StatusActive), Actor: actor, ReasonCode: reason, ExecutionID: a.IdempotencyKey, InputHash: a.InputHash, ResultMemoryID: candidate.ID, OccurredAt: now}, {ID: uuid.NewString(), TenantID: a.Owner.TenantID, UserID: a.Owner.UserID, MemoryID: target.ID, EventType: "superseded", OldStatus: stringPtr(string(memory.StatusActive)), NewStatus: string(memory.StatusSuperseded), Actor: actor, ReasonCode: reason, ExecutionID: a.IdempotencyKey + ":target", InputHash: a.InputHash, ResultMemoryID: candidate.ID, OccurredAt: now}}
		if err := tx.Create(&events).Error; err != nil {
			return mapMemoryWriteError(err)
		}
		relation := memory.Relation{FromID: candidate.ID, ToID: target.ID, Type: memory.RelationSupersedes, ReasonCode: reason}
		result = memory.MutationResult{Memory: candidate, Relations: []memory.Relation{relation}}
		return nil
	})
	return
}

func (s *Store) Expire(ctx context.Context, owner memory.Owner, now time.Time, limit int) (count int, err error) {
	if !owner.Valid() || limit <= 0 || limit > 1000 {
		return 0, memory.ErrInvalidInput
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []memoryRecordRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("tenant_id=? AND user_id=? AND status IN ? AND expires_at IS NOT NULL AND expires_at<=?", owner.TenantID, owner.UserID, []string{"candidate", "active"}, now).Order("expires_at").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			value, err := memoryFromRow(row)
			if err != nil {
				return err
			}
			write := tx.Model(&memoryRecordRow{}).Where("tenant_id=? AND user_id=? AND id=? AND row_version=? AND status=?", owner.TenantID, owner.UserID, value.ID, value.RowVersion, value.Status).Updates(map[string]any{"status": "expired", "row_version": gorm.Expr("row_version+1"), "updated_at": now})
			if write.Error != nil {
				return write.Error
			}
			if write.RowsAffected != 1 {
				return memory.ErrStateConflict
			}
			event := memoryEventRow{ID: uuid.NewString(), TenantID: owner.TenantID, UserID: owner.UserID, MemoryID: value.ID, EventType: "expired", OldStatus: stringPtr(string(value.Status)), NewStatus: string(memory.StatusExpired), Actor: "system", ReasonCode: "ttl_expired", ExecutionID: fmt.Sprintf("expiry:%s:%d", value.ID, value.RowVersion), InputHash: value.ContentHash, ResultMemoryID: value.ID, OccurredAt: now}
			if err := tx.Create(&event).Error; err != nil {
				return mapMemoryWriteError(err)
			}
		}
		count = len(rows)
		return nil
	})
	return
}

func (s *Store) ClaimProjections(ctx context.Context, limit int, now time.Time) (out []memory.Projection, err error) {
	if limit <= 0 || limit > 200 {
		return nil, memory.ErrInvalidInput
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rows []memoryProjectionRow
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).Where("status IN ? AND available_at<=?", []string{"pending", "failed"}, now).Order("created_at").Limit(limit).Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			attempt := row.Attempt + 1
			write := tx.Model(&memoryProjectionRow{}).Where("id=? AND status IN ?", row.ID, []string{"pending", "failed"}).Updates(map[string]any{"status": "processing", "attempt": attempt, "claimed_at": now})
			if write.Error != nil {
				return write.Error
			}
			if write.RowsAffected != 1 {
				return memory.ErrStateConflict
			}
			out = append(out, memory.Projection{ID: row.ID, Owner: memory.Owner{TenantID: row.TenantID, UserID: row.UserID}, MemoryID: row.MemoryID, ContentHash: row.ContentHash, ModelVersion: row.ModelVersion, Status: memory.ProjectionProcessing, Attempt: attempt, AvailableAt: row.AvailableAt, ClaimedAt: &now, ProcessedAt: row.ProcessedAt, LastErrorCode: row.LastErrorCode})
		}
		return nil
	})
	return
}
func (s *Store) PendingProjectionCount(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.WithContext(ctx).Model(&memoryProjectionRow{}).Where("status IN ?", []string{"pending", "failed"}).Count(&count).Error
	return count, err
}
func (s *Store) CompleteProjection(ctx context.Context, owner memory.Owner, id string, now time.Time) error {
	result := s.db.WithContext(ctx).Model(&memoryProjectionRow{}).Where("id=? AND tenant_id=? AND user_id=? AND status='processing'", id, owner.TenantID, owner.UserID).Updates(map[string]any{"status": "completed", "processed_at": now, "last_error_code": ""})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return memory.ErrNotFound
	}
	return nil
}
func (s *Store) FailProjection(ctx context.Context, owner memory.Owner, id, code string, next time.Time, permanent bool) error {
	_, code, err := memory.NormalizeAuditFields("system", code)
	if err != nil {
		return err
	}
	status := memory.ProjectionFailed
	if permanent {
		status = memory.ProjectionPermanentFailed
	}
	result := s.db.WithContext(ctx).Model(&memoryProjectionRow{}).Where("id=? AND tenant_id=? AND user_id=? AND status='processing'", id, owner.TenantID, owner.UserID).Updates(map[string]any{"status": status, "available_at": next, "last_error_code": code})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return memory.ErrNotFound
	}
	return nil
}

func memoryToRow(v memory.Record, now time.Time) (memoryRecordRow, error) {
	raw, err := json.Marshal(v.StructuredValue)
	if err != nil {
		return memoryRecordRow{}, err
	}
	return memoryRecordRow{ID: v.ID, TenantID: v.Owner.TenantID, UserID: v.Owner.UserID, Layer: string(v.Layer), Kind: string(v.Kind), ScopeType: string(v.Scope.Type), ScopeID: v.Scope.ID, Namespace: v.Namespace, SlotKey: v.SlotKey, EntityType: v.Entity.Type, EntityID: v.Entity.ID, LineageID: v.LineageID, LineageVersion: v.LineageVersion, RowVersion: v.RowVersion, CanonicalText: v.CanonicalText, StructuredValue: raw, ContentHash: v.ContentHash, Authority: string(v.Authority), Confidence: v.Confidence, Salience: v.Salience, SourceType: v.Source.Type, SourceID: v.Source.ID, EvidenceID: v.Source.EvidenceID, Status: string(v.Status), SupersedesID: stringPtr(v.SupersedesID), SupersededBy: stringPtr(v.SupersededBy), ExpiresAt: v.ExpiresAt, CreatedAt: now, UpdatedAt: now}, nil
}
func memoryFromRow(row memoryRecordRow) (memory.Record, error) {
	var structured memory.StructuredValue
	if err := json.Unmarshal(row.StructuredValue, &structured); err != nil {
		return memory.Record{}, fmt.Errorf("decode memory structured value: %w", err)
	}
	return memory.Record{ID: row.ID, Owner: memory.Owner{TenantID: row.TenantID, UserID: row.UserID}, Layer: memory.Layer(row.Layer), Kind: memory.Kind(row.Kind), Scope: memory.Scope{Type: memory.ScopeType(row.ScopeType), ID: row.ScopeID}, Namespace: row.Namespace, SlotKey: row.SlotKey, Entity: memory.EntityRef{Type: row.EntityType, ID: row.EntityID}, LineageID: row.LineageID, LineageVersion: row.LineageVersion, RowVersion: row.RowVersion, CanonicalText: row.CanonicalText, StructuredValue: structured, ContentHash: row.ContentHash, Authority: memory.Authority(row.Authority), Confidence: row.Confidence, Salience: row.Salience, Source: memory.SourceRef{Type: row.SourceType, ID: row.SourceID, EvidenceID: row.EvidenceID}, Status: memory.Status(row.Status), SupersedesID: stringValue(row.SupersedesID), SupersededBy: stringValue(row.SupersededBy), ExpiresAt: row.ExpiresAt, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}
func loadMemoryLocked(tx *gorm.DB, owner memory.Owner, id string) (memory.Record, error) {
	var row memoryRecordRow
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND user_id=? AND id=?", owner.TenantID, owner.UserID, id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return memory.Record{}, memory.ErrNotFound
	}
	if err != nil {
		return memory.Record{}, err
	}
	return memoryFromRow(row)
}
func normalizeAudit(actor, reason string) (string, string, error) {
	return memory.NormalizeAuditFields(actor, reason)
}
func safeReason(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 128 {
		value = value[:128]
	}
	return value
}
func mapMemoryWriteError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrDuplicatedKey) {
		return memory.ErrStateConflict
	}
	var mysqlErr *mysqlDriver.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
		return memory.ErrStateConflict
	}
	return err
}
