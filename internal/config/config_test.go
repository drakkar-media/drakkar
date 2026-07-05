package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRejectsUnknownField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{
  "database":{"host":"postgres","port":5432,"name":"drakkar","username":"drakkar","password":"secret"},
  "valkey":{"host":"valkey","port":6379,"password":""},
  "nzbhydra2":{"url":"http://nzbhydra2:5076","apiKey":""},
  "seerr":{"url":"http://seerr:5055","apiKey":""},
  "usenet":{"providers":[{"name":"primary","host":"news","port":563,"tls":true,"username":"","password":"","maxConnections":20,"enabled":true}]},
  "metadata":{"tmdb":{"apiKey":""},"tvdb":{"apiKey":""}},
  "subtitles":{"enabled":true,"languages":["nl","en"],"providers":{"subdl":{"enabled":true,"apiKey":""}}},
  "oops":true
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown field error, got %v", err)
	}
}

func TestRedactedSettings(t *testing.T) {
	out := RedactedSettings(Settings{
		Database:  DatabaseConfig{Host: "postgres", Port: 5432, Name: "drakkar", Username: "drakkar", Password: "secret"},
		Valkey:    ValkeyConfig{Host: "valkey", Port: 6379, Password: "secret"},
		NZBHydra2: ServiceConfig{URL: "http://nzbhydra2:5076", APIKey: "abc"},
		Seerr:     ServiceConfig{URL: "http://seerr:5055", APIKey: "def"},
		Usenet: UsenetConfig{
			MaxDownloadConnections: 15,
			StreamingPriorityPct:   80,
			ArticleBufferSize:      40,
			Providers:              []UsenetProvider{{Name: "primary", Host: "news", Port: 563, TLS: true, Username: "u", Password: "p", MaxConnections: 20, Enabled: true}},
		},
		Metadata: MetadataConfig{TMDB: APIKeyConfig{APIKey: "ghi"}, TVDB: APIKeyConfig{APIKey: "jkl"}},
		Subtitles: SubtitlesConfig{Enabled: true, Languages: []string{"en"}, Providers: map[string]SubtitleAuth{
			"subdl": {Enabled: true, APIKey: "xyz", Username: "u", Password: "p"},
		}},
	})
	if out["database"].(map[string]any)["password"] != "***" {
		t.Fatal("database password not redacted")
	}
	if out["metadata"].(map[string]any)["tmdb"].(map[string]any)["apiKey"] != "***" {
		t.Fatal("tmdb api key not redacted")
	}
	if out["subtitles"].(map[string]any)["providers"].(map[string]any)["subdl"].(map[string]any)["password"] != "***" {
		t.Fatal("subtitle password not redacted")
	}
	if out["usenet"].(map[string]any)["providers"].([]map[string]any)[0]["password"] != "***" {
		t.Fatal("usenet password not redacted")
	}
}

func TestValidatePathsRejectsNestedLibrary(t *testing.T) {
	rt := DefaultRuntime()
	rt.MovieLibraryPath = "/mnt/drakkar/vfs/media/movies"
	err := ValidatePaths(rt)
	if err == nil || !strings.Contains(err.Error(), "outside fuse mount") {
		t.Fatalf("expected fuse separation error, got %v", err)
	}
}

func TestLoadAppliesIndexerWorkerDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{
  "database":{"host":"postgres","port":5432,"name":"drakkar","username":"drakkar","password":"secret"},
  "valkey":{"host":"valkey","port":6379,"password":""},
  "nzbhydra2":{"url":"http://nzbhydra2:5076","apiKey":""},
  "seerr":{"url":"http://seerr:5055","apiKey":""},
  "usenet":{"providers":[{"name":"primary","host":"news","port":563,"tls":true,"username":"","password":"","maxConnections":20,"enabled":true}]},
  "metadata":{"tmdb":{"apiKey":""},"tvdb":{"apiKey":""}},
  "subtitles":{"enabled":true,"languages":["en"],"providers":{"subdl":{"enabled":true,"apiKey":""}}},
  "indexer":{"searchDelayMs":0}
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Indexer.BackgroundSearchWorkers != 10 {
		t.Fatalf("expected default backgroundSearchWorkers=10, got %d", cfg.Indexer.BackgroundSearchWorkers)
	}
}

func testSettingsWithSecrets() Settings {
	return Settings{
		Database:  DatabaseConfig{Host: "postgres", Password: "db-secret"},
		Valkey:    ValkeyConfig{Host: "valkey", Password: "valkey-secret"},
		NZBHydra2: ServiceConfig{URL: "http://hydra", APIKey: "hydra-key"},
		Seerr:     ServiceConfig{URL: "http://seerr", APIKey: "seerr-key"},
		Usenet: UsenetConfig{
			Providers: []UsenetProvider{
				{Name: "primary", Host: "news1", Username: "u1", Password: "provider1-secret"},
				{Name: "backup", Host: "news2", Username: "u2", Password: "provider2-secret"},
			},
		},
		Metadata: MetadataConfig{
			TMDB: APIKeyConfig{APIKey: "tmdb-key"},
			TVDB: APIKeyConfig{APIKey: "tvdb-key"},
		},
		Plex:     PlexConfig{URL: "http://plex", Token: "plex-token"},
		Jellyfin: JellyfinConfig{URL: "http://jellyfin", APIKey: "jellyfin-key"},
		Subtitles: SubtitlesConfig{
			Enabled: true,
			Providers: map[string]SubtitleAuth{
				"subdl":         {Enabled: true, APIKey: "subdl-key"},
				"opensubtitles": {Enabled: true, Username: "osuser", Password: "os-secret"},
			},
		},
	}
}

func TestRedactSecretsBlanksEveryCredential(t *testing.T) {
	cfg := testSettingsWithSecrets()
	redacted := RedactSecrets(cfg)

	if redacted.Database.Password != "" || redacted.Valkey.Password != "" ||
		redacted.NZBHydra2.APIKey != "" || redacted.Seerr.APIKey != "" ||
		redacted.Metadata.TMDB.APIKey != "" || redacted.Metadata.TVDB.APIKey != "" ||
		redacted.Plex.Token != "" || redacted.Jellyfin.APIKey != "" {
		t.Fatalf("expected all top-level secrets blanked, got %+v", redacted)
	}
	for _, p := range redacted.Usenet.Providers {
		if p.Password != "" {
			t.Fatalf("expected usenet provider %q password blanked, got %q", p.Name, p.Password)
		}
	}
	for name, auth := range redacted.Subtitles.Providers {
		if auth.APIKey != "" || auth.Password != "" {
			t.Fatalf("expected subtitle provider %q secrets blanked, got %+v", name, auth)
		}
	}
	// Non-secret fields must survive redaction untouched.
	if redacted.Database.Host != "postgres" || redacted.Usenet.Providers[0].Username != "u1" {
		t.Fatalf("redaction must not touch non-secret fields, got %+v", redacted)
	}
	// Original must be unmodified (no aliasing into cfg's slice/map).
	if cfg.Database.Password != "db-secret" || cfg.Usenet.Providers[0].Password != "provider1-secret" {
		t.Fatalf("RedactSecrets must not mutate its input, got %+v", cfg)
	}
}

func TestMergeSecretsPreservesUntouchedFields(t *testing.T) {
	current := testSettingsWithSecrets()
	incoming := RedactSecrets(current)
	// Simulate the user changing an unrelated field without touching secrets.
	incoming.Database.Host = "postgres-new-host"

	merged := MergeSecrets(current, incoming)

	if merged.Database.Password != "db-secret" || merged.Valkey.Password != "valkey-secret" ||
		merged.NZBHydra2.APIKey != "hydra-key" || merged.Seerr.APIKey != "seerr-key" ||
		merged.Metadata.TMDB.APIKey != "tmdb-key" || merged.Metadata.TVDB.APIKey != "tvdb-key" ||
		merged.Plex.Token != "plex-token" || merged.Jellyfin.APIKey != "jellyfin-key" {
		t.Fatalf("expected untouched secrets preserved from current, got %+v", merged)
	}
	if merged.Database.Host != "postgres-new-host" {
		t.Fatalf("expected non-secret edit to apply, got %q", merged.Database.Host)
	}
	byName := map[string]UsenetProvider{}
	for _, p := range merged.Usenet.Providers {
		byName[p.Name] = p
	}
	if byName["primary"].Password != "provider1-secret" || byName["backup"].Password != "provider2-secret" {
		t.Fatalf("expected usenet provider passwords preserved by name, got %+v", merged.Usenet.Providers)
	}
	if merged.Subtitles.Providers["subdl"].APIKey != "subdl-key" ||
		merged.Subtitles.Providers["opensubtitles"].Password != "os-secret" {
		t.Fatalf("expected subtitle provider secrets preserved by key, got %+v", merged.Subtitles.Providers)
	}
}

func TestMergeSecretsAppliesExplicitChange(t *testing.T) {
	current := testSettingsWithSecrets()
	incoming := RedactSecrets(current)
	incoming.Database.Password = "rotated-secret"
	incoming.Usenet.Providers[0].Password = "rotated-provider-secret"

	merged := MergeSecrets(current, incoming)

	if merged.Database.Password != "rotated-secret" {
		t.Fatalf("expected explicitly set secret to apply, got %q", merged.Database.Password)
	}
	if merged.Usenet.Providers[0].Password != "rotated-provider-secret" {
		t.Fatalf("expected explicitly set provider secret to apply, got %q", merged.Usenet.Providers[0].Password)
	}
	// Untouched provider must still be preserved.
	if merged.Usenet.Providers[1].Password != "provider2-secret" {
		t.Fatalf("expected untouched provider secret preserved, got %q", merged.Usenet.Providers[1].Password)
	}
}
