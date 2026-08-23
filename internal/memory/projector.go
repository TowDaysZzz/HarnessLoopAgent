package memory

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/TowDaysZzz/HarnessLoopAgent/internal/ragclient"
)

type ProjectorConfig struct {
	BatchSize    int
	BaseBackoff  time.Duration
	MaxBackoff   time.Duration
	MaxAttempts  int
	ModelVersion string
}

type Projector struct {
	repository Repository
	indexer    interface {
		IndexMemory(context.Context, ragclient.MemoryIndexRequest) (*ragclient.MemoryIndexResponse, error)
	}
	config    ProjectorConfig
	telemetry Telemetry
}

func (p *Projector) SetTelemetry(telemetry Telemetry) {
	if p != nil {
		p.telemetry = telemetry
	}
}

func NewProjector(repository Repository, indexer interface {
	IndexMemory(context.Context, ragclient.MemoryIndexRequest) (*ragclient.MemoryIndexResponse, error)
}, config ProjectorConfig) (*Projector, error) {
	if repository == nil || indexer == nil || config.BatchSize < 1 || config.BatchSize > 200 || config.BaseBackoff <= 0 || config.MaxBackoff < config.BaseBackoff || config.MaxAttempts < 1 || config.ModelVersion == "" {
		return nil, ErrInvalidInput
	}
	return &Projector{repository: repository, indexer: indexer, config: config}, nil
}

type ProjectionBatchResult struct{ Claimed, Indexed, Skipped, Failed, PermanentFailed int }

func (p *Projector) RunBatch(ctx context.Context, now time.Time) (ProjectionBatchResult, error) {
	started := time.Now()
	claimed, err := p.repository.ClaimProjections(ctx, p.config.BatchSize, now)
	if err != nil {
		return ProjectionBatchResult{}, err
	}
	result := ProjectionBatchResult{Claimed: len(claimed)}
	for _, projection := range claimed {
		values, err := p.repository.BatchGet(ctx, projection.Owner, []string{projection.MemoryID})
		if err != nil {
			permanent := projection.Attempt >= p.config.MaxAttempts
			if failErr := p.fail(ctx, projection, "mysql_reload_failed", now, permanent); failErr != nil {
				return result, failErr
			}
			if permanent {
				result.PermanentFailed++
			} else {
				result.Failed++
			}
			continue
		}
		if len(values) != 1 || !values[0].IsActiveAt(now) || values[0].ContentHash != projection.ContentHash {
			if err := p.repository.CompleteProjection(ctx, projection.Owner, projection.ID, now); err != nil {
				return result, err
			}
			result.Skipped++
			continue
		}
		value := values[0]
		ownerCtx := ragclient.WithTrustedMemoryOwner(ctx, value.Owner.TenantID, value.Owner.UserID)
		_, err = p.indexer.IndexMemory(ownerCtx, ragclient.MemoryIndexRequest{MemoryID: value.ID, CanonicalText: value.CanonicalText, ContentHash: value.ContentHash, Layer: ragclient.MemoryLayer(value.Layer), Kind: ragclient.MemoryKind(value.Kind), CreatedAt: value.CreatedAt, ProjectionVersion: p.config.ModelVersion})
		if err == nil {
			if err := p.repository.CompleteProjection(ctx, projection.Owner, projection.ID, now); err != nil {
				return result, err
			}
			result.Indexed++
			continue
		}
		permanent := projection.Attempt >= p.config.MaxAttempts || !temporaryError(err)
		if failErr := p.fail(ctx, projection, projectionErrorCode(err), now, permanent); failErr != nil {
			return result, failErr
		}
		if permanent {
			result.PermanentFailed++
		} else {
			result.Failed++
		}
	}
	if p.telemetry != nil {
		p.telemetry.ObserveProjection(result, time.Since(started))
	}
	return result, nil
}

func (p *Projector) fail(ctx context.Context, projection Projection, code string, now time.Time, permanent bool) error {
	delay := p.config.BaseBackoff
	for i := 1; i < projection.Attempt; i++ {
		if delay >= p.config.MaxBackoff/2 {
			delay = p.config.MaxBackoff
			break
		}
		delay *= 2
	}
	if delay > p.config.MaxBackoff {
		delay = p.config.MaxBackoff
	}
	return p.repository.FailProjection(ctx, projection.Owner, projection.ID, code, now.Add(delay), permanent)
}

func temporaryError(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	var temporary interface{ Temporary() bool }
	return errors.As(err, &temporary) && temporary.Temporary()
}

func projectionErrorCode(err error) string {
	var api *ragclient.APIError
	if errors.As(err, &api) {
		return fmt.Sprintf("rag_http_%d", api.HTTPStatus)
	}
	var large *ragclient.ResponseTooLargeError
	if errors.As(err, &large) {
		return "rag_response_too_large"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "rag_timeout"
	}
	return "rag_contract_error"
}
