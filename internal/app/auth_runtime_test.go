package app

import (
	"strings"
	"testing"

	"github.com/drakkar-media/drakkar/internal/config"
)

func TestApplyAuthRuntimeEnvironment(t *testing.T) {
	t.Setenv("DRAKKAR_AUTH_COOKIE_SECURE", "true")
	t.Setenv("DRAKKAR_AUTH_TRUST_PROXY_HEADERS", "1")
	runtime := config.DefaultRuntime()

	if err := applyAuthRuntimeEnvironment(&runtime); err != nil {
		t.Fatal(err)
	}
	if !runtime.AuthCookieSecure || !runtime.AuthTrustProxyHeaders {
		t.Fatalf("auth runtime flags not applied: %+v", runtime)
	}
}

func TestApplyAuthRuntimeEnvironmentRejectsInvalidBoolean(t *testing.T) {
	t.Setenv("DRAKKAR_AUTH_COOKIE_SECURE", "sometimes")
	runtime := config.DefaultRuntime()

	err := applyAuthRuntimeEnvironment(&runtime)
	if err == nil || !strings.Contains(err.Error(), "DRAKKAR_AUTH_COOKIE_SECURE") {
		t.Fatalf("expected named parse error, got %v", err)
	}
}
