package auth

import "context"

type Principal struct {
	UserID      uint64 `json:"user_id"`
	TenantID    uint64 `json:"tenant_id"`
	Role        string `json:"role"`
	Email       string `json:"email,omitempty"`
	Name        string `json:"name,omitempty"`
	AccessToken string `json:"-"`
}

type principalKey struct{}

func WithPrincipal(ctx context.Context, principal Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, principal)
}

func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalKey{}).(Principal)
	return principal, ok && principal.UserID > 0 && principal.TenantID > 0
}
