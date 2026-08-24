package dailyreview

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/skill"
)

var (
	ErrCacheNotFound = errors.New("daily review cache not found")
	ErrClaimLost     = errors.New("daily review cache claim lost")
)

type CacheStatus string

const (
	CacheGenerating CacheStatus = "generating"
	CacheReady      CacheStatus = "ready"
	CacheFailed     CacheStatus = "failed"
)

type CacheIdentity struct {
	Owner               skill.Owner `json:"owner"`
	Window              Window      `json:"window"`
	OptionsHash         string      `json:"options_hash"`
	SkillID             string      `json:"skill_id"`
	SkillVersion        string      `json:"skill_version"`
	SchemaVersion       string      `json:"schema_version"`
	PromptPolicyVersion string      `json:"prompt_policy_version"`
}

func (i CacheIdentity) LogicalKey() (string, error) {
	if !i.Owner.Valid() || !i.Window.Valid() || i.OptionsHash == "" || i.SkillID == "" || i.SkillVersion == "" || i.SchemaVersion == "" || i.PromptPolicyVersion == "" {
		return "", skill.ErrInvalidInvocation
	}
	b, _ := json.Marshal(i)
	return hashBytes(b), nil
}

func SourceFingerprint(snapshot SourceSnapshot) (string, error) {
	if err := snapshot.Normalize(); err != nil {
		return "", err
	}
	return snapshot.Digest, nil
}

type CachedResult struct {
	Structured   json.RawMessage `json:"structured"`
	Rendered     string          `json:"rendered"`
	EvidenceHash string          `json:"evidence_hash"`
	ContentHash  string          `json:"content_hash"`
}

func (r CachedResult) Validate(maxBytes int) error {
	if maxBytes < 1 || len(r.Structured)+len(r.Rendered) > maxBytes || !json.Valid(r.Structured) || r.Rendered == "" || r.EvidenceHash == "" || r.ContentHash != ContentHash(r.Rendered) {
		return skill.ErrOutputLimit
	}
	return nil
}

type CacheRecord struct {
	ID                            string
	Owner                         skill.Owner
	LogicalKey, SourceFingerprint string
	Status                        CacheStatus
	ClaimToken                    string
	LeaseUntil                    time.Time
	ValidUntil                    time.Time
	Result                        CachedResult
	ErrorCode                     string
	CreatedAt, UpdatedAt          time.Time
}

type ClaimResult struct {
	Record    CacheRecord
	Generator bool
}
type CacheRepository interface {
	Lookup(context.Context, skill.Owner, string, string, time.Time) (CacheRecord, error)
	Claim(context.Context, skill.Owner, string, string, time.Time, time.Duration) (ClaimResult, error)
	CommitReady(context.Context, skill.Owner, string, string, CachedResult, time.Time, time.Time) (CacheRecord, error)
	FailClaim(context.Context, skill.Owner, string, string, string, time.Time) error
	CleanupExpired(context.Context, time.Time, int) (int, error)
}

func ComputeValidUntil(now time.Time, ttl time.Duration, memoryExpiry *time.Time, policyUntil *time.Time) time.Time {
	out := now.Add(ttl)
	for _, candidate := range []*time.Time{memoryExpiry, policyUntil} {
		if candidate != nil && candidate.Before(out) {
			out = *candidate
		}
	}
	return out.UTC()
}

type MemoryCache struct {
	mu      sync.Mutex
	records map[string]CacheRecord
}

func NewMemoryCache() *MemoryCache { return &MemoryCache{records: map[string]CacheRecord{}} }
func cacheMapKey(owner skill.Owner, logical, source string) string {
	return fmt.Sprintf("%d:%d:%s:%s", owner.TenantID, owner.UserID, logical, source)
}
func (m *MemoryCache) Lookup(_ context.Context, owner skill.Owner, logical, source string, now time.Time) (CacheRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.records[cacheMapKey(owner, logical, source)]
	if !ok || v.Owner != owner || v.Status != CacheReady || !now.Before(v.ValidUntil) {
		return CacheRecord{}, ErrCacheNotFound
	}
	return v, nil
}
func (m *MemoryCache) Claim(_ context.Context, owner skill.Owner, logical, source string, now time.Time, lease time.Duration) (ClaimResult, error) {
	if !owner.Valid() || logical == "" || source == "" || lease <= 0 {
		return ClaimResult{}, skill.ErrInvalidInvocation
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := cacheMapKey(owner, logical, source)
	v, ok := m.records[key]
	if ok && v.Status == CacheReady && now.Before(v.ValidUntil) {
		return ClaimResult{Record: v}, nil
	}
	if ok && v.Status == CacheGenerating && now.Before(v.LeaseUntil) {
		return ClaimResult{Record: v}, nil
	}
	token, err := claimToken()
	if err != nil {
		return ClaimResult{}, err
	}
	if !ok {
		v = CacheRecord{ID: token, Owner: owner, LogicalKey: logical, SourceFingerprint: source, CreatedAt: now}
	}
	v.Status, v.ClaimToken, v.LeaseUntil, v.UpdatedAt = CacheGenerating, token, now.Add(lease), now
	m.records[key] = v
	return ClaimResult{Record: v, Generator: true}, nil
}
func (m *MemoryCache) CommitReady(_ context.Context, owner skill.Owner, id, token string, result CachedResult, validUntil, now time.Time) (CacheRecord, error) {
	if err := result.Validate(128 * 1024); err != nil {
		return CacheRecord{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, v := range m.records {
		if v.ID == id && v.Owner == owner {
			if v.Status != CacheGenerating || v.ClaimToken != token || now.After(v.LeaseUntil) {
				return CacheRecord{}, ErrClaimLost
			}
			v.Status, v.Result, v.ValidUntil, v.ClaimToken, v.UpdatedAt = CacheReady, result, validUntil, "", now
			m.records[key] = v
			return v, nil
		}
	}
	return CacheRecord{}, ErrCacheNotFound
}
func (m *MemoryCache) FailClaim(_ context.Context, owner skill.Owner, id, token, code string, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, v := range m.records {
		if v.ID == id && v.Owner == owner {
			if v.Status != CacheGenerating || v.ClaimToken != token {
				return ErrClaimLost
			}
			v.Status, v.ErrorCode, v.ClaimToken, v.UpdatedAt = CacheFailed, boundedCode(code), "", now
			m.records[key] = v
			return nil
		}
	}
	return ErrCacheNotFound
}
func (m *MemoryCache) CleanupExpired(_ context.Context, now time.Time, limit int) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for key, v := range m.records {
		if count == limit {
			break
		}
		terminal := v.Status == CacheReady || v.Status == CacheFailed
		if terminal && !now.Before(v.ValidUntil) && !now.Before(v.LeaseUntil) {
			delete(m.records, key)
			count++
		}
	}
	return count, nil
}
func claimToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func boundedCode(code string) string {
	code = strings.TrimSpace(code)
	if len(code) > 128 {
		code = code[:128]
	}
	return code
}
