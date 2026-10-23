package relay

import (
	"context"
	"net/http"
)

func withHost(ctx context.Context, hostID string) context.Context {
	return context.WithValue(ctx, hostIDKey, hostID)
}

func hostFrom(r *http.Request) string {
	if v, ok := r.Context().Value(hostIDKey).(string); ok {
		return v
	}
	return ""
}
