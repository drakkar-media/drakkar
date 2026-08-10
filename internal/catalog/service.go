package catalog

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/drakkar-media/drakkar/internal/database"
	"github.com/drakkar-media/drakkar/internal/tmdb"
)

// TMDBClient defines the subset of TMDB metadata lookups the catalog
// service needs to enrich library items and serve the discover/dashboard
// views.
type TMDBClient interface {
	Enabled() bool
	Search(ctx context.Context, mediaType string, query string) ([]tmdb.MediaSummary, error)
	Trending(ctx context.Context, mediaType string) ([]tmdb.MediaSummary, error)
	TrendingPage(ctx context.Context, mediaType string, page int) (tmdb.ListResult, error)
	MovieDetails(ctx context.Context, tmdbID int64) (tmdb.MovieDetails, error)
	TVDetails(ctx context.Context, tmdbID int64) (tmdb.TVDetails, error)
	TVSeasonNumbers(ctx context.Context, tmdbID int64) ([]int, error)
	TVSeason(ctx context.Context, tmdbID int64, seasonNumber int) (tmdb.TVSeason, error)
}

// Service builds the media catalog views (dashboard, discover, library
// listing/detail, release calendar) served to the frontend, combining local
// library/queue state from the database with optional TMDB metadata.
type Service struct {
	db   *database.DB
	tmdb TMDBClient
}

// NewService creates a catalog Service. tmdbClient may be a client whose
// Enabled() returns false (or nil) when TMDB integration is not configured;
// discover/trending features degrade gracefully in that case while
// library-only views continue to work.
func NewService(db *database.DB, tmdbClient TMDBClient) *Service {
	return &Service{db: db, tmdb: tmdbClient}
}

// MediaCard is the summary representation of a movie or TV show used in
// library listings, search results, and dashboard rails.
type MediaCard struct {
	ID                int64     `json:"id"`
	MediaType         string    `json:"mediaType"`
	Title             string    `json:"title"`
	Year              int       `json:"year"`
	Overview          string    `json:"overview,omitempty"`
	PosterURL         string    `json:"posterUrl,omitempty"`
	BackdropURL       string    `json:"backdropUrl,omitempty"`
	Available         bool      `json:"available"`
	QueueState        string    `json:"queueState"`
	FailureReason     string    `json:"failureReason"`
	RequestedAt       time.Time `json:"requestedAt"`
	SelectedReleaseID *int64    `json:"selectedReleaseId,omitempty"`
	TMDBID            int64     `json:"tmdbId,omitempty"`
	TVDBID            int64     `json:"tvdbId,omitempty"`
	IMDbID            string    `json:"imdbId,omitempty"`
	AvailableCount    int       `json:"availableCount"`
	MissingCount      int       `json:"missingCount"`
	SeasonNumber      int       `json:"seasonNumber,omitempty"`
	EpisodeNumber     int       `json:"episodeNumber,omitempty"`
	TVShowID          int64     `json:"-"`
}

// DashboardHome is the payload for the home/dashboard view: a hero item plus
// recently-added library content and TMDB trending rails.
type DashboardHome struct {
	Hero           *MediaCard  `json:"hero,omitempty"`
	RecentlyAdded  []MediaCard `json:"recentlyAdded"`
	TrendingMovies []MediaCard `json:"trendingMovies"`
	TrendingTV     []MediaCard `json:"trendingTv"`
}

// DiscoverSearchResult holds TMDB search results split by media type.
type DiscoverSearchResult struct {
	Movies []MediaCard `json:"movies"`
	TV     []MediaCard `json:"tv"`
}

// DiscoverListResult is one page of a TMDB trending/browse listing.
type DiscoverListResult struct {
	Page       int         `json:"page"`
	TotalPages int         `json:"totalPages"`
	Items      []MediaCard `json:"items"`
}

// DiscoverDetails is the full TMDB detail view for a single movie or TV
// show, used by the discover/details page rather than the library.
type DiscoverDetails struct {
	MediaType           string      `json:"mediaType"`
	Title               string      `json:"title"`
	Year                int         `json:"year"`
	Overview            string      `json:"overview,omitempty"`
	Tagline             string      `json:"tagline,omitempty"`
	PosterURL           string      `json:"posterUrl,omitempty"`
	BackdropURL         string      `json:"backdropUrl,omitempty"`
	TMDBID              int64       `json:"tmdbId,omitempty"`
	IMDbID              string      `json:"imdbId,omitempty"`
	OriginalLanguage    string      `json:"originalLanguage,omitempty"`
	RuntimeMinutes      int         `json:"runtimeMinutes,omitempty"`
	Status              string      `json:"status,omitempty"`
	Network             string      `json:"network,omitempty"`
	NumberOfSeasons     int         `json:"numberOfSeasons,omitempty"`
	NumberOfEpisodes    int         `json:"numberOfEpisodes,omitempty"`
	VoteAverage         float64     `json:"voteAverage,omitempty"`
	VoteCount           int         `json:"voteCount,omitempty"`
	Budget              int64       `json:"budget,omitempty"`
	Revenue             int64       `json:"revenue,omitempty"`
	Genres              []string    `json:"genres,omitempty"`
	ProductionCompanies []string    `json:"productionCompanies,omitempty"`
	Cast                []CastCard  `json:"cast,omitempty"`
	Recommendations     []MediaCard `json:"recommendations,omitempty"`
	Similar             []MediaCard `json:"similar,omitempty"`
}

// CastCard summarizes a single cast member for display in DiscoverDetails.
type CastCard struct {
	ID         int64  `json:"id,omitempty"`
	Name       string `json:"name"`
	Character  string `json:"character,omitempty"`
	ProfileURL string `json:"profileUrl,omitempty"`
}

// DiscoverLookup identifies a title to fetch DiscoverDetails for. TMDBID is
// used directly when known; otherwise resolveTMDBID matches by Title/Year.
type DiscoverLookup struct {
	MediaType string
	Title     string
	Year      int
	TMDBID    int64
	IMDbID    string
}

// LibraryDetail is the full detail view for a single library item,
// including per-season/episode breakdown for TV shows.
type LibraryDetail struct {
	ID                int64          `json:"id"`
	MediaType         string         `json:"mediaType"`
	Title             string         `json:"title"`
	Year              int            `json:"year"`
	Overview          string         `json:"overview,omitempty"`
	PosterURL         string         `json:"posterUrl,omitempty"`
	BackdropURL       string         `json:"backdropUrl,omitempty"`
	Available         bool           `json:"available"`
	QueueState        string         `json:"queueState"`
	FailureReason     string         `json:"failureReason"`
	SelectedReleaseID *int64         `json:"selectedReleaseId,omitempty"`
	TMDBID            int64          `json:"tmdbId,omitempty"`
	TVDBID            int64          `json:"tvdbId,omitempty"`
	IMDbID            string         `json:"imdbId,omitempty"`
	AvailableCount    int            `json:"availableCount"`
	MissingCount      int            `json:"missingCount"`
	Seasons           []SeasonDetail `json:"seasons,omitempty"`
	TVShowID          int64          `json:"tvShowId,omitempty"`
	MonitoringMode    string         `json:"monitoringMode,omitempty"`
	CurrentFile       *CurrentFile   `json:"currentFile,omitempty"`
}

// CurrentFile describes the actual media file currently backing this
// library item's selected release -- what drakkar is really serving right
// now, as opposed to a log of past grab attempts.
type CurrentFile struct {
	FileName          string   `json:"fileName"`
	FileSizeBytes     int64    `json:"fileSizeBytes"`
	ReleaseTitle      string   `json:"releaseTitle"`
	IndexerName       string   `json:"indexerName,omitempty"`
	Resolution        string   `json:"resolution,omitempty"`
	Score             int      `json:"score"`
	SubtitleLanguages []string `json:"subtitleLanguages,omitempty"`
}

// SeasonDetail describes one TV season within LibraryDetail, including its
// episode-level availability breakdown.
type SeasonDetail struct {
	SeasonNumber   int             `json:"seasonNumber"`
	Name           string          `json:"name"`
	EpisodeCount   int             `json:"episodeCount"`
	AvailableCount int             `json:"availableCount"`
	MissingCount   int             `json:"missingCount"`
	Episodes       []EpisodeDetail `json:"episodes"`
}

// EpisodeDetail describes a single episode's availability status within a
// SeasonDetail.
type EpisodeDetail struct {
	SeasonNumber      int      `json:"seasonNumber"`
	EpisodeNumber     int      `json:"episodeNumber"`
	Title             string   `json:"title"`
	Status            string   `json:"status"`
	LibraryItemID     *int64   `json:"libraryItemId,omitempty"`
	SubtitleLanguages []string `json:"subtitleLanguages,omitempty"`
}

type showEpisodeRow struct {
	SeasonNumber      int
	EpisodeNumber     int
	Title             string
	Available         bool
	LibraryItemID     int64
	SubtitleLanguages []string
}

// ListLibraryCards returns every movie and TV show in the local library as
// MediaCards, newest requested first.
func (s *Service) ListLibraryCards(ctx context.Context) ([]MediaCard, error) {
	movies, err := s.movieCards(ctx)
	if err != nil {
		return nil, err
	}
	tv, err := s.tvCards(ctx)
	if err != nil {
		return nil, err
	}
	cards := append(movies, tv...)
	sort.Slice(cards, func(i, j int) bool {
		if cards[i].RequestedAt.Equal(cards[j].RequestedAt) {
			return cards[i].ID > cards[j].ID
		}
		return cards[i].RequestedAt.After(cards[j].RequestedAt)
	})
	return cards, nil
}

// DiscoverSearch searches TMDB for movies and TV shows matching query.
//
// Errors:
//   - returns an error when TMDB integration is not configured/enabled.
func (s *Service) DiscoverSearch(ctx context.Context, query string) (DiscoverSearchResult, error) {
	if s.tmdb == nil || !s.tmdb.Enabled() {
		return DiscoverSearchResult{}, fmt.Errorf("tmdb unavailable")
	}
	movies, err := s.tmdb.Search(ctx, "movie", query)
	if err != nil {
		return DiscoverSearchResult{}, err
	}
	tv, err := s.tmdb.Search(ctx, "tv", query)
	if err != nil {
		return DiscoverSearchResult{}, err
	}
	return DiscoverSearchResult{
		Movies: mediaCardsFromSummaries(movies),
		TV:     mediaCardsFromSummaries(tv),
	}, nil
}

// DiscoverList returns one page of TMDB's trending listing for mediaType
// ("movie" or "tv").
func (s *Service) DiscoverList(ctx context.Context, mediaType string, page int) (DiscoverListResult, error) {
	if s.tmdb == nil || !s.tmdb.Enabled() {
		return DiscoverListResult{}, fmt.Errorf("tmdb unavailable")
	}
	result, err := s.tmdb.TrendingPage(ctx, mediaType, page)
	if err != nil {
		return DiscoverListResult{}, err
	}
	return DiscoverListResult{
		Page:       result.Page,
		TotalPages: result.TotalPages,
		Items:      mediaCardsFromSummaries(result.Items),
	}, nil
}

// DiscoverDetails fetches the full TMDB detail view for the title
// identified by lookup, resolving a TMDB ID from Title/Year first when one
// is not already known.
func (s *Service) DiscoverDetails(ctx context.Context, lookup DiscoverLookup) (DiscoverDetails, error) {
	if s.tmdb == nil || !s.tmdb.Enabled() {
		return DiscoverDetails{}, fmt.Errorf("tmdb unavailable")
	}
	switch lookup.MediaType {
	case "tv":
		tmdbID, err := s.resolveTMDBID(ctx, lookup)
		if err != nil {
			return DiscoverDetails{}, err
		}
		detail, err := s.tmdb.TVDetails(ctx, tmdbID)
		if err != nil {
			return DiscoverDetails{}, err
		}
		return DiscoverDetails{
			MediaType:           "tv",
			Title:               detail.Name,
			Year:                detail.Year,
			Overview:            detail.Overview,
			Tagline:             detail.Tagline,
			PosterURL:           detail.PosterURL,
			BackdropURL:         detail.BackdropURL,
			TMDBID:              tmdbID,
			IMDbID:              detail.IMDbID,
			OriginalLanguage:    detail.OriginalLanguage,
			RuntimeMinutes:      detail.EpisodeRunTime,
			Status:              detail.Status,
			Network:             detail.Network,
			NumberOfSeasons:     detail.NumberOfSeasons,
			NumberOfEpisodes:    detail.NumberOfEpisodes,
			VoteAverage:         detail.VoteAverage,
			VoteCount:           detail.VoteCount,
			Genres:              detail.Genres,
			ProductionCompanies: detail.ProductionCompanies,
			Cast:                castCards(detail.Cast),
			Recommendations:     mediaCardsFromSummaries(detail.Recommendations),
			Similar:             mediaCardsFromSummaries(detail.Similar),
		}, nil
	default:
		tmdbID, err := s.resolveTMDBID(ctx, lookup)
		if err != nil {
			return DiscoverDetails{}, err
		}
		detail, err := s.tmdb.MovieDetails(ctx, tmdbID)
		if err != nil {
			return DiscoverDetails{}, err
		}
		return DiscoverDetails{
			MediaType:           "movie",
			Title:               detail.Title,
			Year:                detail.Year,
			Overview:            detail.Overview,
			Tagline:             detail.Tagline,
			PosterURL:           detail.PosterURL,
			BackdropURL:         detail.BackdropURL,
			TMDBID:              tmdbID,
			IMDbID:              detail.IMDbID,
			OriginalLanguage:    detail.OriginalLanguage,
			RuntimeMinutes:      detail.RuntimeMinutes,
			VoteAverage:         detail.VoteAverage,
			VoteCount:           detail.VoteCount,
			Budget:              detail.Budget,
			Revenue:             detail.Revenue,
			Genres:              detail.Genres,
			ProductionCompanies: detail.ProductionCompanies,
			Cast:                castCards(detail.Cast),
			Recommendations:     mediaCardsFromSummaries(detail.Recommendations),
			Similar:             mediaCardsFromSummaries(detail.Similar),
		}, nil
	}
}

// SearchLibraryCards searches the local library by title, year, IMDb ID, or
// TMDB ID. Matches are ranked with available items first, then exact title
// matches, then most recently requested, and capped at 60 results.
func (s *Service) SearchLibraryCards(ctx context.Context, query string) ([]MediaCard, error) {
	cards, err := s.ListLibraryCards(ctx)
	if err != nil {
		return nil, err
	}
	q := normalizeSearch(query)
	if q == "" {
		return []MediaCard{}, nil
	}
	matches := make([]MediaCard, 0, 32)
	for _, card := range cards {
		hay := normalizeSearch(card.Title)
		if card.Year > 0 {
			hay += " " + strconv.Itoa(card.Year)
		}
		if card.IMDbID != "" {
			hay += " " + normalizeSearch(card.IMDbID)
		}
		if hay == "" {
			continue
		}
		if strings.Contains(hay, q) || (card.TMDBID > 0 && strconv.FormatInt(card.TMDBID, 10) == q) {
			matches = append(matches, card)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Available != matches[j].Available {
			return matches[i].Available
		}
		if strings.EqualFold(matches[i].Title, query) != strings.EqualFold(matches[j].Title, query) {
			return strings.EqualFold(matches[i].Title, query)
		}
		if matches[i].RequestedAt.Equal(matches[j].RequestedAt) {
			return matches[i].ID > matches[j].ID
		}
		return matches[i].RequestedAt.After(matches[j].RequestedAt)
	})
	if len(matches) > 60 {
		matches = matches[:60]
	}
	return matches, nil
}

func (s *Service) movieCards(ctx context.Context) ([]MediaCard, error) {
	rows, err := s.db.SQL.QueryContext(ctx, `
		select
			li.id,
			coalesce(m.title, li.title, ''),
			li.available,
			li.requested_at,
			coalesce(q.state, ''),
			coalesce(q.failure_reason, ''),
			q.selected_release_id,
			coalesce(m.release_year, 0),
			coalesce(m.tmdb_id, 0),
			coalesce(m.imdb_id, ''),
			coalesce(m.overview, ''),
			coalesce(m.poster_url, ''),
			coalesce(m.backdrop_url, '')
		from library_items li
		left join lateral (
			select qi.state, qi.failure_reason, qi.selected_release_id
			from queue_items qi
			where qi.library_item_id = li.id
			order by qi.id desc
			limit 1
		) q on true
		join movies m on m.id = li.movie_id
		order by li.requested_at desc, li.id desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MediaCard
	for rows.Next() {
		var (
			item     MediaCard
			selected sql.NullInt64
		)
		item.MediaType = "movie"
		if err := rows.Scan(
			&item.ID,
			&item.Title,
			&item.Available,
			&item.RequestedAt,
			&item.QueueState,
			&item.FailureReason,
			&selected,
			&item.Year,
			&item.TMDBID,
			&item.IMDbID,
			&item.Overview,
			&item.PosterURL,
			&item.BackdropURL,
		); err != nil {
			return nil, err
		}
		if selected.Valid {
			value := selected.Int64
			item.SelectedReleaseID = &value
		}
		if item.Available {
			item.AvailableCount = 1
		} else {
			item.MissingCount = 1
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// tvCards returns one MediaCard per TV show (not per episode), aggregating
// across all of a show's episode-level library_items and queue_items rows.
// availableEpisodes/totalEpisodes drive the "x/y episodes" badge, preferring
// TMDB's number_of_episodes as the total when known since our own tracked
// episode rows may be incomplete; queueRank picks the single most relevant
// queue state across all of a show's episodes (an active download outranks a
// failure, which outranks a pending request).
func (s *Service) tvCards(ctx context.Context) ([]MediaCard, error) {
	rows, err := s.db.SQL.QueryContext(ctx, `
		select
			min(li.id) as library_item_id,
			tv.id as tv_show_id,
			coalesce(tv.title, ''),
			max(li.requested_at) as requested_at,
			coalesce(tv.release_year, 0),
			coalesce(tv.tmdb_id, 0),
			coalesce(tv.tvdb_id, 0),
			coalesce(tv.imdb_id, ''),
			coalesce(tv.overview, ''),
			coalesce(tv.poster_url, ''),
			coalesce(tv.backdrop_url, ''),
			-- available_episodes: specific episodes we have in the library.
			-- total_episodes: use TMDB's number_of_episodes from tv_shows (the real total),
			-- falling back to our tracked count when TMDB count is not available.
			count(distinct case when li.available and e.season_number > 0 and e.episode_number > 0 then li.id end) as available_episodes,
			greatest(
			    count(distinct case when e.season_number > 0 and e.episode_number > 0 then li.id end),
			    coalesce(max(tv.number_of_episodes), 0)
			) as total_episodes,
			max(q.selected_release_id) as selected_release_id,
			max(
				case
					when q.state in ('selected', 'fetching_nzb', 'indexing', 'preflight', 'publishing') then 3
					when q.state = 'failed' then 2
					when q.state = 'requested' then 1
					else 0
				end
			) as queue_rank,
			max(coalesce(q.failure_reason, '')) as failure_reason
		from library_items li
		join episodes e on e.id = li.episode_id
		join tv_shows tv on tv.id = e.tv_show_id
		left join queue_items q on q.library_item_id = li.id
		group by tv.id, tv.title, tv.release_year, tv.tmdb_id, tv.tvdb_id, tv.imdb_id, tv.overview, tv.poster_url, tv.backdrop_url
		order by max(li.requested_at) desc, min(li.id) desc`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MediaCard
	for rows.Next() {
		var (
			item              MediaCard
			selected          sql.NullInt64
			totalEpisodes     int
			availableEpisodes int
			queueRank         int
		)
		item.MediaType = "tv"
		if err := rows.Scan(
			&item.ID,
			&item.TVShowID,
			&item.Title,
			&item.RequestedAt,
			&item.Year,
			&item.TMDBID,
			&item.TVDBID,
			&item.IMDbID,
			&item.Overview,
			&item.PosterURL,
			&item.BackdropURL,
			&availableEpisodes,
			&totalEpisodes,
			&selected,
			&queueRank,
			&item.FailureReason,
		); err != nil {
			return nil, err
		}
		if selected.Valid {
			value := selected.Int64
			item.SelectedReleaseID = &value
		}
		item.AvailableCount = availableEpisodes
		item.MissingCount = totalEpisodes - availableEpisodes
		if totalEpisodes > 0 {
			// We have specific season/episode items — use their count
			item.Available = availableEpisodes == totalEpisodes
		} else {
			// Only a whole-show placeholder exists — fall back to its available flag
			item.Available = queueRank == 0 && item.SelectedReleaseID != nil
			// Keep counts at 0 so the badge shows "Pack" not "0/0 ep"
		}
		item.QueueState = queueStateFromRank(queueRank, item.Available)
		out = append(out, item)
	}
	return out, rows.Err()
}

// trendingLibraryState looks up local library state -- id, availability,
// queue state, counts -- for whichever of the given TMDB ids are already
// tracked locally, keyed by TMDB id. Trending() itself returns pure TMDB
// metadata with no local cross-reference, so without this every trending
// card always reports ID 0 (and therefore always shows the "+ request"
// button) regardless of whether the title was already requested/is
// downloading/is fully available. Returns an empty map (never an error) if
// tmdbIDs is empty.
func (s *Service) trendingLibraryState(ctx context.Context, mediaType string, tmdbIDs []int64) (map[int64]MediaCard, error) {
	out := make(map[int64]MediaCard, len(tmdbIDs))
	if len(tmdbIDs) == 0 {
		return out, nil
	}
	if mediaType == "movie" {
		rows, err := s.db.SQL.QueryContext(ctx, `
			select
				li.id,
				m.tmdb_id,
				li.available,
				li.requested_at,
				coalesce(q.state, ''),
				coalesce(q.failure_reason, ''),
				q.selected_release_id
			from library_items li
			left join lateral (
				select qi.state, qi.failure_reason, qi.selected_release_id
				from queue_items qi
				where qi.library_item_id = li.id
				order by qi.id desc
				limit 1
			) q on true
			join movies m on m.id = li.movie_id
			where m.tmdb_id = any($1)`, tmdbIDs)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				item     MediaCard
				tmdbID   int64
				selected sql.NullInt64
			)
			if err := rows.Scan(&item.ID, &tmdbID, &item.Available, &item.RequestedAt, &item.QueueState, &item.FailureReason, &selected); err != nil {
				return nil, err
			}
			if selected.Valid {
				value := selected.Int64
				item.SelectedReleaseID = &value
			}
			if item.Available {
				item.AvailableCount = 1
			} else {
				item.MissingCount = 1
			}
			out[tmdbID] = item
		}
		return out, rows.Err()
	}

	// tv: aggregate across a show's episode-level rows, same shape as tvCards.
	rows, err := s.db.SQL.QueryContext(ctx, `
		select
			min(li.id),
			tv.tmdb_id,
			max(li.requested_at),
			count(distinct case when li.available and e.season_number > 0 and e.episode_number > 0 then li.id end),
			greatest(
			    count(distinct case when e.season_number > 0 and e.episode_number > 0 then li.id end),
			    coalesce(max(tv.number_of_episodes), 0)
			),
			max(q.selected_release_id),
			max(
				case
					when q.state in ('selected', 'fetching_nzb', 'indexing', 'preflight', 'publishing') then 3
					when q.state = 'failed' then 2
					when q.state = 'requested' then 1
					else 0
				end
			),
			max(coalesce(q.failure_reason, ''))
		from library_items li
		join episodes e on e.id = li.episode_id
		join tv_shows tv on tv.id = e.tv_show_id
		left join queue_items q on q.library_item_id = li.id
		where tv.tmdb_id = any($1)
		group by tv.id, tv.tmdb_id`, tmdbIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			item              MediaCard
			tmdbID            int64
			selected          sql.NullInt64
			availableEpisodes int
			totalEpisodes     int
			queueRank         int
		)
		if err := rows.Scan(&item.ID, &tmdbID, &item.RequestedAt, &availableEpisodes, &totalEpisodes, &selected, &queueRank, &item.FailureReason); err != nil {
			return nil, err
		}
		if selected.Valid {
			value := selected.Int64
			item.SelectedReleaseID = &value
		}
		item.AvailableCount = availableEpisodes
		item.MissingCount = totalEpisodes - availableEpisodes
		if totalEpisodes > 0 {
			item.Available = availableEpisodes == totalEpisodes
		} else {
			item.Available = queueRank == 0 && item.SelectedReleaseID != nil
		}
		item.QueueState = queueStateFromRank(queueRank, item.Available)
		out[tmdbID] = item
	}
	return out, rows.Err()
}

// mergeTrendingLibraryState overlays local library state onto TMDB-sourced
// trending cards in place, for whichever cards have a local match in state
// (keyed by TMDBID). Cards with no match are left untouched (ID stays 0,
// meaning "not in library yet" -- the "+ request" button's actual signal).
func mergeTrendingLibraryState(cards []MediaCard, state map[int64]MediaCard) {
	for i := range cards {
		local, ok := state[cards[i].TMDBID]
		if !ok {
			continue
		}
		cards[i].ID = local.ID
		cards[i].Available = local.Available
		cards[i].QueueState = local.QueueState
		cards[i].FailureReason = local.FailureReason
		cards[i].RequestedAt = local.RequestedAt
		cards[i].SelectedReleaseID = local.SelectedReleaseID
		cards[i].AvailableCount = local.AvailableCount
		cards[i].MissingCount = local.MissingCount
	}
}

// tmdbIDsOf collects the TMDB ids of cards, for use as a trendingLibraryState
// batch-lookup filter.
func tmdbIDsOf(cards []MediaCard) []int64 {
	ids := make([]int64, 0, len(cards))
	for _, c := range cards {
		if c.TMDBID > 0 {
			ids = append(ids, c.TMDBID)
		}
	}
	return ids
}

// Dashboard builds the home page payload: recently added library items plus
// TMDB trending rails when TMDB is enabled. The hero item is chosen from
// recently-added content first, falling back to trending, then to any
// library card, so the dashboard always has a hero when any content exists.
func (s *Service) Dashboard(ctx context.Context) (DashboardHome, error) {
	cards, err := s.ListLibraryCards(ctx)
	if err != nil {
		return DashboardHome{}, err
	}
	recent, err := s.recentlyAdded(ctx)
	if err != nil {
		return DashboardHome{}, err
	}
	out := DashboardHome{RecentlyAdded: recent}
	if len(recent) > 0 {
		hero := recent[0]
		out.Hero = &hero
	}
	if s.tmdb != nil && s.tmdb.Enabled() {
		if movies, err := s.tmdb.Trending(ctx, "movie"); err == nil {
			out.TrendingMovies = summariesToCards(movies)
			if state, err := s.trendingLibraryState(ctx, "movie", tmdbIDsOf(out.TrendingMovies)); err == nil {
				mergeTrendingLibraryState(out.TrendingMovies, state)
			}
			if out.Hero == nil && len(out.TrendingMovies) > 0 {
				hero := out.TrendingMovies[0]
				out.Hero = &hero
			}
		}
		if tv, err := s.tmdb.Trending(ctx, "tv"); err == nil {
			out.TrendingTV = summariesToCards(tv)
			if state, err := s.trendingLibraryState(ctx, "tv", tmdbIDsOf(out.TrendingTV)); err == nil {
				mergeTrendingLibraryState(out.TrendingTV, state)
			}
			if out.Hero == nil && len(out.TrendingTV) > 0 {
				hero := out.TrendingTV[0]
				out.Hero = &hero
			}
		}
	}
	if out.Hero == nil && len(cards) > 0 {
		hero := cards[0]
		out.Hero = &hero
	}
	return out, nil
}

// recentlyAdded returns the 40 most recently published library items for the
// dashboard's "recently added" rail. The source union covers two distinct
// ways an item becomes available: a symlink_publications row (movies and
// individually-published episodes) or, for season-pack downloads that never
// get an individual symlink, the library_items row itself. seen deduplicates
// a library item that could otherwise surface from both branches.
//
// Every row here already satisfies li.available = true. The frontend's
// itemStatus() deliberately lets an in-progress queue state override
// availability elsewhere in the app (e.g. the main Library grid, where
// "actively re-checking for an upgrade" is useful to show) -- but for this
// rail specifically, that reads as a real bug: a routine background upgrade
// check queued against an already-available item (state 'selected',
// 'requested', 'searching', 'ranking', or a failed upgrade attempt) made a
// just-added, fully watchable title display as "Missing"/"active" in the
// one section whose entire point is "this is ready to watch." Only an
// actually-in-flight fetch (fetching_nzb/indexing/preflight/publishing)
// reflects something genuinely happening to this item right now, so only
// those states are passed through here; anything else is blanked so the
// frontend falls back to its availability-based status instead.
func (s *Service) recentlyAdded(ctx context.Context) ([]MediaCard, error) {
	rows, err := s.db.SQL.QueryContext(ctx, `
		select
			li.id,
			li.media_type,
			coalesce(m.title, tv.title, li.title, ''),
			li.available,
			case when q.state in ('fetching_nzb', 'indexing', 'preflight', 'publishing') then q.state else '' end,
			coalesce(q.failure_reason, ''),
			q.selected_release_id,
			coalesce(m.release_year, 0),
			coalesce(m.tmdb_id, 0),
			coalesce(m.imdb_id, ''),
			coalesce(m.overview, ''),
			coalesce(m.poster_url, ''),
			coalesce(m.backdrop_url, ''),
			coalesce(tv.release_year, 0),
			coalesce(tv.tmdb_id, 0),
			coalesce(tv.tvdb_id, 0),
			coalesce(tv.imdb_id, ''),
			coalesce(tv.overview, ''),
			coalesce(tv.poster_url, ''),
			coalesce(tv.backdrop_url, ''),
			src.created_at,
			coalesce(e.season_number, 0),
			coalesce(e.episode_number, 0)
		from (
		    -- Movies and specific episode symlinks (exclude S00E00 whole-show placeholders)
		    select sp2.created_at, sp2.library_item_id
		    from symlink_publications sp2
		    join library_items li2 on li2.id = sp2.library_item_id
		    left join episodes e2 on e2.id = li2.episode_id
		    where e2.id is null                                              -- movie
		       or (e2.season_number > 0 and e2.episode_number > 0)          -- specific episode symlink
		    union all
		    -- Per-episode items from season packs that have no individual symlink
		    select li3.requested_at, li3.id
		    from library_items li3
		    join episodes e3 on e3.id = li3.episode_id
		    where li3.available = true
		      and e3.season_number > 0 and e3.episode_number > 0
		      and not exists (select 1 from symlink_publications sp3 where sp3.library_item_id = li3.id)
		) src
		join library_items li on li.id = src.library_item_id
		left join lateral (
			select qi.state, qi.failure_reason, qi.selected_release_id
			from queue_items qi
			where qi.library_item_id = li.id
			order by qi.id desc
			limit 1
		) q on true
		left join movies m on m.id = li.movie_id
		left join episodes e on e.id = li.episode_id
		left join tv_shows tv on tv.id = e.tv_show_id
		where li.available = true
		order by src.created_at desc
		limit 40`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []MediaCard
	seen := map[int64]struct{}{}
	for rows.Next() {
		var (
			item          MediaCard
			selected      sql.NullInt64
			movieYear     int
			movieTMDBID   int64
			movieIMDbID   string
			movieOverview string
			moviePoster   string
			movieBackdrop string
			showYear      int
			showTMDBID    int64
			showTVDBID    int64
			showIMDbID    string
			showOverview  string
			showPoster    string
			showBackdrop  string
			createdAt     time.Time
		)
		if err := rows.Scan(
			&item.ID,
			&item.MediaType,
			&item.Title,
			&item.Available,
			&item.QueueState,
			&item.FailureReason,
			&selected,
			&movieYear,
			&movieTMDBID,
			&movieIMDbID,
			&movieOverview,
			&moviePoster,
			&movieBackdrop,
			&showYear,
			&showTMDBID,
			&showTVDBID,
			&showIMDbID,
			&showOverview,
			&showPoster,
			&showBackdrop,
			&createdAt,
			&item.SeasonNumber,
			&item.EpisodeNumber,
		); err != nil {
			return nil, err
		}
		if _, ok := seen[item.ID]; ok {
			continue
		}
		seen[item.ID] = struct{}{}
		if selected.Valid {
			value := selected.Int64
			item.SelectedReleaseID = &value
		}
		item.RequestedAt = createdAt
		if item.MediaType == "movie" {
			item.Year = movieYear
			item.TMDBID = movieTMDBID
			item.IMDbID = movieIMDbID
			item.Overview = movieOverview
			item.PosterURL = moviePoster
			item.BackdropURL = movieBackdrop
		} else {
			item.Year = showYear
			item.TMDBID = showTMDBID
			item.TVDBID = showTVDBID
			item.IMDbID = showIMDbID
			item.Overview = showOverview
			item.PosterURL = showPoster
			item.BackdropURL = showBackdrop
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// LibraryDetail returns the full detail view for a single library item. For
// TV shows this also builds the per-season/episode breakdown via
// buildTVSeasons.
// GetCurrentFile returns the actual media file currently backing
// libraryItemID's selected release, along with which subtitle languages it
// already has. Works for any library item -- a movie, or a single episode
// -- since each has its own selected_releases/virtual_files rows
// independent of any show-level aggregate. Returns (nil, nil) when the item
// has no selected release yet.
func (s *Service) GetCurrentFile(ctx context.Context, libraryItemID int64) (*CurrentFile, error) {
	current, found, err := s.db.GetCurrentFileDetail(ctx, libraryItemID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, nil
	}
	languagesByItem, err := s.db.ListSubtitleLanguagesByLibraryItemIDs(ctx, []int64{libraryItemID})
	if err != nil {
		return nil, err
	}
	return &CurrentFile{
		FileName:          current.FileName,
		FileSizeBytes:     current.FileSizeBytes,
		ReleaseTitle:      current.ReleaseTitle,
		IndexerName:       current.IndexerName,
		Resolution:        current.Resolution,
		Score:             current.Score,
		SubtitleLanguages: languagesByItem[libraryItemID],
	}, nil
}

func (s *Service) LibraryDetail(ctx context.Context, libraryItemID int64) (LibraryDetail, error) {
	var (
		detail         LibraryDetail
		selected       sql.NullInt64
		movieYear      int
		movieTMDBID    int64
		movieIMDbID    string
		movieOverview  string
		moviePoster    string
		movieBackdrop  string
		showYear       int
		showTMDBID     int64
		showTVDBID     int64
		showIMDbID     string
		showOverview   string
		showPoster     string
		showBackdrop   string
		tvShowID       int64
		monitoringMode string
	)
	err := s.db.SQL.QueryRowContext(ctx, `
		select
			li.id,
			li.media_type,
			coalesce(m.title, tv.title, li.title, ''),
			li.available,
			coalesce(q.state, ''),
			coalesce(q.failure_reason, ''),
			q.selected_release_id,
			coalesce(m.release_year, 0),
			coalesce(m.tmdb_id, 0),
			coalesce(m.imdb_id, ''),
			coalesce(m.overview, ''),
			coalesce(m.poster_url, ''),
			coalesce(m.backdrop_url, ''),
			coalesce(tv.release_year, 0),
			coalesce(tv.tmdb_id, 0),
			coalesce(tv.tvdb_id, 0),
			coalesce(tv.imdb_id, ''),
			coalesce(tv.overview, ''),
			coalesce(tv.poster_url, ''),
			coalesce(tv.backdrop_url, ''),
			coalesce(e.tv_show_id, 0),
			coalesce(tv.monitoring_mode, 'all')
		from library_items li
		left join lateral (
			select qi.state, qi.failure_reason, qi.selected_release_id
			from queue_items qi
			where qi.library_item_id = li.id
			order by qi.id desc
			limit 1
		) q on true
		left join movies m on m.id = li.movie_id
		left join episodes e on e.id = li.episode_id
		left join tv_shows tv on tv.id = e.tv_show_id
		where li.id = $1`, libraryItemID,
	).Scan(
		&detail.ID,
		&detail.MediaType,
		&detail.Title,
		&detail.Available,
		&detail.QueueState,
		&detail.FailureReason,
		&selected,
		&movieYear,
		&movieTMDBID,
		&movieIMDbID,
		&movieOverview,
		&moviePoster,
		&movieBackdrop,
		&showYear,
		&showTMDBID,
		&showTVDBID,
		&showIMDbID,
		&showOverview,
		&showPoster,
		&showBackdrop,
		&tvShowID,
		&monitoringMode,
	)
	if err != nil {
		return LibraryDetail{}, err
	}
	if selected.Valid {
		value := selected.Int64
		detail.SelectedReleaseID = &value
	}
	currentFile, err := s.GetCurrentFile(ctx, libraryItemID)
	if err != nil {
		return LibraryDetail{}, err
	}
	detail.CurrentFile = currentFile
	if detail.MediaType == "movie" {
		detail.Year = movieYear
		detail.TMDBID = movieTMDBID
		detail.IMDbID = movieIMDbID
		detail.Overview = movieOverview
		detail.PosterURL = moviePoster
		detail.BackdropURL = movieBackdrop
		if detail.Available {
			detail.AvailableCount = 1
		} else {
			detail.MissingCount = 1
		}
		return detail, nil
	}

	detail.Year = showYear
	detail.TMDBID = showTMDBID
	detail.TVDBID = showTVDBID
	detail.IMDbID = showIMDbID
	detail.Overview = showOverview
	detail.PosterURL = showPoster
	detail.BackdropURL = showBackdrop
	detail.TVShowID = tvShowID
	detail.MonitoringMode = monitoringMode

	seasons, err := s.buildTVSeasons(ctx, detail)
	if err != nil {
		return LibraryDetail{}, err
	}
	detail.Seasons = seasons
	for _, season := range seasons {
		detail.AvailableCount += season.AvailableCount
		detail.MissingCount += season.MissingCount
	}
	detail.Available = detail.MissingCount == 0 && detail.AvailableCount > 0
	if detail.Available {
		detail.QueueState = string(database.QueueAvailable)
	}
	return detail, nil
}

func summariesToCards(items []tmdb.MediaSummary) []MediaCard {
	out := make([]MediaCard, 0, len(items))
	for _, item := range items {
		out = append(out, MediaCard{
			Title:       item.Title,
			Year:        item.Year,
			Overview:    item.Overview,
			PosterURL:   item.PosterURL,
			BackdropURL: item.BackdropURL,
			TMDBID:      item.TMDBID,
			MediaType:   item.MediaType,
		})
	}
	return out
}

// buildTVSeasons builds the per-season/episode breakdown for LibraryDetail.
// It merges local availability (showEpisodes) with episodes implied by the
// currently selected release's title/filename (selectedEpisodes), so an
// episode that is mid-download but has no library_items row yet still shows
// up rather than appearing as entirely missing. When TMDB is unavailable, not
// configured, or returns no season data, it falls back to a season list
// derived purely from local rows via fallbackTVSeasonsFromRows.
func (s *Service) buildTVSeasons(ctx context.Context, detail LibraryDetail) ([]SeasonDetail, error) {
	rows, err := s.showEpisodes(ctx, detail.TVShowID)
	if err != nil {
		return nil, err
	}
	available := make(map[string]showEpisodeRow, len(rows))
	for _, item := range rows {
		available[episodeKey(item.SeasonNumber, item.EpisodeNumber)] = item
	}
	for key := range s.selectedEpisodes(ctx, detail) {
		if _, ok := available[key]; ok {
			continue
		}
		parts := strings.Split(key, ":")
		if len(parts) != 2 {
			continue
		}
		season, _ := strconv.Atoi(parts[0])
		episode, _ := strconv.Atoi(parts[1])
		available[key] = showEpisodeRow{
			SeasonNumber:  season,
			EpisodeNumber: episode,
			Available:     false,
		}
	}
	if s.tmdb == nil || !s.tmdb.Enabled() || detail.TMDBID <= 0 {
		return fallbackTVSeasonsFromRows(rows), nil
	}
	seasonNumbers, err := s.tmdb.TVSeasonNumbers(ctx, detail.TMDBID)
	if err != nil || len(seasonNumbers) == 0 {
		return fallbackTVSeasonsFromRows(rows), nil
	}

	var out []SeasonDetail
	for _, seasonNumber := range seasonNumbers {
		season, err := s.tmdb.TVSeason(ctx, detail.TMDBID, seasonNumber)
		if err != nil {
			continue
		}
		item := SeasonDetail{
			SeasonNumber: seasonNumber,
			Name:         season.Name,
			Episodes:     make([]EpisodeDetail, 0, len(season.Episodes)),
		}
		for _, episode := range season.Episodes {
			status := "missing"
			title := episode.Name
			var libID *int64
			var subtitleLanguages []string
			if row, ok := available[episodeKey(seasonNumber, episode.EpisodeNumber)]; ok {
				if row.Available {
					status = "available"
					item.AvailableCount++
				} else {
					item.MissingCount++
				}
				if strings.TrimSpace(row.Title) != "" {
					title = row.Title
				}
				if row.LibraryItemID > 0 {
					id := row.LibraryItemID
					libID = &id
				}
				subtitleLanguages = row.SubtitleLanguages
			} else {
				item.MissingCount++
			}
			item.Episodes = append(item.Episodes, EpisodeDetail{
				SeasonNumber:      seasonNumber,
				EpisodeNumber:     episode.EpisodeNumber,
				Title:             title,
				Status:            status,
				LibraryItemID:     libID,
				SubtitleLanguages: subtitleLanguages,
			})
		}
		item.EpisodeCount = len(item.Episodes)
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SeasonNumber < out[j].SeasonNumber })
	if len(out) == 0 {
		return fallbackTVSeasonsFromRows(rows), nil
	}
	return out, nil
}

// selectedEpisodes infers which season/episode combinations the currently
// selected release covers by pattern-matching its release title and any
// published file names, since a release in flight has no episodes.available
// rows yet. It matches explicit SxxEyy markers and, for season-pack releases
// (seasonPackNumber), assumes episodes 1-24 of that season are covered.
func (s *Service) selectedEpisodes(ctx context.Context, detail LibraryDetail) map[string]struct{} {
	out := make(map[string]struct{})
	if detail.SelectedReleaseID == nil {
		return out
	}
	re := regexp.MustCompile(`(?i)s(\d{1,2})e(\d{1,2})`)
	rows, err := s.detailReleaseTitles(ctx, *detail.SelectedReleaseID)
	if err != nil {
		return out
	}
	for _, title := range rows {
		matches := re.FindAllStringSubmatch(title, -1)
		for _, match := range matches {
			season, _ := strconv.Atoi(match[1])
			episode, _ := strconv.Atoi(match[2])
			out[episodeKey(season, episode)] = struct{}{}
		}
		if seasonPack := seasonPackNumber(title); seasonPack > 0 {
			for episode := 1; episode <= 24; episode++ {
				out[episodeKey(seasonPack, episode)] = struct{}{}
			}
		}
	}
	return out
}

func (s *Service) detailReleaseTitles(ctx context.Context, selectedReleaseID int64) ([]string, error) {
	rows, err := s.db.SQL.QueryContext(ctx, `
		select rc.title, coalesce(vf.file_name, '')
		from selected_releases sr
		join release_candidates rc on rc.id = sr.release_candidate_id
		left join virtual_files vf on vf.selected_release_id = sr.id
		where sr.id = $1`, selectedReleaseID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var releaseTitle string
		var fileName string
		if err := rows.Scan(&releaseTitle, &fileName); err != nil {
			return nil, err
		}
		if strings.TrimSpace(releaseTitle) != "" {
			out = append(out, releaseTitle)
		}
		if strings.TrimSpace(fileName) != "" {
			out = append(out, fileName)
		}
	}
	return out, rows.Err()
}

func (s *Service) showEpisodes(ctx context.Context, tvShowID int64) ([]showEpisodeRow, error) {
	if tvShowID <= 0 {
		return nil, nil
	}
	rows, err := s.db.SQL.QueryContext(ctx, `
		select
			e.season_number,
			e.episode_number,
			coalesce(e.title, ''),
			li.available,
			li.id
		from episodes e
		join library_items li on li.episode_id = e.id
		where e.tv_show_id = $1
		order by e.season_number asc, e.episode_number asc`, tvShowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []showEpisodeRow
	var libraryItemIDs []int64
	for rows.Next() {
		var item showEpisodeRow
		if err := rows.Scan(&item.SeasonNumber, &item.EpisodeNumber, &item.Title, &item.Available, &item.LibraryItemID); err != nil {
			return nil, err
		}
		out = append(out, item)
		libraryItemIDs = append(libraryItemIDs, item.LibraryItemID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// One batched lookup for the whole show's episodes rather than one query
	// per episode -- backs the "which languages does this episode already
	// have" column shown without expanding each episode's subtitle panel.
	languagesByItem, err := s.db.ListSubtitleLanguagesByLibraryItemIDs(ctx, libraryItemIDs)
	if err != nil {
		return nil, err
	}
	for i := range out {
		out[i].SubtitleLanguages = languagesByItem[out[i].LibraryItemID]
	}
	return out, nil
}

// fallbackTVSeasonsFromRows builds a season/episode breakdown directly from
// local database rows, used when TMDB season metadata is unavailable (TMDB
// disabled, unmatched title, or an API error) so the detail view still shows
// whatever the library actually has.
func fallbackTVSeasonsFromRows(rows []showEpisodeRow) []SeasonDetail {
	if len(rows) == 0 {
		return nil
	}
	bySeason := make(map[int]*SeasonDetail)
	var order []int
	for _, row := range rows {
		season := bySeason[row.SeasonNumber]
		if season == nil {
			season = &SeasonDetail{
				SeasonNumber: row.SeasonNumber,
				Name:         fmt.Sprintf("Season %02d", row.SeasonNumber),
			}
			bySeason[row.SeasonNumber] = season
			order = append(order, row.SeasonNumber)
		}
		status := "missing"
		if row.Available {
			status = "available"
			season.AvailableCount++
		} else {
			season.MissingCount++
		}
		title := strings.TrimSpace(row.Title)
		if title == "" {
			title = fmt.Sprintf("Episode %02d", row.EpisodeNumber)
		}
		var libID *int64
		if row.LibraryItemID > 0 {
			id := row.LibraryItemID
			libID = &id
		}
		season.Episodes = append(season.Episodes, EpisodeDetail{
			SeasonNumber:      row.SeasonNumber,
			EpisodeNumber:     row.EpisodeNumber,
			Title:             title,
			Status:            status,
			LibraryItemID:     libID,
			SubtitleLanguages: row.SubtitleLanguages,
		})
		season.EpisodeCount++
	}
	sort.Ints(order)
	out := make([]SeasonDetail, 0, len(order))
	for _, seasonNumber := range order {
		out = append(out, *bySeason[seasonNumber])
	}
	return out
}

// seasonPackNumber returns the season number a release title refers to when
// the title indicates a whole-season pack (e.g. "Season 02" or "S02" combined
// with "complete"/"pack"), or 0 if the title does not look like a season
// pack. Used to distinguish a season-pack release from a single episode that
// happens to match the same "S02" prefix.
func seasonPackNumber(title string) int {
	re := regexp.MustCompile(`(?i)(?:season|s)(\d{1,2})`)
	match := re.FindStringSubmatch(title)
	if len(match) != 2 {
		return 0
	}
	value, _ := strconv.Atoi(match[1])
	if strings.Contains(strings.ToLower(title), "complete") || strings.Contains(strings.ToLower(title), "pack") {
		return value
	}
	return 0
}

func episodeKey(season, episode int) string {
	return fmt.Sprintf("%02d:%02d", season, episode)
}

func mediaCardsFromSummaries(items []tmdb.MediaSummary) []MediaCard {
	out := make([]MediaCard, 0, len(items))
	for _, item := range items {
		out = append(out, MediaCard{
			MediaType:   item.MediaType,
			Title:       item.Title,
			Year:        item.Year,
			Overview:    item.Overview,
			PosterURL:   item.PosterURL,
			BackdropURL: item.BackdropURL,
			TMDBID:      item.TMDBID,
		})
	}
	return out
}

func castCards(items []tmdb.PersonSummary) []CastCard {
	out := make([]CastCard, 0, len(items))
	for _, item := range items {
		out = append(out, CastCard{
			ID:         item.ID,
			Name:       item.Name,
			Character:  item.Character,
			ProfileURL: item.ProfileURL,
		})
	}
	return out
}

// resolveTMDBID resolves a DiscoverLookup to a TMDB ID, searching by title
// when one isn't already known. It prefers a result whose normalized title
// (and year, if provided) matches exactly, falling back to the first search
// result so a near-miss (e.g. a retitled release) still resolves rather than
// failing outright.
func (s *Service) resolveTMDBID(ctx context.Context, lookup DiscoverLookup) (int64, error) {
	if lookup.TMDBID > 0 {
		return lookup.TMDBID, nil
	}
	results, err := s.tmdb.Search(ctx, lookup.MediaType, lookup.Title)
	if err != nil {
		return 0, err
	}
	best := int64(0)
	for _, item := range results {
		if normalizeSearch(item.Title) != normalizeSearch(lookup.Title) {
			continue
		}
		if lookup.Year > 0 && item.Year > 0 && lookup.Year != item.Year {
			continue
		}
		best = item.TMDBID
		break
	}
	if best == 0 && len(results) > 0 {
		best = results[0].TMDBID
	}
	if best == 0 {
		return 0, fmt.Errorf("tmdb match not found")
	}
	return best, nil
}

func normalizeSearch(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("-", " ", "_", " ", ".", " ", ":", " ", "/", " ").Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

// queueStateFromRank maps the highest queue_rank seen across a TV show's
// episodes (see tvCards: 3=actively downloading, 2=failed, 1=requested,
// 0=none) to the queue state string surfaced on the show's MediaCard.
func queueStateFromRank(rank int, available bool) string {
	if available {
		return string(database.QueueAvailable)
	}
	switch rank {
	case 3:
		return "downloading"
	case 2:
		return string(database.QueueFailed)
	case 1:
		return string(database.QueueRequested)
	default:
		return string(database.QueueRequested)
	}
}

// CalendarEntry is one library item with a known release/air date.
type CalendarEntry struct {
	ID            int64  `json:"id"`
	LibraryItemID int64  `json:"libraryItemId"`
	Type          string `json:"type"` // "movie" or "tv"
	Title         string `json:"title"`
	ReleaseDate   string `json:"releaseDate"` // "YYYY-MM-DD"
	TmdbID        int64  `json:"tmdbId,omitempty"`
	PosterURL     string `json:"posterUrl,omitempty"`
	Available     bool   `json:"available"`
	QueueState    string `json:"queueState,omitempty"`
	// SeasonNumber/EpisodeNumber/EpisodeTitle are only populated for
	// Type=="tv" entries -- Title above is the show's name, not the
	// episode's, so the calendar previously had no way to show which
	// season/episode was airing on a given date.
	SeasonNumber  int    `json:"seasonNumber,omitempty"`
	EpisodeNumber int    `json:"episodeNumber,omitempty"`
	EpisodeTitle  string `json:"episodeTitle,omitempty"`
}

// ReleaseCalendar returns library movies (and optionally episodes) with their
// release/air date for the given month "YYYY-MM".  Falls back to the current
// month when month is empty.  Only shows items that exist in the local library.
func (s *Service) ReleaseCalendar(ctx context.Context, month string) ([]CalendarEntry, error) {
	if month == "" {
		month = time.Now().UTC().Format("2006-01")
	}
	// Parse the requested month to build start/end bounds.
	t, err := time.Parse("2006-01", month)
	if err != nil {
		t = time.Now().UTC()
	}
	startDate := t.Format("2006-01-02")
	endDate := t.AddDate(0, 1, 0).Format("2006-01-02")

	rows, err := s.db.SQL.QueryContext(ctx, `
		select
			li.id,
			li.media_type,
			coalesce(m.title, tv.title, li.title, ''),
			coalesce(m.release_date::text, e.air_date::text, ''),
			coalesce(m.tmdb_id, tv.tmdb_id, 0),
			coalesce(m.poster_url, tv.poster_url, ''),
			li.available,
			coalesce(q.state, ''),
			coalesce(e.season_number, 0),
			coalesce(e.episode_number, 0),
			coalesce(e.title, '')
		from library_items li
		left join movies m on m.id = li.movie_id
		left join episodes e on e.id = li.episode_id
		left join tv_shows tv on tv.id = e.tv_show_id
		left join lateral (
			select state from queue_items
			where library_item_id = li.id
			order by created_at desc
			limit 1
		) q on true
		where (
			(li.media_type = 'movie' and m.release_date >= $1::date and m.release_date < $2::date)
			or
			(li.media_type = 'episode' and e.air_date >= $1::date and e.air_date < $2::date
			 and e.season_number > 0 and e.episode_number > 0)
		)
		order by coalesce(m.release_date, e.air_date) asc, li.id asc`,
		startDate, endDate)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []CalendarEntry
	for rows.Next() {
		var e CalendarEntry
		var dateStr string
		if err := rows.Scan(&e.LibraryItemID, &e.Type, &e.Title, &dateStr,
			&e.TmdbID, &e.PosterURL, &e.Available, &e.QueueState,
			&e.SeasonNumber, &e.EpisodeNumber, &e.EpisodeTitle); err != nil {
			return nil, err
		}
		e.ID = e.LibraryItemID
		if len(dateStr) >= 10 {
			e.ReleaseDate = dateStr[:10]
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// ManualSearchResult is defined in workflow package for manual Hydra searches.
// This stub satisfies the CatalogService interface compilation.
type ManualSearchResult = struct{} // placeholder if needed
