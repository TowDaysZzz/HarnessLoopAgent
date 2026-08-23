package memory

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var sensitiveKey = regexp.MustCompile(`(?i)(access[_-]?token|refresh[_-]?token|authorization|cookie|password|passwd|api[_-]?key|secret|private[_-]?key)`)
var sensitiveValue = regexp.MustCompile(`(?i)(bearer\s+[a-z0-9._~+/-]{8,}|-----begin [a-z ]*private key-----)`)
var allowedSourceTypes = map[string]struct{}{"workflow": {}, "user_message": {}, "trusted_system": {}, "note": {}, "api": {}}

func ValidateContent(text string, value StructuredValue, source SourceRef) error {
	if source.Type == "" || source.ID == "" {
		return fmt.Errorf("%w: source type and id are required", ErrInvalidInput)
	}
	if _, ok := allowedSourceTypes[source.Type]; !ok {
		return fmt.Errorf("%w: unsupported source type", ErrInvalidInput)
	}
	if sensitiveValue.MatchString(text) || sensitiveKey.MatchString(source.Type) || sensitiveKey.MatchString(source.ID) || sensitiveKey.MatchString(source.EvidenceID) {
		return ErrSensitiveContent
	}
	if err := scanSensitive(value.Data); err != nil {
		return err
	}
	return nil
}

func NormalizeAuditFields(actor, reason string) (string, string, error) {
	actor, reason = strings.TrimSpace(actor), strings.TrimSpace(reason)
	if actor == "" {
		actor = "system"
	}
	if reason == "" {
		reason = "unspecified"
	}
	if len(actor) > 191 || sensitiveKey.MatchString(actor) || sensitiveValue.MatchString(actor) || !validReasonCode(reason) || sensitiveKey.MatchString(reason) || sensitiveValue.MatchString(reason) {
		return "", "", ErrSensitiveContent
	}
	return actor, reason, nil
}

func scanSensitive(value any) error {
	switch v := value.(type) {
	case map[string]any:
		for key, item := range v {
			if sensitiveKey.MatchString(key) {
				return fmt.Errorf("%w: forbidden key", ErrSensitiveContent)
			}
			if err := scanSensitive(item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range v {
			if err := scanSensitive(item); err != nil {
				return err
			}
		}
	case string:
		if sensitiveValue.MatchString(v) {
			return ErrSensitiveContent
		}
	}
	return nil
}

func boundedReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > 128 {
		reason = reason[:128]
	}
	if sensitiveValue.MatchString(reason) {
		return "redacted"
	}
	return reason
}

func SafeStructuredJSON(value StructuredValue) ([]byte, error) {
	if err := scanSensitive(value.Data); err != nil {
		return nil, err
	}
	return json.Marshal(value)
}
