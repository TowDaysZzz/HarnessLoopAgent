package mysqlstore

import (
	"context"
	"errors"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/dailyreview"
	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *Store) Lookup(ctx context.Context, owner skill.Owner, logical, source string, now time.Time) (dailyreview.CacheRecord, error) {
	var row dailyReviewCacheRow
	err := s.db.WithContext(ctx).Where("tenant_id=? AND user_id=? AND logical_key=? AND source_fingerprint=? AND status='ready' AND valid_until>?", owner.TenantID, owner.UserID, logical, source, now).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return dailyreview.CacheRecord{}, dailyreview.ErrCacheNotFound
	}
	if err != nil {
		return dailyreview.CacheRecord{}, err
	}
	return cacheFromRow(row), nil
}

func (s *Store) Claim(ctx context.Context, owner skill.Owner, logical, source string, now time.Time, lease time.Duration) (result dailyreview.ClaimResult, err error) {
	if !owner.Valid() || logical == "" || source == "" || lease <= 0 {
		return result, skill.ErrInvalidInvocation
	}
	token := uuid.NewString()[:32]
	until := now.Add(lease)
	candidate := dailyReviewCacheRow{ID: uuid.NewString(), TenantID: owner.TenantID, UserID: owner.UserID, LogicalKey: logical, SourceFingerprint: source, Status: string(dailyreview.CacheGenerating), ClaimToken: token, LeaseUntil: &until, CreatedAt: now, UpdatedAt: now}
	insert := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(&candidate)
	if insert.Error != nil {
		return result, insert.Error
	}
	if insert.RowsAffected == 1 {
		return dailyreview.ClaimResult{Record: cacheFromRow(candidate), Generator: true}, nil
	}
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row dailyReviewCacheRow
		load := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("tenant_id=? AND user_id=? AND logical_key=? AND source_fingerprint=?", owner.TenantID, owner.UserID, logical, source).First(&row).Error
		if load != nil {
			return load
		}
		record := cacheFromRow(row)
		if row.Status == string(dailyreview.CacheReady) && row.ValidUntil != nil && now.Before(*row.ValidUntil) {
			result.Record = record
			return nil
		}
		if row.Status == string(dailyreview.CacheGenerating) && row.LeaseUntil != nil && now.Before(*row.LeaseUntil) {
			result.Record = record
			return nil
		}
		token := uuid.NewString()[:32]
		until := now.Add(lease)
		write := tx.Model(&dailyReviewCacheRow{}).Where("id=? AND tenant_id=? AND user_id=?", row.ID, owner.TenantID, owner.UserID).Updates(map[string]any{"status": dailyreview.CacheGenerating, "claim_token": token, "lease_until": until, "valid_until": nil, "result_json": nil, "rendered_text": nil, "evidence_hash": "", "content_hash": "", "error_code": "", "updated_at": now})
		if write.Error != nil {
			return write.Error
		}
		row.Status, row.ClaimToken, row.LeaseUntil, row.UpdatedAt = string(dailyreview.CacheGenerating), token, &until, now
		result = dailyreview.ClaimResult{Record: cacheFromRow(row), Generator: true}
		return nil
	})
	return
}

func (s *Store) CommitReady(ctx context.Context, owner skill.Owner, id, token string, result dailyreview.CachedResult, validUntil, now time.Time) (dailyreview.CacheRecord, error) {
	if err := result.Validate(128 * 1024); err != nil {
		return dailyreview.CacheRecord{}, err
	}
	write := s.db.WithContext(ctx).Model(&dailyReviewCacheRow{}).Where("id=? AND tenant_id=? AND user_id=? AND status='generating' AND claim_token=? AND lease_until>=?", id, owner.TenantID, owner.UserID, token, now).Updates(map[string]any{"status": dailyreview.CacheReady, "claim_token": "", "valid_until": validUntil, "result_json": []byte(result.Structured), "rendered_text": result.Rendered, "evidence_hash": result.EvidenceHash, "content_hash": result.ContentHash, "error_code": "", "updated_at": now})
	if write.Error != nil {
		return dailyreview.CacheRecord{}, write.Error
	}
	if write.RowsAffected != 1 {
		return dailyreview.CacheRecord{}, dailyreview.ErrClaimLost
	}
	var row dailyReviewCacheRow
	if err := s.db.WithContext(ctx).Where("id=? AND tenant_id=? AND user_id=?", id, owner.TenantID, owner.UserID).First(&row).Error; err != nil {
		return dailyreview.CacheRecord{}, err
	}
	return cacheFromRow(row), nil
}

func (s *Store) FailClaim(ctx context.Context, owner skill.Owner, id, token, code string, now time.Time) error {
	write := s.db.WithContext(ctx).Model(&dailyReviewCacheRow{}).Where("id=? AND tenant_id=? AND user_id=? AND status='generating' AND claim_token=?", id, owner.TenantID, owner.UserID, token).Updates(map[string]any{"status": dailyreview.CacheFailed, "claim_token": "", "valid_until": now, "error_code": code, "updated_at": now})
	if write.Error != nil {
		return write.Error
	}
	if write.RowsAffected != 1 {
		return dailyreview.ErrClaimLost
	}
	return nil
}

func (s *Store) CleanupExpired(ctx context.Context, now time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, skill.ErrInvalidInvocation
	}
	var ids []string
	err := s.db.WithContext(ctx).Model(&dailyReviewCacheRow{}).Where("status IN ? AND COALESCE(valid_until,lease_until)<?", []string{string(dailyreview.CacheReady), string(dailyreview.CacheFailed)}, now).Order("updated_at").Limit(limit).Pluck("id", &ids).Error
	if err != nil || len(ids) == 0 {
		return 0, err
	}
	write := s.db.WithContext(ctx).Where("id IN ? AND status IN ?", ids, []string{string(dailyreview.CacheReady), string(dailyreview.CacheFailed)}).Delete(&dailyReviewCacheRow{})
	return int(write.RowsAffected), write.Error
}

func cacheFromRow(row dailyReviewCacheRow) dailyreview.CacheRecord {
	out := dailyreview.CacheRecord{ID: row.ID, Owner: skill.Owner{TenantID: row.TenantID, UserID: row.UserID}, LogicalKey: row.LogicalKey, SourceFingerprint: row.SourceFingerprint, Status: dailyreview.CacheStatus(row.Status), ClaimToken: row.ClaimToken, Result: dailyreview.CachedResult{Structured: append([]byte(nil), row.ResultJSON...), Rendered: row.RenderedText, EvidenceHash: row.EvidenceHash, ContentHash: row.ContentHash}, ErrorCode: row.ErrorCode, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}
	if row.LeaseUntil != nil {
		out.LeaseUntil = *row.LeaseUntil
	}
	if row.ValidUntil != nil {
		out.ValidUntil = *row.ValidUntil
	}
	return out
}
