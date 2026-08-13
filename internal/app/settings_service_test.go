package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/drakkar-media/drakkar/internal/config"
)

type settingsApplierStub struct {
	applied  []config.Settings
	failNext bool
}

func (a *settingsApplierStub) ApplySettings(_ context.Context, cfg config.Settings) error {
	a.applied = append(a.applied, cfg)
	if a.failNext {
		a.failNext = false
		return errors.New("apply failed")
	}
	return nil
}

func TestFileSettingsServiceDoesNotPersistFailedApplication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	current := validSettingsForUpdateTest()
	current.NZBHydra2.URL = "http://old-hydra.example/api"
	if err := config.Save(path, current); err != nil {
		t.Fatal(err)
	}

	applier := &settingsApplierStub{failNext: true}
	service := &fileSettingsService{path: path, applier: applier}
	incoming := current
	incoming.NZBHydra2.URL = "http://new-hydra.example/api"
	if _, err := service.UpdateSettings(context.Background(), incoming); err == nil {
		t.Fatal("expected live application failure")
	}

	persisted, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.NZBHydra2.URL != current.NZBHydra2.URL {
		t.Fatalf("failed update persisted %q, want %q", persisted.NZBHydra2.URL, current.NZBHydra2.URL)
	}
	if len(applier.applied) != 2 {
		t.Fatalf("applier called %d times, want candidate plus rollback", len(applier.applied))
	}
	if applier.applied[0].NZBHydra2.URL != incoming.NZBHydra2.URL || applier.applied[1].NZBHydra2.URL != current.NZBHydra2.URL {
		t.Fatalf("unexpected apply sequence: %+v", applier.applied)
	}
	if _, err := os.Stat(path + ".pending"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pending settings file was not removed: %v", err)
	}
}

func TestFileSettingsServicePersistsAfterSuccessfulApplication(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	current := validSettingsForUpdateTest()
	if err := config.Save(path, current); err != nil {
		t.Fatal(err)
	}

	applier := &settingsApplierStub{}
	service := &fileSettingsService{path: path, applier: applier}
	incoming := current
	incoming.NZBHydra2.URL = "http://new-hydra.example/api"
	updated, err := service.UpdateSettings(context.Background(), incoming)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if updated.NZBHydra2.URL != incoming.NZBHydra2.URL || persisted.NZBHydra2.URL != incoming.NZBHydra2.URL {
		t.Fatalf("successful update was not persisted: updated=%q persisted=%q", updated.NZBHydra2.URL, persisted.NZBHydra2.URL)
	}
	if len(applier.applied) != 1 {
		t.Fatalf("applier called %d times, want 1", len(applier.applied))
	}
}

func validSettingsForUpdateTest() config.Settings {
	return config.Settings{
		Database: config.DatabaseConfig{
			Host:     "postgres",
			Port:     5432,
			Name:     "drakkar",
			Username: "drakkar",
		},
		Valkey: config.ValkeyConfig{Host: "valkey", Port: 6379},
	}
}
