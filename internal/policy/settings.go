package policy

import (
	"context"
	"strings"
)

const SettingsKey = "queue_policies"

type QueueDecisionAction string

const (
	QueueActionDoNothing                QueueDecisionAction = "do_nothing"
	QueueActionRemove                   QueueDecisionAction = "remove"
	QueueActionRemoveAndBlocklist       QueueDecisionAction = "remove_and_blocklist"
	QueueActionRemoveBlocklistAndSearch QueueDecisionAction = "remove_blocklist_and_search"
	QueueActionSearchAgain              QueueDecisionAction = "search_again"
)

type Settings struct {
	QueueDecisionActions map[string]QueueDecisionAction `json:"queueDecisionActions"`
	IgnoredPatterns      []string                       `json:"ignoredPatterns"`
	DuplicateNzbBehavior string                         `json:"duplicateNzbBehavior"`
	FailNzbWithoutVideo  bool                           `json:"failNzbWithoutVideo"`
	ImportStrategy       string                         `json:"importStrategy"`
	ManualUploadCategory string                         `json:"manualUploadCategory"`
	BlocklistTTLDays     int                            `json:"blocklistTtlDays"`
}

type Store interface {
	GetAppSetting(ctx context.Context, key string, dst any) (bool, error)
	PutAppSetting(ctx context.Context, key string, value any) error
}

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) Settings(ctx context.Context) (Settings, error) {
	settings := DefaultSettings()
	if s == nil || s.store == nil {
		return settings, nil
	}
	var stored Settings
	ok, err := s.store.GetAppSetting(ctx, SettingsKey, &stored)
	if err != nil || !ok {
		return settings, err
	}
	return mergeSettings(settings, stored), nil
}

func (s *Service) Update(ctx context.Context, input Settings) (Settings, error) {
	settings := mergeSettings(DefaultSettings(), input)
	if s == nil || s.store == nil {
		return settings, nil
	}
	if err := s.store.PutAppSetting(ctx, SettingsKey, settings); err != nil {
		return Settings{}, err
	}
	return settings, nil
}

func DefaultSettings() Settings {
	return Settings{
		DuplicateNzbBehavior: "mark_failed",
		FailNzbWithoutVideo:  false,
		ImportStrategy:       "symlink",
		ManualUploadCategory: "",
		BlocklistTTLDays:     30,
		QueueDecisionActions: map[string]QueueDecisionAction{
			// ── Sonarr/Radarr-compatible keys ──────────────────────────────
			"grabbedSeriesIdMismatch":     QueueActionDoNothing,
			"grabbedMovieIdMismatch":      QueueActionDoNothing,
			"episodeMissingInRelease":     QueueActionDoNothing,
			"unexpectedEpisodes":          QueueActionDoNothing,
			"notEpisodeUpgrade":           QueueActionRemoveAndBlocklist,
			"notMovieUpgrade":             QueueActionRemoveAndBlocklist,
			"notCustomFormatUpgrade":      QueueActionRemoveAndBlocklist,
			"noEligibleFiles":             QueueActionRemoveBlocklistAndSearch,
			"episodeAlreadyImported":      QueueActionRemove,
			"noAudioTracks":               QueueActionRemoveBlocklistAndSearch,
			"invalidSeasonEpisode":        QueueActionDoNothing,
			"singleEpisodeContainsSeason": QueueActionDoNothing,
			"unableToDetermineSample":     QueueActionDoNothing,
			"sample":                      QueueActionRemoveBlocklistAndSearch,
			"archiveNeedsExtraction":      QueueActionDoNothing,
			"missingArticles":             QueueActionRemoveBlocklistAndSearch,

			// ── Drakkar-native keys ─────────────────────────────────────────
			// All candidates matched search but had wrong titles — search again,
			// the rejected candidates are blocklisted automatically.
			"allCandidatesWrongTitle": QueueActionSearchAgain,
			// All candidates were rejected for other reasons (bad source, size,
			// quality) — search again for a replacement.
			"allCandidatesRejected": QueueActionSearchAgain,
			// No NZBs returned by indexers at all — do nothing, wait for new content.
			"noReleaseFound": QueueActionDoNothing,
			// Preflight (archive inspection) failed — blocklist bad release and search again.
			"preflightFailed": QueueActionRemoveBlocklistAndSearch,
			// NZB fetch returned a permanent HTTP error (401/404/410/451) — blocklist.
			"nzbFetch4xx": QueueActionRemoveBlocklistAndSearch,
			// NZB fetch returned 403 quota/rate-limit — retry later without blocklisting.
			"nzbFetch403": QueueActionDoNothing,
			// NZB fetch failed for other transient reasons — blocklist URL, search again.
			"nzbFetchFailed": QueueActionRemoveBlocklistAndSearch,
			// Publish failed (FUSE/symlink issue) — retry later, don't abandon.
			"publishFailed": QueueActionDoNothing,
			// Bad source (CAM/TS/etc.) — blocklist release, search for a proper one.
			"badSource": QueueActionRemoveBlocklistAndSearch,
			// Interrupted by process restart — re-dispatch the same release.
			"interruptedByRestart": QueueActionSearchAgain,
		},
		IgnoredPatterns: []string{
			"*.nfo",
			"*.sfv",
			"*sample.mkv",
			"*.iso",
			"*.jpg",
			"*.png",
		},
	}
}

func mergeSettings(base, override Settings) Settings {
	out := base
	if override.DuplicateNzbBehavior != "" {
		out.DuplicateNzbBehavior = override.DuplicateNzbBehavior
	}
	if override.ImportStrategy != "" {
		out.ImportStrategy = override.ImportStrategy
	}
	out.FailNzbWithoutVideo = override.FailNzbWithoutVideo
	if override.ManualUploadCategory != "" {
		out.ManualUploadCategory = override.ManualUploadCategory
	}
	if override.BlocklistTTLDays > 0 {
		out.BlocklistTTLDays = override.BlocklistTTLDays
	}
	if len(override.QueueDecisionActions) > 0 {
		for key, value := range override.QueueDecisionActions {
			if strings.TrimSpace(key) == "" || strings.TrimSpace(string(value)) == "" {
				continue
			}
			out.QueueDecisionActions[key] = value
		}
	}
	if len(override.IgnoredPatterns) > 0 {
		out.IgnoredPatterns = out.IgnoredPatterns[:0]
		for _, pattern := range override.IgnoredPatterns {
			pattern = strings.TrimSpace(pattern)
			if pattern != "" {
				out.IgnoredPatterns = append(out.IgnoredPatterns, pattern)
			}
		}
	}
	return out
}

func Merge(base, override Settings) Settings {
	return mergeSettings(base, override)
}

// DecisionKeyForReason maps a raw queue_items.failure_reason to the
// QueueDecisionActions key used for user-configurable policy lookup.
// Covers both Sonarr/Radarr-compatible keys and Drakkar-native keys.
func DecisionKeyForReason(reason string) string {
	r := strings.ToLower(strings.TrimSpace(reason))
	switch {
	// ── Drakkar-native: article/download failures ─────────────────────────
	case strings.Contains(r, "missing article"),
		strings.Contains(r, "430"),
		strings.Contains(r, "nntp_article_unavailable"):
		return "missingArticles"

	case strings.Contains(r, "status 403"):
		return "nzbFetch403"

	case strings.Contains(r, "status 404"),
		strings.Contains(r, "status 410"),
		strings.Contains(r, "status 401"),
		strings.Contains(r, "status 451"):
		return "nzbFetch4xx"

	case strings.Contains(r, "nzb_fetch"),
		strings.Contains(r, "nzb fetch"),
		strings.Contains(r, "fetch status"):
		return "nzbFetchFailed"

	// ── Drakkar-native: archive / preflight ───────────────────────────────
	case strings.Contains(r, "preflight"):
		return "preflightFailed"

	case strings.Contains(r, "publish_failed"),
		strings.Contains(r, "no publishable"):
		return "publishFailed"

	case strings.Contains(r, "archive_video_not_found"),
		strings.Contains(r, "no eligible"):
		return "noEligibleFiles"

	// archive_* must be after archive_video_not_found
	case strings.Contains(r, "archive_"),
		strings.Contains(r, "archive"):
		return "archiveNeedsExtraction"

	// ── Drakkar-native: search / candidate failures ───────────────────────
	// "all_candidates_wrong_title" must match before generic "wrong_title"
	case strings.Contains(r, "all_candidates_wrong_title"):
		return "allCandidatesWrongTitle"

	case strings.Contains(r, "all_candidates_bad_source"),
		strings.Contains(r, "bad_source"):
		return "badSource"

	// Generic all_candidates after the specific ones
	case strings.Contains(r, "all_candidates"):
		return "allCandidatesRejected"

	case strings.Contains(r, "no_release_found"),
		strings.Contains(r, "no releases"),
		strings.Contains(r, "no_releases"):
		return "noReleaseFound"

	case strings.Contains(r, "interrupted_by_restart"),
		strings.Contains(r, "stale_worker"),
		strings.Contains(r, "too_many_failures"):
		return "interruptedByRestart"

	// ── Sonarr/Radarr-compatible keys ─────────────────────────────────────
	case strings.Contains(r, "sample"):
		if strings.Contains(r, "unable") || strings.Contains(r, "indeterminate") {
			return "unableToDetermineSample"
		}
		return "sample"

	case strings.Contains(r, "no audio"):
		return "noAudioTracks"

	case strings.Contains(r, "already imported"):
		return "episodeAlreadyImported"

	case strings.Contains(r, "not custom format upgrade"):
		return "notCustomFormatUpgrade"

	case strings.Contains(r, "not upgrade for existing episode"):
		return "notEpisodeUpgrade"

	case strings.Contains(r, "not upgrade for existing movie"),
		strings.Contains(r, "metadata_conflict"):
		return "notMovieUpgrade"

	case strings.Contains(r, "invalid season"),
		strings.Contains(r, "invalid episode"):
		return "invalidSeasonEpisode"

	default:
		return ""
	}
}

func ActionForReason(settings Settings, reason string) QueueDecisionAction {
	key := DecisionKeyForReason(reason)
	if key == "" {
		return QueueActionDoNothing
	}
	if action, ok := settings.QueueDecisionActions[key]; ok {
		return action
	}
	return QueueActionDoNothing
}
