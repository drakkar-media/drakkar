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
		QueueDecisionActions: map[string]QueueDecisionAction{
			"grabbedSeriesIdMismatch":      QueueActionDoNothing,
			"grabbedMovieIdMismatch":       QueueActionDoNothing,
			"episodeMissingInRelease":      QueueActionDoNothing,
			"unexpectedEpisodes":           QueueActionDoNothing,
			"notEpisodeUpgrade":            QueueActionRemoveAndBlocklist,
			"notMovieUpgrade":              QueueActionRemoveAndBlocklist,
			"notCustomFormatUpgrade":       QueueActionRemoveAndBlocklist,
			"noEligibleFiles":              QueueActionRemoveBlocklistAndSearch,
			"episodeAlreadyImported":       QueueActionRemove,
			"noAudioTracks":                QueueActionRemoveBlocklistAndSearch,
			"invalidSeasonEpisode":         QueueActionDoNothing,
			"singleEpisodeContainsSeason":  QueueActionDoNothing,
			"unableToDetermineSample":      QueueActionDoNothing,
			"sample":                       QueueActionRemoveBlocklistAndSearch,
			"archiveNeedsExtraction":       QueueActionDoNothing,
			"missingArticles":              QueueActionRemoveBlocklistAndSearch,
		},
		IgnoredPatterns: []string{
			"*.nfo",
			"*.par2",
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

func DecisionKeyForReason(reason string) string {
	r := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case strings.Contains(r, "missing article"), strings.Contains(r, "430"), strings.Contains(r, "nntp_article_unavailable"):
		return "missingArticles"
	case strings.Contains(r, "sample"):
		if strings.Contains(r, "unable") || strings.Contains(r, "indeterminate") {
			return "unableToDetermineSample"
		}
		return "sample"
	case strings.Contains(r, "no audio"):
		return "noAudioTracks"
	case strings.Contains(r, "archive_"), strings.Contains(r, "archive"):
		return "archiveNeedsExtraction"
	case strings.Contains(r, "already imported"):
		return "episodeAlreadyImported"
	case strings.Contains(r, "not custom format upgrade"):
		return "notCustomFormatUpgrade"
	case strings.Contains(r, "not upgrade for existing episode"), strings.Contains(r, "all_candidates_wrong_title"):
		return "notEpisodeUpgrade"
	case strings.Contains(r, "not upgrade for existing movie"), strings.Contains(r, "metadata_conflict"):
		return "notMovieUpgrade"
	case strings.Contains(r, "no eligible"), strings.Contains(r, "no publishable"), strings.Contains(r, "archive_video_not_found"):
		return "noEligibleFiles"
	case strings.Contains(r, "invalid season"), strings.Contains(r, "invalid episode"):
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
