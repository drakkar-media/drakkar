package database

import (
	"testing"
)

func TestUpsertReleaseBlockRuleCreatesCustomRule(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	created, err := db.UpsertReleaseBlockRule(ctx, ReleaseBlockRule{
		Type:         "release_group",
		Pattern:      "TESTGRP_upsert",
		MediaType:    "both",
		Action:       "block",
		ScorePenalty: 0,
		Enabled:      true,
		Note:         "test upsert",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from release_block_rules where id = $1`, created.ID)

	if created.ID == 0 {
		t.Fatal("expected a non-zero ID")
	}
	if created.Source != "custom" {
		t.Errorf("Source = %q, want custom (UpsertReleaseBlockRule always inserts as custom)", created.Source)
	}
	if created.Pattern != "TESTGRP_upsert" {
		t.Errorf("Pattern = %q, want TESTGRP_upsert", created.Pattern)
	}
	if !created.Enabled {
		t.Error("expected Enabled to be true")
	}
}

func TestListReleaseBlockRulesIncludesNewlyCreatedCustomRule(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	created, err := db.UpsertReleaseBlockRule(ctx, ReleaseBlockRule{
		Type:      "title_pattern",
		Pattern:   "some.title.pattern.list.test",
		MediaType: "movie",
		Action:    "penalty",
		Enabled:   true,
		Note:      "list test",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from release_block_rules where id = $1`, created.ID)

	rules, err := db.ListReleaseBlockRules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found *ReleaseBlockRule
	for i := range rules {
		if rules[i].ID == created.ID {
			found = &rules[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected the newly-created rule to appear in ListReleaseBlockRules")
	}
	if found.Pattern != "some.title.pattern.list.test" {
		t.Errorf("Pattern = %q, want some.title.pattern.list.test", found.Pattern)
	}
	if found.MediaType != "movie" {
		t.Errorf("MediaType = %q, want movie", found.MediaType)
	}
	if found.Action != "penalty" {
		t.Errorf("Action = %q, want penalty", found.Action)
	}
}

func TestUpdateReleaseBlockRuleFullyEditsCustomRule(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	created, err := db.UpsertReleaseBlockRule(ctx, ReleaseBlockRule{
		Type:         "regex",
		Pattern:      "^original-pattern$",
		MediaType:    "tv",
		Action:       "block",
		ScorePenalty: 0,
		Enabled:      true,
		Note:         "original note",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.ExecContext(ctx, `delete from release_block_rules where id = $1`, created.ID)

	updated, err := db.UpdateReleaseBlockRule(ctx, ReleaseBlockRule{
		ID:           created.ID,
		Type:         "missing_release_group",
		Pattern:      "^updated-pattern$",
		MediaType:    "movie",
		Action:       "penalty",
		ScorePenalty: 25,
		Enabled:      false,
		Note:         "updated note",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Type != "missing_release_group" {
		t.Errorf("Type = %q, want missing_release_group", updated.Type)
	}
	if updated.Pattern != "^updated-pattern$" {
		t.Errorf("Pattern = %q, want ^updated-pattern$", updated.Pattern)
	}
	if updated.MediaType != "movie" {
		t.Errorf("MediaType = %q, want movie", updated.MediaType)
	}
	if updated.Action != "penalty" {
		t.Errorf("Action = %q, want penalty", updated.Action)
	}
	if updated.ScorePenalty != 25 {
		t.Errorf("ScorePenalty = %d, want 25", updated.ScorePenalty)
	}
	if updated.Enabled {
		t.Error("expected Enabled to be false after update")
	}
	if updated.Note != "updated note" {
		t.Errorf("Note = %q, want %q", updated.Note, "updated note")
	}
}

// TestUpdateReleaseBlockRuleOnDefaultSourceOnlyTogglesEnabledAndNote guards
// the CASE WHEN source = 'custom' logic in UpdateReleaseBlockRule: for a
// non-custom (default/trash) rule, only enabled/note may be changed by a
// user -- rule_type/pattern/media_type/action/score_penalty must be left
// untouched even though the caller passes different values for them,
// preventing a user-facing "disable this rule" toggle from accidentally
// rewriting a seeded TRaSH-guide rule's actual matching behavior.
func TestUpdateReleaseBlockRuleOnDefaultSourceOnlyTogglesEnabledAndNote(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	var defaultID int64
	var originalPattern, originalType, originalMediaType, originalAction, originalNote string
	var originalScorePenalty int
	var originalEnabled bool
	if err := sqlDB.QueryRowContext(ctx, `
		select id, rule_type, pattern, media_type, action, score_penalty, enabled, note
		from release_block_rules
		where source = 'default'
		order by id asc
		limit 1`,
	).Scan(&defaultID, &originalType, &originalPattern, &originalMediaType, &originalAction, &originalScorePenalty, &originalEnabled, &originalNote); err != nil {
		t.Skipf("no source='default' seed row available to test against: %v", err)
	}
	// Restore the seed row's mutable fields exactly once the test finishes,
	// since UpdateReleaseBlockRule legitimately mutates enabled/note for any
	// source (this is real seed data, not a disposable test fixture).
	defer sqlDB.ExecContext(ctx, `
		update release_block_rules set enabled = $2, note = $3 where id = $1`,
		defaultID, originalEnabled, originalNote)

	updated, err := db.UpdateReleaseBlockRule(ctx, ReleaseBlockRule{
		ID:           defaultID,
		Type:         "missing_release_group", // attempted change, must be ignored
		Pattern:      "SHOULD_NOT_BE_APPLIED",  // attempted change, must be ignored
		MediaType:    "movie",                  // attempted change, must be ignored
		Action:       "penalty",                // attempted change, must be ignored
		ScorePenalty: 999,                       // attempted change, must be ignored
		Enabled:      !originalEnabled,          // allowed to change
		Note:         "test toggled note",       // allowed to change
	})
	if err != nil {
		t.Fatal(err)
	}

	if updated.Pattern != originalPattern {
		t.Errorf("Pattern changed on a default-source rule: got %q, want unchanged %q", updated.Pattern, originalPattern)
	}
	if updated.Type != originalType {
		t.Errorf("Type changed on a default-source rule: got %q, want unchanged %q", updated.Type, originalType)
	}
	if updated.MediaType != originalMediaType {
		t.Errorf("MediaType changed on a default-source rule: got %q, want unchanged %q", updated.MediaType, originalMediaType)
	}
	if updated.Action != originalAction {
		t.Errorf("Action changed on a default-source rule: got %q, want unchanged %q", updated.Action, originalAction)
	}
	if updated.ScorePenalty != originalScorePenalty {
		t.Errorf("ScorePenalty changed on a default-source rule: got %d, want unchanged %d", updated.ScorePenalty, originalScorePenalty)
	}
	if updated.Enabled == originalEnabled {
		t.Error("expected Enabled to have toggled on a default-source rule")
	}
	if updated.Note != "test toggled note" {
		t.Errorf("Note = %q, want the updated note (note is always editable)", updated.Note)
	}
}

func TestDeleteReleaseBlockRuleOnlyRemovesCustomRules(t *testing.T) {
	db, sqlDB, ctx := openBlocklistTestDB(t)

	custom, err := db.UpsertReleaseBlockRule(ctx, ReleaseBlockRule{
		Type: "release_group", Pattern: "TESTGRP_delete", MediaType: "both", Action: "block", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteReleaseBlockRule(ctx, custom.ID); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from release_block_rules where id = $1`, custom.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("expected the custom rule to be deleted, found %d", count)
	}

	var defaultID int64
	if err := sqlDB.QueryRowContext(ctx, `
		select id from release_block_rules where source = 'default' order by id asc limit 1`,
	).Scan(&defaultID); err != nil {
		t.Skipf("no source='default' seed row available to test against: %v", err)
	}
	if err := db.DeleteReleaseBlockRule(ctx, defaultID); err != nil {
		t.Fatal(err)
	}
	var defaultCount int
	if err := sqlDB.QueryRowContext(ctx, `select count(*) from release_block_rules where id = $1`, defaultID).Scan(&defaultCount); err != nil {
		t.Fatal(err)
	}
	if defaultCount != 1 {
		t.Fatalf("expected DeleteReleaseBlockRule to be a no-op on a default-source rule (only 'custom' rules may be deleted), but it was removed")
	}
}
