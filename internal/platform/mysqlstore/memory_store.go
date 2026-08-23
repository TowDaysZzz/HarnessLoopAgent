package mysqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/memory"
)

const memoryColumns = `id,tenant_id,user_id,layer,kind,scope_type,scope_id,namespace,slot_key,entity_type,entity_id,lineage_id,lineage_version,row_version,canonical_text,structured_value,content_hash,authority,confidence,salience,source_type,source_id,evidence_id,status,supersedes_id,superseded_by,expires_at,created_at,updated_at`

func scanMemory(row rowScanner) (memory.Record, error) {
	var value memory.Record
	var raw []byte
	var supersedes, supersededBy sql.NullString
	var expires sql.NullTime
	err := row.Scan(&value.ID, &value.Owner.TenantID, &value.Owner.UserID, &value.Layer, &value.Kind, &value.Scope.Type, &value.Scope.ID,
		&value.Namespace, &value.SlotKey, &value.Entity.Type, &value.Entity.ID, &value.LineageID, &value.LineageVersion, &value.RowVersion,
		&value.CanonicalText, &raw, &value.ContentHash, &value.Authority, &value.Confidence, &value.Salience, &value.Source.Type,
		&value.Source.ID, &value.Source.EvidenceID, &value.Status, &supersedes, &supersededBy, &expires, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return memory.Record{}, memory.ErrNotFound
	}
	if err != nil {
		return memory.Record{}, err
	}
	if err := json.Unmarshal(raw, &value.StructuredValue); err != nil {
		return memory.Record{}, fmt.Errorf("decode memory structured value: %w", err)
	}
	if supersedes.Valid {
		value.SupersedesID = supersedes.String
	}
	if supersededBy.Valid {
		value.SupersededBy = supersededBy.String
	}
	if expires.Valid {
		value.ExpiresAt = &expires.Time
	}
	return value, nil
}

func (s *Store) BatchGet(ctx context.Context, owner memory.Owner, ids []string) ([]memory.Record, error) {
	if !owner.Valid() || len(ids) == 0 {
		return []memory.Record{}, nil
	}
	if len(ids) > 200 {
		return nil, memory.ErrInvalidInput
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := []any{owner.TenantID, owner.UserID}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+memoryColumns+` FROM memory_records WHERE tenant_id=? AND user_id=? AND id IN (`+marks+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[string]memory.Record{}
	for rows.Next() {
		value, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		byID[value.ID] = value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]memory.Record, 0, len(byID))
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
		marks := strings.TrimSuffix(strings.Repeat("?,", len(q.Layers)), ",")
		where = append(where, "layer IN ("+marks+")")
		for _, value := range q.Layers {
			args = append(args, value)
		}
	}
	if len(q.Kinds) > 0 {
		marks := strings.TrimSuffix(strings.Repeat("?,", len(q.Kinds)), ",")
		where = append(where, "kind IN ("+marks+")")
		for _, value := range q.Kinds {
			args = append(args, value)
		}
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
	if len(q.Refs) > 0 {
		for _, ref := range q.Refs {
			matches = append(matches, "(id=? AND lineage_version=? AND content_hash=?)")
			args = append(args, ref.ID, ref.LineageVersion, ref.ContentHash)
		}
	}
	if len(matches) == 0 {
		return []memory.Record{}, nil
	}
	where = append(where, "("+strings.Join(matches, " OR ")+")")
	args = append(args, q.Limit)
	rows, err := s.db.QueryContext(ctx, `SELECT `+memoryColumns+` FROM memory_records WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC,id ASC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []memory.Record
	for rows.Next() {
		value, err := scanMemory(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, rows.Err()
}

func (s *Store) CommitMutation(ctx context.Context, m memory.Mutation) (memory.MutationResult, error) {
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
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return memory.MutationResult{}, err
	}
	defer tx.Rollback()
	if result, found, err := loadMemoryIdempotency(ctx, tx, m.Owner, m.IdempotencyKey, m.InputHash); err != nil {
		return memory.MutationResult{}, err
	} else if found {
		if err := tx.Commit(); err != nil {
			return memory.MutationResult{}, err
		}
		result.Replayed = true
		return result, nil
	}
	for _, target := range m.Targets {
		old, err := scanMemory(tx.QueryRowContext(ctx, `SELECT `+memoryColumns+` FROM memory_records WHERE tenant_id=? AND user_id=? AND id=? FOR UPDATE`, m.Owner.TenantID, m.Owner.UserID, target.ID))
		if err != nil {
			return memory.MutationResult{}, err
		}
		if old.RowVersion != target.ExpectedRowVersion || !old.Status.CanTransition(target.NewStatus) {
			return memory.MutationResult{}, memory.ErrStateConflict
		}
		result, err := tx.ExecContext(ctx, `UPDATE memory_records SET status=?,superseded_by=CASE WHEN ?='superseded' THEN ? ELSE superseded_by END,row_version=row_version+1,updated_at=? WHERE tenant_id=? AND user_id=? AND id=? AND row_version=? AND status=?`, target.NewStatus, target.NewStatus, value.ID, now, m.Owner.TenantID, m.Owner.UserID, target.ID, target.ExpectedRowVersion, old.Status)
		if err != nil {
			return memory.MutationResult{}, mapMemoryWriteError(err)
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return memory.MutationResult{}, memory.ErrStateConflict
		}
	}
	if err := insertMemory(ctx, tx, value, now); err != nil {
		return memory.MutationResult{}, mapMemoryWriteError(err)
	}
	for _, relation := range m.Relations {
		_, relationReason, validationErr := memory.NormalizeAuditFields("system", relation.ReasonCode)
		if validationErr != nil {
			return memory.MutationResult{}, validationErr
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_relations (id,tenant_id,user_id,from_memory_id,to_memory_id,relation_type,reason_code,created_at) VALUES (?,?,?,?,?,?,?,?)`, uuid.NewString(), m.Owner.TenantID, m.Owner.UserID, relation.FromID, relation.ToID, relation.Type, relationReason, now); err != nil {
			return memory.MutationResult{}, mapMemoryWriteError(err)
		}
	}
	if value.Status == memory.StatusActive {
		if err := s.insertProjection(ctx, tx, value, now); err != nil {
			return memory.MutationResult{}, err
		}
	}
	eventType := "created"
	oldStatus := any(nil)
	if len(m.Targets) > 0 {
		eventType = "superseded"
		oldStatus = memory.StatusActive
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_events (id,tenant_id,user_id,memory_id,event_type,old_status,new_status,actor,reason_code,execution_id,input_hash,result_memory_id,occurred_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, uuid.NewString(), m.Owner.TenantID, m.Owner.UserID, value.ID, eventType, oldStatus, value.Status, actor, reason, m.IdempotencyKey, m.InputHash, value.ID, now); err != nil {
		return memory.MutationResult{}, mapMemoryWriteError(err)
	}
	if err := tx.Commit(); err != nil {
		return memory.MutationResult{}, err
	}
	return memory.MutationResult{Memory: value, Relations: append([]memory.Relation(nil), m.Relations...)}, nil
}

func insertMemory(ctx context.Context, tx *sql.Tx, v memory.Record, now time.Time) error {
	raw, err := json.Marshal(v.StructuredValue)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO memory_records (`+memoryColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		v.ID, v.Owner.TenantID, v.Owner.UserID, v.Layer, v.Kind, v.Scope.Type, v.Scope.ID, v.Namespace, v.SlotKey, v.Entity.Type, v.Entity.ID,
		v.LineageID, v.LineageVersion, v.RowVersion, v.CanonicalText, raw, v.ContentHash, v.Authority, v.Confidence, v.Salience, v.Source.Type,
		v.Source.ID, v.Source.EvidenceID, v.Status, nullableString(v.SupersedesID), nullableString(v.SupersededBy), v.ExpiresAt, now, now)
	return err
}

func (s *Store) insertProjection(ctx context.Context, tx *sql.Tx, v memory.Record, now time.Time) error {
	if s.projectionVersion == "" {
		return fmt.Errorf("%w: projection version is not configured", memory.ErrInvalidInput)
	}
	id := projectionOutboxID(v.ID, v.ContentHash, s.projectionVersion)
	_, err := tx.ExecContext(ctx, `INSERT INTO memory_projection_outbox (id,tenant_id,user_id,memory_id,content_hash,model_version,status,available_at,created_at) VALUES (?,?,?,?,?,?,'pending',?,?)`, id, v.Owner.TenantID, v.Owner.UserID, v.ID, v.ContentHash, s.projectionVersion, now, now)
	return err
}

func projectionOutboxID(memoryID, contentHash, version string) string {
	sum := sha256.Sum256([]byte(memoryID + "\x00" + contentHash + "\x00" + version))
	return hex.EncodeToString(sum[:])
}

func loadMemoryIdempotency(ctx context.Context, tx *sql.Tx, owner memory.Owner, key, inputHash string) (memory.MutationResult, bool, error) {
	var storedHash, resultID string
	err := tx.QueryRowContext(ctx, `SELECT input_hash,result_memory_id FROM memory_events WHERE tenant_id=? AND user_id=? AND execution_id=?`, owner.TenantID, owner.UserID, key).Scan(&storedHash, &resultID)
	if errors.Is(err, sql.ErrNoRows) {
		return memory.MutationResult{}, false, nil
	}
	if err != nil {
		return memory.MutationResult{}, false, err
	}
	if storedHash != inputHash {
		return memory.MutationResult{}, false, memory.ErrIdempotencyConflict
	}
	value, err := scanMemory(tx.QueryRowContext(ctx, `SELECT `+memoryColumns+` FROM memory_records WHERE tenant_id=? AND user_id=? AND id=?`, owner.TenantID, owner.UserID, resultID))
	return memory.MutationResult{Memory: value}, true, err
}

func (s *Store) TransitionMemory(ctx context.Context, owner memory.Owner, id string, expected uint64, to memory.Status, actor, reason, key, inputHash string, now time.Time) (memory.MutationResult, error) {
	if !owner.Valid() || id == "" || key == "" || inputHash == "" {
		return memory.MutationResult{}, memory.ErrInvalidInput
	}
	normalizedActor, normalizedReason, err := memory.NormalizeAuditFields(actor, reason)
	if err != nil {
		return memory.MutationResult{}, err
	}
	actor, reason = normalizedActor, normalizedReason
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return memory.MutationResult{}, err
	}
	defer tx.Rollback()
	if result, found, err := loadMemoryIdempotency(ctx, tx, owner, key, inputHash); err != nil {
		return memory.MutationResult{}, err
	} else if found {
		result.Replayed = true
		if err := tx.Commit(); err != nil {
			return memory.MutationResult{}, err
		}
		return result, nil
	}
	value, err := scanMemory(tx.QueryRowContext(ctx, `SELECT `+memoryColumns+` FROM memory_records WHERE tenant_id=? AND user_id=? AND id=? FOR UPDATE`, owner.TenantID, owner.UserID, id))
	if err != nil {
		return memory.MutationResult{}, err
	}
	if value.RowVersion != expected || !value.Status.CanTransition(to) {
		return memory.MutationResult{}, memory.ErrStateConflict
	}
	old := value.Status
	result, err := tx.ExecContext(ctx, `UPDATE memory_records SET status=?,row_version=row_version+1,updated_at=? WHERE tenant_id=? AND user_id=? AND id=? AND status=? AND row_version=?`, to, now, owner.TenantID, owner.UserID, id, old, expected)
	if err != nil {
		return memory.MutationResult{}, mapMemoryWriteError(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return memory.MutationResult{}, memory.ErrStateConflict
	}
	value.Status, value.RowVersion, value.UpdatedAt = to, value.RowVersion+1, now
	if to == memory.StatusActive {
		if err := s.insertProjection(ctx, tx, value, now); err != nil {
			return memory.MutationResult{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_events (id,tenant_id,user_id,memory_id,event_type,old_status,new_status,actor,reason_code,execution_id,input_hash,result_memory_id,occurred_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, uuid.NewString(), owner.TenantID, owner.UserID, id, to, old, to, actor, reason, key, inputHash, id, now); err != nil {
		return memory.MutationResult{}, mapMemoryWriteError(err)
	}
	if err := tx.Commit(); err != nil {
		return memory.MutationResult{}, err
	}
	return memory.MutationResult{Memory: value}, nil
}

func (s *Store) ActivateCandidateSuperseding(ctx context.Context, activation memory.CandidateActivation) (memory.MutationResult, error) {
	if !activation.Owner.Valid() || activation.CandidateID == "" || activation.SupersededID == "" || activation.CandidateID == activation.SupersededID || activation.CandidateVersion == 0 || activation.TargetVersion == 0 || activation.IdempotencyKey == "" || activation.InputHash == "" {
		return memory.MutationResult{}, memory.ErrInvalidInput
	}
	actor, reason, err := memory.NormalizeAuditFields(activation.Actor, activation.ReasonCode)
	if err != nil {
		return memory.MutationResult{}, err
	}
	now := activation.OccurredAt.UTC()
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return memory.MutationResult{}, err
	}
	defer tx.Rollback()
	if result, found, loadErr := loadMemoryIdempotency(ctx, tx, activation.Owner, activation.IdempotencyKey, activation.InputHash); loadErr != nil {
		return memory.MutationResult{}, loadErr
	} else if found {
		result.Replayed = true
		if err := tx.Commit(); err != nil {
			return memory.MutationResult{}, err
		}
		return result, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT `+memoryColumns+` FROM memory_records WHERE tenant_id=? AND user_id=? AND id IN (?,?) ORDER BY id FOR UPDATE`, activation.Owner.TenantID, activation.Owner.UserID, activation.CandidateID, activation.SupersededID)
	if err != nil {
		return memory.MutationResult{}, err
	}
	locked := map[string]memory.Record{}
	for rows.Next() {
		value, scanErr := scanMemory(rows)
		if scanErr != nil {
			rows.Close()
			return memory.MutationResult{}, scanErr
		}
		locked[value.ID] = value
	}
	if err := rows.Close(); err != nil {
		return memory.MutationResult{}, err
	}
	candidate, candidateOK := locked[activation.CandidateID]
	target, targetOK := locked[activation.SupersededID]
	if !candidateOK || !targetOK {
		return memory.MutationResult{}, memory.ErrNotFound
	}
	if candidate.RowVersion != activation.CandidateVersion || target.RowVersion != activation.TargetVersion || candidate.Status != memory.StatusCandidate || target.Status != memory.StatusActive || !candidate.Status.CanTransition(memory.StatusActive) || !target.Status.CanTransition(memory.StatusSuperseded) || candidate.SupersedesID != target.ID || candidate.LineageID != target.LineageID || candidate.LineageVersion <= target.LineageVersion {
		return memory.MutationResult{}, memory.ErrStateConflict
	}
	updated, err := tx.ExecContext(ctx, `UPDATE memory_records SET status='superseded',superseded_by=?,row_version=row_version+1,updated_at=? WHERE tenant_id=? AND user_id=? AND id=? AND status='active' AND row_version=?`, candidate.ID, now, activation.Owner.TenantID, activation.Owner.UserID, target.ID, activation.TargetVersion)
	if err != nil {
		return memory.MutationResult{}, mapMemoryWriteError(err)
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return memory.MutationResult{}, memory.ErrStateConflict
	}
	updated, err = tx.ExecContext(ctx, `UPDATE memory_records SET status='active',row_version=row_version+1,updated_at=? WHERE tenant_id=? AND user_id=? AND id=? AND status='candidate' AND row_version=?`, now, activation.Owner.TenantID, activation.Owner.UserID, candidate.ID, activation.CandidateVersion)
	if err != nil {
		return memory.MutationResult{}, mapMemoryWriteError(err)
	}
	if count, _ := updated.RowsAffected(); count != 1 {
		return memory.MutationResult{}, memory.ErrStateConflict
	}
	candidate.Status, candidate.RowVersion, candidate.UpdatedAt = memory.StatusActive, candidate.RowVersion+1, now
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_relations (id,tenant_id,user_id,from_memory_id,to_memory_id,relation_type,reason_code,created_at) VALUES (?,?,?,?,?,?,?,?)`, uuid.NewString(), activation.Owner.TenantID, activation.Owner.UserID, candidate.ID, target.ID, memory.RelationSupersedes, reason, now); err != nil {
		return memory.MutationResult{}, mapMemoryWriteError(err)
	}
	if err := s.insertProjection(ctx, tx, candidate, now); err != nil {
		return memory.MutationResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_events (id,tenant_id,user_id,memory_id,event_type,old_status,new_status,actor,reason_code,execution_id,input_hash,result_memory_id,occurred_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, uuid.NewString(), activation.Owner.TenantID, activation.Owner.UserID, candidate.ID, "activated", memory.StatusCandidate, memory.StatusActive, actor, reason, activation.IdempotencyKey, activation.InputHash, candidate.ID, now); err != nil {
		return memory.MutationResult{}, mapMemoryWriteError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO memory_events (id,tenant_id,user_id,memory_id,event_type,old_status,new_status,actor,reason_code,execution_id,input_hash,result_memory_id,occurred_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, uuid.NewString(), activation.Owner.TenantID, activation.Owner.UserID, target.ID, "superseded", memory.StatusActive, memory.StatusSuperseded, actor, reason, activation.IdempotencyKey+":target", activation.InputHash, candidate.ID, now); err != nil {
		return memory.MutationResult{}, mapMemoryWriteError(err)
	}
	if err := tx.Commit(); err != nil {
		return memory.MutationResult{}, err
	}
	return memory.MutationResult{Memory: candidate, Relations: []memory.Relation{{FromID: candidate.ID, ToID: target.ID, Type: memory.RelationSupersedes, ReasonCode: reason}}}, nil
}

func (s *Store) Expire(ctx context.Context, owner memory.Owner, now time.Time, limit int) (int, error) {
	if !owner.Valid() || limit <= 0 || limit > 1000 {
		return 0, memory.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT `+memoryColumns+` FROM memory_records WHERE tenant_id=? AND user_id=? AND status IN ('candidate','active') AND expires_at IS NOT NULL AND expires_at<=? ORDER BY expires_at LIMIT ? FOR UPDATE SKIP LOCKED`, owner.TenantID, owner.UserID, now, limit)
	if err != nil {
		return 0, err
	}
	var values []memory.Record
	for rows.Next() {
		value, err := scanMemory(rows)
		if err != nil {
			rows.Close()
			return 0, err
		}
		values = append(values, value)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	for _, value := range values {
		result, err := tx.ExecContext(ctx, `UPDATE memory_records SET status='expired',row_version=row_version+1,updated_at=? WHERE tenant_id=? AND user_id=? AND id=? AND row_version=? AND status=?`, now, owner.TenantID, owner.UserID, value.ID, value.RowVersion, value.Status)
		if err != nil {
			return 0, err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return 0, memory.ErrStateConflict
		}
		executionID := fmt.Sprintf("expiry:%s:%d", value.ID, value.RowVersion)
		if _, err := tx.ExecContext(ctx, `INSERT INTO memory_events (id,tenant_id,user_id,memory_id,event_type,old_status,new_status,actor,reason_code,execution_id,input_hash,result_memory_id,occurred_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, uuid.NewString(), owner.TenantID, owner.UserID, value.ID, "expired", value.Status, memory.StatusExpired, "system", "ttl_expired", executionID, value.ContentHash, value.ID, now); err != nil {
			return 0, mapMemoryWriteError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(values), nil
}

func (s *Store) ClaimProjections(ctx context.Context, limit int, now time.Time) ([]memory.Projection, error) {
	if limit <= 0 || limit > 200 {
		return nil, memory.ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT id,tenant_id,user_id,memory_id,content_hash,model_version,status,attempt,available_at,claimed_at,processed_at,last_error_code FROM memory_projection_outbox WHERE status IN ('pending','failed') AND available_at<=? ORDER BY created_at LIMIT ? FOR UPDATE SKIP LOCKED`, now, limit)
	if err != nil {
		return nil, err
	}
	var out []memory.Projection
	for rows.Next() {
		var p memory.Projection
		var claimed, processed sql.NullTime
		if err := rows.Scan(&p.ID, &p.Owner.TenantID, &p.Owner.UserID, &p.MemoryID, &p.ContentHash, &p.ModelVersion, &p.Status, &p.Attempt, &p.AvailableAt, &claimed, &processed, &p.LastErrorCode); err != nil {
			rows.Close()
			return nil, err
		}
		if claimed.Valid {
			p.ClaimedAt = &claimed.Time
		}
		if processed.Valid {
			p.ProcessedAt = &processed.Time
		}
		p.Attempt++
		p.Status = memory.ProjectionProcessing
		p.ClaimedAt = &now
		out = append(out, p)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, p := range out {
		if _, err := tx.ExecContext(ctx, `UPDATE memory_projection_outbox SET status='processing',attempt=?,claimed_at=? WHERE id=?`, p.Attempt, now, p.ID); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) PendingProjectionCount(ctx context.Context) (int64, error) {
	var count int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_projection_outbox WHERE status IN ('pending','failed')`).Scan(&count)
	return count, err
}

func (s *Store) CompleteProjection(ctx context.Context, owner memory.Owner, id string, now time.Time) error {
	result, err := s.db.ExecContext(ctx, `UPDATE memory_projection_outbox SET status='completed',processed_at=?,last_error_code='' WHERE id=? AND tenant_id=? AND user_id=? AND status='processing'`, now, id, owner.TenantID, owner.UserID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return memory.ErrNotFound
	}
	return nil
}

func (s *Store) FailProjection(ctx context.Context, owner memory.Owner, id, code string, next time.Time, permanent bool) error {
	_, normalizedCode, validationErr := memory.NormalizeAuditFields("system", code)
	if validationErr != nil {
		return validationErr
	}
	code = normalizedCode
	status := memory.ProjectionFailed
	if permanent {
		status = memory.ProjectionPermanentFailed
	}
	result, err := s.db.ExecContext(ctx, `UPDATE memory_projection_outbox SET status=?,available_at=?,last_error_code=? WHERE id=? AND tenant_id=? AND user_id=? AND status='processing'`, status, next, code, id, owner.TenantID, owner.UserID)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return memory.ErrNotFound
	}
	return nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
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
	if strings.Contains(err.Error(), "Duplicate entry") {
		return memory.ErrStateConflict
	}
	return err
}

var _ memory.Repository = (*Store)(nil)
