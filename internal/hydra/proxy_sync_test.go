package hydra

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/drakkar-media/drakkar/internal/config"
)

// newFakeInternalAPIServer emulates enough of NZBHydra2's real
// /internalapi/config behavior to test SyncProxy against: GET returns the
// stored config with configured "secret" keys masked as the literal
// "***UNCHANGED***", and PUT stores every field except a masked secret,
// which is left as whatever was already stored (matching the real
// behavior confirmed live against a production NZBHydra2 instance).
func newFakeInternalAPIServer(t *testing.T, initial map[string]any, secretKeys map[string][]string) *httptest.Server {
	t.Helper()
	stored := initial

	mask := func(cfg map[string]any) map[string]any {
		masked := make(map[string]any, len(cfg))
		for section, v := range cfg {
			m, ok := v.(map[string]any)
			if !ok {
				masked[section] = v
				continue
			}
			cloned := make(map[string]any, len(m))
			for k, val := range m {
				cloned[k] = val
			}
			for _, key := range secretKeys[section] {
				if cloned[key] != nil {
					cloned[key] = "***UNCHANGED***"
				}
			}
			masked[section] = cloned
		}
		return masked
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/internalapi/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(mask(stored))
		case http.MethodPut:
			body, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			var incoming map[string]any
			if err := json.Unmarshal(body, &incoming); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			for section, v := range incoming {
				m, ok := v.(map[string]any)
				if !ok {
					stored[section] = v
					continue
				}
				existing, _ := stored[section].(map[string]any)
				merged := make(map[string]any, len(m))
				for k, val := range m {
					if val == "***UNCHANGED***" && existing != nil {
						merged[k] = existing[k]
						continue
					}
					merged[k] = val
				}
				stored[section] = merged
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":            true,
				"errorMessages": []string{},
				"newConfig":     stored,
			})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	return httptest.NewServer(mux)
}

func TestSyncProxyEnablesSOCKS5WithoutTouchingOtherSecrets(t *testing.T) {
	server := newFakeInternalAPIServer(t, map[string]any{
		"main": map[string]any{
			"proxyType":        "NONE",
			"proxyHost":        nil,
			"proxyPort":        1080.0,
			"proxyUsername":    nil,
			"proxyPassword":    nil,
			"proxyIgnoreLocal": true,
		},
		"indexers": []any{
			map[string]any{"name": "NZB Finder", "apiKey": "real-secret-key"},
		},
	}, map[string][]string{
		// only "main" secrets are masked in this fixture; indexer apiKey is
		// deliberately left unmasked here so the test can assert it was
		// never even read/rewritten by SyncProxy.
	})
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL + "/api"})

	err := client.SyncProxy(context.Background(), true, ProxyConfig{
		Host:     "ams.socks.privado.io",
		Port:     1080,
		Username: "user",
		Password: "pass",
	})
	if err != nil {
		t.Fatalf("SyncProxy: %v", err)
	}

	resp, err := http.Get(server.URL + "/internalapi/config")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	defer resp.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	main := got["main"].(map[string]any)
	if main["proxyType"] != "SOCKS" {
		t.Fatalf("expected proxyType SOCKS, got %v", main["proxyType"])
	}
	if main["proxyHost"] != "ams.socks.privado.io" {
		t.Fatalf("expected proxyHost set, got %v", main["proxyHost"])
	}
	if main["proxyUsername"] != "user" || main["proxyPassword"] != "pass" {
		t.Fatalf("expected proxy credentials set, got user=%v pass=%v", main["proxyUsername"], main["proxyPassword"])
	}

	indexers := got["indexers"].([]any)
	firstIndexer := indexers[0].(map[string]any)
	if firstIndexer["apiKey"] != "real-secret-key" {
		t.Fatalf("expected unrelated indexer apiKey to survive untouched, got %v", firstIndexer["apiKey"])
	}
}

func TestSyncProxyDisablesBackToNoProxy(t *testing.T) {
	server := newFakeInternalAPIServer(t, map[string]any{
		"main": map[string]any{
			"proxyType":     "SOCKS",
			"proxyHost":     "old-host",
			"proxyUsername": "old-user",
			"proxyPassword": "old-pass",
		},
	}, nil)
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL + "/api"})

	if err := client.SyncProxy(context.Background(), false, ProxyConfig{}); err != nil {
		t.Fatalf("SyncProxy: %v", err)
	}

	resp, err := http.Get(server.URL + "/internalapi/config")
	if err != nil {
		t.Fatalf("get config: %v", err)
	}
	defer resp.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	main := got["main"].(map[string]any)
	if main["proxyType"] != "NONE" {
		t.Fatalf("expected proxyType NONE, got %v", main["proxyType"])
	}
	if main["proxyHost"] != nil {
		t.Fatalf("expected proxyHost cleared, got %v", main["proxyHost"])
	}
}

func TestSyncProxyReturnsErrorOnRejection(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/internalapi/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"main": map[string]any{"proxyType": "NONE"}})
		case http.MethodPut:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "errorMessages": []string{"proxy host invalid"}})
		}
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewClient(config.ServiceConfig{URL: server.URL + "/api"})
	err := client.SyncProxy(context.Background(), true, ProxyConfig{Host: "bad"})
	if err == nil {
		t.Fatal("expected error when NZBHydra2 rejects the config")
	}
}
