package mysqlstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
	"gorm.io/gorm"
)

func (s *Store) CreateInvocation(ctx context.Context, value skill.Invocation) (skill.Invocation, bool, error) {
	if err := value.Validate(); err != nil {
		return skill.Invocation{}, false, err
	}
	row := invocationToRow(value)
	err := s.db.WithContext(ctx).Create(&row).Error
	if err == nil {
		return value, true, nil
	}
	if !duplicateKey(err) {
		return skill.Invocation{}, false, err
	}
	var existing skillInvocationRow
	if loadErr := s.db.WithContext(ctx).Where("chat_run_id=? AND tenant_id=? AND user_id=?", value.ChatRunID, value.Owner.TenantID, value.Owner.UserID).First(&existing).Error; loadErr != nil {
		return skill.Invocation{}, false, loadErr
	}
	loaded, loadErr := invocationFromRow(existing)
	if loadErr != nil {
		return skill.Invocation{}, false, loadErr
	}
	if loaded.Skill != value.Skill || loaded.ArgumentsHash != value.ArgumentsHash || loaded.SessionID != value.SessionID {
		return skill.Invocation{}, false, fmt.Errorf("%w: chat run invocation differs", skill.ErrInvalidInvocation)
	}
	return loaded, false, nil
}

func (s *Store) GetInvocation(ctx context.Context, owner skill.Owner, id string) (skill.Invocation, error) {
	if !owner.Valid() {
		return skill.Invocation{}, skill.ErrInvalidInvocation
	}
	var row skillInvocationRow
	err := s.db.WithContext(ctx).Where("id=? AND tenant_id=? AND user_id=?", id, owner.TenantID, owner.UserID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return skill.Invocation{}, skill.ErrNotFound
	}
	if err != nil {
		return skill.Invocation{}, err
	}
	return invocationFromRow(row)
}

func (s *Store) TransitionInvocation(ctx context.Context, owner skill.Owner, id string, from, to skill.InvocationStatus, errorCode string, now time.Time) (skill.Invocation, error) {
	if !owner.Valid() || !skill.CanTransitionInvocation(from, to) || len(errorCode) > 128 {
		return skill.Invocation{}, skill.ErrInvalidInvocation
	}
	result := s.db.WithContext(ctx).Model(&skillInvocationRow{}).Where("id=? AND tenant_id=? AND user_id=? AND status=?", id, owner.TenantID, owner.UserID, from).Updates(map[string]any{"status": string(to), "error_code": errorCode, "updated_at": now.UTC()})
	if result.Error != nil {
		return skill.Invocation{}, result.Error
	}
	if result.RowsAffected != 1 {
		return skill.Invocation{}, skill.ErrNotFound
	}
	return s.GetInvocation(ctx, owner, id)
}

func invocationToRow(value skill.Invocation) skillInvocationRow {
	return skillInvocationRow{ID: value.ID, TenantID: value.Owner.TenantID, UserID: value.Owner.UserID, SessionID: value.SessionID, ChatRunID: value.ChatRunID, SkillID: string(value.Skill.ID), SkillVersion: string(value.Skill.Version), ArgumentsJSON: append([]byte(nil), value.Arguments...), ArgumentsHash: value.ArgumentsHash, Status: string(value.Status), CreatedAt: value.CreatedAt.UTC(), UpdatedAt: value.UpdatedAt.UTC()}
}

func invocationFromRow(row skillInvocationRow) (skill.Invocation, error) {
	arguments, hash, err := skill.NormalizeArguments(row.ArgumentsJSON, 8*1024)
	if err != nil || hash != row.ArgumentsHash {
		return skill.Invocation{}, skill.ErrInvalidInvocation
	}
	value := skill.Invocation{ID: row.ID, Owner: skill.Owner{TenantID: row.TenantID, UserID: row.UserID}, SessionID: row.SessionID, ChatRunID: row.ChatRunID, Skill: skill.Ref{ID: skill.ID(row.SkillID), Version: skill.Version(row.SkillVersion)}, Arguments: arguments, ArgumentsHash: row.ArgumentsHash, Status: skill.InvocationStatus(row.Status), CreatedAt: row.CreatedAt.UTC(), UpdatedAt: row.UpdatedAt.UTC()}
	if err := value.Validate(); err != nil {
		return skill.Invocation{}, err
	}
	return value, nil
}
