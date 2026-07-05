package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hjongedijk/drakkar/internal/config"
	"github.com/hjongedijk/drakkar/internal/mediadate"
)

type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
}

type MovieDetails struct {
	Title               string
	OriginalTitle       string
	Year                int
	ReleaseDate         string // "YYYY-MM-DD"
	IMDbID              string
	Overview            string
	Tagline             string
	Status              string // "Released", "In Production", etc.
	ContentRating       string // "PG-13", "R" from release dates
	OriginalLanguage    string
	RuntimeMinutes      int
	PosterURL           string
	BackdropURL         string
	TrailerURL          string
	Genres              []string
	ProductionCompanies []string
	AlternativeTitles   []string
	Popularity          float64
	VoteAverage         float64
	VoteCount           int
	Budget              int64
	Revenue             int64
	Cast                []PersonSummary
	Recommendations     []MediaSummary
	Similar             []MediaSummary
}

type TVDetails struct {
	Name                string
	OriginalName        string
	Year                int
	FirstAirDate        string // "YYYY-MM-DD"
	LastAirDate         string // "YYYY-MM-DD"
	IMDbID              string
	Overview            string
	Tagline             string
	Status              string // "Returning Series", "Ended", etc.
	ContentRating       string // "TV-MA", "TV-14" from content ratings
	OriginalLanguage    string
	Network             string
	EpisodeRunTime      int
	NumberOfSeasons     int
	NumberOfEpisodes    int
	InProduction        bool
	PosterURL           string
	BackdropURL         string
	TrailerURL          string
	Genres              []string
	ProductionCompanies []string
	AlternativeTitles   []string
	Popularity          float64
	VoteAverage         float64
	VoteCount           int
	Cast                []PersonSummary
	Recommendations     []MediaSummary
	Similar             []MediaSummary
}

type MediaSummary struct {
	Title       string
	Year        int
	Overview    string
	PosterURL   string
	BackdropURL string
	TMDBID      int64
	MediaType   string
}

type PersonSummary struct {
	ID         int64
	Name       string
	Character  string
	ProfileURL string
}

type TVSeason struct {
	SeasonNumber int
	Name         string
	Episodes     []TVEpisode
}

type TVEpisode struct {
	EpisodeNumber int
	Name          string
	AirDate       string // "YYYY-MM-DD", may be empty
}

type ListResult struct {
	Page       int
	TotalPages int
	Items      []MediaSummary
}

func NewClient(cfg config.MetadataConfig) *Client {
	return &Client{
		apiKey:  strings.TrimSpace(cfg.TMDB.APIKey),
		baseURL: "https://api.themoviedb.org/3",
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (c *Client) Enabled() bool {
	return c != nil && strings.TrimSpace(c.apiKey) != ""
}

func (c *Client) MovieDetails(ctx context.Context, tmdbID int64) (MovieDetails, error) {
	var payload struct {
		Title            string  `json:"title"`
		OriginalTitle    string  `json:"original_title"`
		ReleaseDate      string  `json:"release_date"`
		IMDbID           string  `json:"imdb_id"`
		Overview         string  `json:"overview"`
		Tagline          string  `json:"tagline"`
		OriginalLanguage string  `json:"original_language"`
		Runtime          int     `json:"runtime"`
		PosterPath       string  `json:"poster_path"`
		BackdropPath     string  `json:"backdrop_path"`
		Popularity       float64 `json:"popularity"`
		VoteAverage      float64 `json:"vote_average"`
		VoteCount        int     `json:"vote_count"`
		Budget           int64   `json:"budget"`
		Revenue          int64   `json:"revenue"`
		Genres           []struct {
			Name string `json:"name"`
		} `json:"genres"`
		ProductionCompanies []struct {
			Name string `json:"name"`
		} `json:"production_companies"`
		AlternativeTitles struct {
			Titles []struct {
				Title string `json:"title"`
			} `json:"titles"`
		} `json:"alternative_titles"`
		Credits struct {
			Cast []struct {
				ID          int64  `json:"id"`
				Name        string `json:"name"`
				Character   string `json:"character"`
				ProfilePath string `json:"profile_path"`
			} `json:"cast"`
		} `json:"credits"`
		Recommendations struct {
			Results []struct {
				ID           int64  `json:"id"`
				Title        string `json:"title"`
				Overview     string `json:"overview"`
				ReleaseDate  string `json:"release_date"`
				PosterPath   string `json:"poster_path"`
				BackdropPath string `json:"backdrop_path"`
			} `json:"results"`
		} `json:"recommendations"`
		Similar struct {
			Results []struct {
				ID           int64  `json:"id"`
				Title        string `json:"title"`
				Overview     string `json:"overview"`
				ReleaseDate  string `json:"release_date"`
				PosterPath   string `json:"poster_path"`
				BackdropPath string `json:"backdrop_path"`
			} `json:"results"`
		} `json:"similar"`
		Status       string `json:"status"`
		ReleaseDates struct {
			Results []struct {
				ISO3166_1    string `json:"iso_3166_1"`
				ReleaseDates []struct {
					Certification string `json:"certification"`
					ReleaseDate   string `json:"release_date"`
				} `json:"release_dates"`
			} `json:"results"`
		} `json:"release_dates"`
		Videos struct {
			Results []struct {
				Key      string `json:"key"`
				Site     string `json:"site"`
				Type     string `json:"type"`
				Official bool   `json:"official"`
			} `json:"results"`
		} `json:"videos"`
	}
	values := url.Values{}
	values.Set("append_to_response", "alternative_titles,credits,recommendations,similar,release_dates,videos")
	if err := c.get(ctx, "/movie/"+strconv.FormatInt(tmdbID, 10), values, &payload); err != nil {
		return MovieDetails{}, err
	}
	genres := make([]string, 0, len(payload.Genres))
	for _, g := range payload.Genres {
		genres = append(genres, g.Name)
	}
	companies := make([]string, 0, len(payload.ProductionCompanies))
	for _, company := range payload.ProductionCompanies {
		if name := strings.TrimSpace(company.Name); name != "" {
			companies = append(companies, name)
		}
	}
	altTitles := make([]string, 0)
	for _, t := range payload.AlternativeTitles.Titles {
		if title := strings.TrimSpace(t.Title); title != "" {
			altTitles = append(altTitles, title)
		}
	}
	// Extract US content rating from release_dates
	contentRating := ""
	for _, r := range payload.ReleaseDates.Results {
		if strings.ToUpper(r.ISO3166_1) == "US" {
			for _, rd := range r.ReleaseDates {
				if rd.Certification != "" {
					contentRating = rd.Certification
					break
				}
			}
		}
	}
	// Extract official trailer URL from videos
	trailerURL := ""
	for _, v := range payload.Videos.Results {
		if strings.EqualFold(v.Type, "Trailer") && strings.EqualFold(v.Site, "YouTube") && v.Official {
			trailerURL = "https://www.youtube.com/watch?v=" + v.Key
			break
		}
	}
	return MovieDetails{
		Title:               strings.TrimSpace(payload.Title),
		OriginalTitle:       strings.TrimSpace(payload.OriginalTitle),
		Year:                mediadate.Year(payload.ReleaseDate),
		ReleaseDate:         payload.ReleaseDate,
		IMDbID:              strings.TrimSpace(payload.IMDbID),
		Overview:            strings.TrimSpace(payload.Overview),
		Tagline:             strings.TrimSpace(payload.Tagline),
		Status:              strings.TrimSpace(payload.Status),
		ContentRating:       contentRating,
		OriginalLanguage:    strings.TrimSpace(payload.OriginalLanguage),
		RuntimeMinutes:      payload.Runtime,
		PosterURL:           imageURL("w500", payload.PosterPath),
		BackdropURL:         imageURL("w1280", payload.BackdropPath),
		TrailerURL:          trailerURL,
		Popularity:          payload.Popularity,
		VoteAverage:         payload.VoteAverage,
		VoteCount:           payload.VoteCount,
		Budget:              payload.Budget,
		Revenue:             payload.Revenue,
		Genres:              genres,
		ProductionCompanies: companies,
		AlternativeTitles:   altTitles,
		Cast:                mapCast(payload.Credits.Cast),
		Recommendations:     mapMovieResults(payload.Recommendations.Results),
		Similar:             mapMovieResults(payload.Similar.Results),
	}, nil
}

func (c *Client) TVDetails(ctx context.Context, tmdbID int64) (TVDetails, error) {
	var payload struct {
		Name             string  `json:"name"`
		OriginalName     string  `json:"original_name"`
		FirstAirDate     string  `json:"first_air_date"`
		Tagline          string  `json:"tagline"`
		OriginalLanguage string  `json:"original_language"`
		Status           string  `json:"status"`
		Popularity       float64 `json:"popularity"`
		VoteAverage      float64 `json:"vote_average"`
		VoteCount        int     `json:"vote_count"`
		NumberOfSeasons  int     `json:"number_of_seasons"`
		NumberOfEpisodes int     `json:"number_of_episodes"`
		Overview         string  `json:"overview"`
		PosterPath       string  `json:"poster_path"`
		BackdropPath     string  `json:"backdrop_path"`
		EpisodeRunTime   []int   `json:"episode_run_time"`
		Networks         []struct {
			Name string `json:"name"`
		} `json:"networks"`
		Genres []struct {
			Name string `json:"name"`
		} `json:"genres"`
		ProductionCompanies []struct {
			Name string `json:"name"`
		} `json:"production_companies"`
		ExternalIDs struct {
			IMDbID string `json:"imdb_id"`
		} `json:"external_ids"`
		AlternativeTitles struct {
			Results []struct {
				Title string `json:"title"`
			} `json:"results"`
		} `json:"alternative_titles"`
		AggregateCredits struct {
			Cast []struct {
				ID          int64  `json:"id"`
				Name        string `json:"name"`
				Character   string `json:"roles.0.character"`
				ProfilePath string `json:"profile_path"`
				Roles       []struct {
					Character string `json:"character"`
				} `json:"roles"`
			} `json:"cast"`
		} `json:"aggregate_credits"`
		Recommendations struct {
			Results []struct {
				ID           int64  `json:"id"`
				Name         string `json:"name"`
				Overview     string `json:"overview"`
				FirstAirDate string `json:"first_air_date"`
				PosterPath   string `json:"poster_path"`
				BackdropPath string `json:"backdrop_path"`
			} `json:"results"`
		} `json:"recommendations"`
		Similar struct {
			Results []struct {
				ID           int64  `json:"id"`
				Name         string `json:"name"`
				Overview     string `json:"overview"`
				FirstAirDate string `json:"first_air_date"`
				PosterPath   string `json:"poster_path"`
				BackdropPath string `json:"backdrop_path"`
			} `json:"results"`
		} `json:"similar"`
		LastAirDate    string `json:"last_air_date"`
		InProduction   bool   `json:"in_production"`
		ContentRatings struct {
			Results []struct {
				ISO3166_1 string `json:"iso_3166_1"`
				Rating    string `json:"rating"`
			} `json:"results"`
		} `json:"content_ratings"`
		Videos struct {
			Results []struct {
				Key      string `json:"key"`
				Site     string `json:"site"`
				Type     string `json:"type"`
				Official bool   `json:"official"`
			} `json:"results"`
		} `json:"videos"`
	}
	values := url.Values{}
	values.Set("append_to_response", "external_ids,alternative_titles,aggregate_credits,recommendations,similar,content_ratings,videos")
	if err := c.get(ctx, "/tv/"+strconv.FormatInt(tmdbID, 10), values, &payload); err != nil {
		return TVDetails{}, err
	}
	network := ""
	if len(payload.Networks) > 0 {
		network = payload.Networks[0].Name
	}
	epRunTime := 0
	if len(payload.EpisodeRunTime) > 0 {
		epRunTime = payload.EpisodeRunTime[0]
	}
	genres := make([]string, 0, len(payload.Genres))
	for _, g := range payload.Genres {
		genres = append(genres, g.Name)
	}
	companies := make([]string, 0, len(payload.ProductionCompanies))
	for _, company := range payload.ProductionCompanies {
		if name := strings.TrimSpace(company.Name); name != "" {
			companies = append(companies, name)
		}
	}
	altTitles := make([]string, 0)
	for _, t := range payload.AlternativeTitles.Results {
		if title := strings.TrimSpace(t.Title); title != "" {
			altTitles = append(altTitles, title)
		}
	}
	cast := make([]PersonSummary, 0, len(payload.AggregateCredits.Cast))
	for _, person := range payload.AggregateCredits.Cast {
		character := strings.TrimSpace(person.Character)
		if character == "" && len(person.Roles) > 0 {
			character = strings.TrimSpace(person.Roles[0].Character)
		}
		cast = append(cast, PersonSummary{
			ID:         person.ID,
			Name:       strings.TrimSpace(person.Name),
			Character:  character,
			ProfileURL: imageURL("w185", person.ProfilePath),
		})
	}
	// Extract US content rating
	tvContentRating := ""
	for _, r := range payload.ContentRatings.Results {
		if strings.ToUpper(r.ISO3166_1) == "US" && r.Rating != "" {
			tvContentRating = r.Rating
			break
		}
	}
	// Extract official trailer
	tvTrailerURL := ""
	for _, v := range payload.Videos.Results {
		if strings.EqualFold(v.Type, "Trailer") && strings.EqualFold(v.Site, "YouTube") && v.Official {
			tvTrailerURL = "https://www.youtube.com/watch?v=" + v.Key
			break
		}
	}
	return TVDetails{
		Name:                strings.TrimSpace(payload.Name),
		OriginalName:        strings.TrimSpace(payload.OriginalName),
		Year:                mediadate.Year(payload.FirstAirDate),
		FirstAirDate:        payload.FirstAirDate,
		LastAirDate:         payload.LastAirDate,
		IMDbID:              strings.TrimSpace(payload.ExternalIDs.IMDbID),
		Overview:            strings.TrimSpace(payload.Overview),
		Tagline:             strings.TrimSpace(payload.Tagline),
		OriginalLanguage:    strings.TrimSpace(payload.OriginalLanguage),
		Network:             network,
		Status:              strings.TrimSpace(payload.Status),
		ContentRating:       tvContentRating,
		EpisodeRunTime:      epRunTime,
		NumberOfSeasons:     payload.NumberOfSeasons,
		NumberOfEpisodes:    payload.NumberOfEpisodes,
		InProduction:        payload.InProduction,
		PosterURL:           imageURL("w500", payload.PosterPath),
		BackdropURL:         imageURL("w1280", payload.BackdropPath),
		TrailerURL:          tvTrailerURL,
		Popularity:          payload.Popularity,
		VoteAverage:         payload.VoteAverage,
		VoteCount:           payload.VoteCount,
		Genres:              genres,
		ProductionCompanies: companies,
		AlternativeTitles:   altTitles,
		Cast:                cast,
		Recommendations:     mapTVResults(payload.Recommendations.Results),
		Similar:             mapTVResults(payload.Similar.Results),
	}, nil
}

func (c *Client) Search(ctx context.Context, mediaType string, query string) ([]MediaSummary, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []MediaSummary{}, nil
	}
	values := url.Values{}
	values.Set("query", query)
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie":
		var payload struct {
			Results []struct {
				ID           int64  `json:"id"`
				Title        string `json:"title"`
				Overview     string `json:"overview"`
				ReleaseDate  string `json:"release_date"`
				PosterPath   string `json:"poster_path"`
				BackdropPath string `json:"backdrop_path"`
			} `json:"results"`
		}
		if err := c.get(ctx, "/search/movie", values, &payload); err != nil {
			return nil, err
		}
		return mapMovieResults(payload.Results), nil
	case "tv":
		var payload struct {
			Results []struct {
				ID           int64  `json:"id"`
				Name         string `json:"name"`
				Overview     string `json:"overview"`
				FirstAirDate string `json:"first_air_date"`
				PosterPath   string `json:"poster_path"`
				BackdropPath string `json:"backdrop_path"`
			} `json:"results"`
		}
		if err := c.get(ctx, "/search/tv", values, &payload); err != nil {
			return nil, err
		}
		return mapTVResults(payload.Results), nil
	default:
		return nil, fmt.Errorf("unsupported media type %q", mediaType)
	}
}

func (c *Client) Trending(ctx context.Context, mediaType string) ([]MediaSummary, error) {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie":
		return c.trendingPath(ctx, "/trending/movie/day", "movie")
	case "tv":
		return c.trendingPath(ctx, "/trending/tv/day", "tv")
	default:
		return nil, fmt.Errorf("unsupported media type %q", mediaType)
	}
}

func (c *Client) TrendingPage(ctx context.Context, mediaType string, page int) (ListResult, error) {
	if page < 1 {
		page = 1
	}
	values := url.Values{}
	values.Set("page", strconv.Itoa(page))
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "movie":
		return c.listPath(ctx, "/trending/movie/day", "movie", values)
	case "tv":
		return c.listPath(ctx, "/trending/tv/day", "tv", values)
	default:
		return ListResult{}, fmt.Errorf("unsupported media type %q", mediaType)
	}
}

func (c *Client) TVSeason(ctx context.Context, tmdbID int64, seasonNumber int) (TVSeason, error) {
	var payload struct {
		Name         string `json:"name"`
		SeasonNumber int    `json:"season_number"`
		Episodes     []struct {
			EpisodeNumber int    `json:"episode_number"`
			Name          string `json:"name"`
			AirDate       string `json:"air_date"`
		} `json:"episodes"`
	}
	if err := c.get(ctx, "/tv/"+strconv.FormatInt(tmdbID, 10)+"/season/"+strconv.Itoa(seasonNumber), nil, &payload); err != nil {
		return TVSeason{}, err
	}
	out := TVSeason{
		Name:         strings.TrimSpace(payload.Name),
		SeasonNumber: payload.SeasonNumber,
		Episodes:     make([]TVEpisode, 0, len(payload.Episodes)),
	}
	for _, episode := range payload.Episodes {
		out.Episodes = append(out.Episodes, TVEpisode{
			EpisodeNumber: episode.EpisodeNumber,
			Name:          strings.TrimSpace(episode.Name),
			AirDate:       episode.AirDate,
		})
	}
	return out, nil
}

func (c *Client) TVSeasonNumbers(ctx context.Context, tmdbID int64) ([]int, error) {
	var payload struct {
		Seasons []struct {
			SeasonNumber int `json:"season_number"`
		} `json:"seasons"`
	}
	if err := c.get(ctx, "/tv/"+strconv.FormatInt(tmdbID, 10), nil, &payload); err != nil {
		return nil, err
	}
	out := make([]int, 0, len(payload.Seasons))
	for _, season := range payload.Seasons {
		if season.SeasonNumber <= 0 {
			continue
		}
		out = append(out, season.SeasonNumber)
	}
	return out, nil
}

func (c *Client) trendingPath(ctx context.Context, path string, mediaType string) ([]MediaSummary, error) {
	result, err := c.listPath(ctx, path, mediaType, nil)
	if err != nil {
		return nil, err
	}
	return result.Items, nil
}

func (c *Client) listPath(ctx context.Context, path string, mediaType string, values url.Values) (ListResult, error) {
	var payload struct {
		Page       int `json:"page"`
		TotalPages int `json:"total_pages"`
		Results    []struct {
			ID           int64  `json:"id"`
			Title        string `json:"title"`
			Name         string `json:"name"`
			Overview     string `json:"overview"`
			ReleaseDate  string `json:"release_date"`
			FirstAirDate string `json:"first_air_date"`
			PosterPath   string `json:"poster_path"`
			BackdropPath string `json:"backdrop_path"`
		} `json:"results"`
	}
	if err := c.get(ctx, path, values, &payload); err != nil {
		return ListResult{}, err
	}
	out := make([]MediaSummary, 0, len(payload.Results))
	for _, item := range payload.Results {
		title := strings.TrimSpace(item.Title)
		year := mediadate.Year(item.ReleaseDate)
		if mediaType == "tv" {
			title = strings.TrimSpace(item.Name)
			year = mediadate.Year(item.FirstAirDate)
		}
		out = append(out, MediaSummary{
			Title:       title,
			Year:        year,
			Overview:    strings.TrimSpace(item.Overview),
			PosterURL:   imageURL("w500", item.PosterPath),
			BackdropURL: imageURL("w1280", item.BackdropPath),
			TMDBID:      item.ID,
			MediaType:   mediaType,
		})
	}
	return ListResult{
		Page:       payload.Page,
		TotalPages: payload.TotalPages,
		Items:      out,
	}, nil
}

func imageURL(size, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return "https://image.tmdb.org/t/p/" + size + path
}

func mapCast(items []struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Character   string `json:"character"`
	ProfilePath string `json:"profile_path"`
}) []PersonSummary {
	out := make([]PersonSummary, 0, len(items))
	for _, item := range items {
		out = append(out, PersonSummary{
			ID:         item.ID,
			Name:       strings.TrimSpace(item.Name),
			Character:  strings.TrimSpace(item.Character),
			ProfileURL: imageURL("w185", item.ProfilePath),
		})
	}
	return out
}

func mapMovieResults(items []struct {
	ID           int64  `json:"id"`
	Title        string `json:"title"`
	Overview     string `json:"overview"`
	ReleaseDate  string `json:"release_date"`
	PosterPath   string `json:"poster_path"`
	BackdropPath string `json:"backdrop_path"`
}) []MediaSummary {
	out := make([]MediaSummary, 0, len(items))
	for _, item := range items {
		out = append(out, MediaSummary{
			Title:       strings.TrimSpace(item.Title),
			Year:        mediadate.Year(item.ReleaseDate),
			Overview:    strings.TrimSpace(item.Overview),
			PosterURL:   imageURL("w500", item.PosterPath),
			BackdropURL: imageURL("w1280", item.BackdropPath),
			TMDBID:      item.ID,
			MediaType:   "movie",
		})
	}
	return out
}

func mapTVResults(items []struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Overview     string `json:"overview"`
	FirstAirDate string `json:"first_air_date"`
	PosterPath   string `json:"poster_path"`
	BackdropPath string `json:"backdrop_path"`
}) []MediaSummary {
	out := make([]MediaSummary, 0, len(items))
	for _, item := range items {
		out = append(out, MediaSummary{
			Title:       strings.TrimSpace(item.Name),
			Year:        mediadate.Year(item.FirstAirDate),
			Overview:    strings.TrimSpace(item.Overview),
			PosterURL:   imageURL("w500", item.PosterPath),
			BackdropURL: imageURL("w1280", item.BackdropPath),
			TMDBID:      item.ID,
			MediaType:   "tv",
		})
	}
	return out
}

func (c *Client) get(ctx context.Context, path string, values url.Values, target any) error {
	if !c.Enabled() {
		return fmt.Errorf("tmdb client unavailable")
	}
	u, err := url.Parse(c.baseURL + path)
	if err != nil {
		return err
	}
	q := u.Query()
	q.Set("api_key", c.apiKey)
	for key, entries := range values {
		for _, value := range entries {
			q.Add(key, value)
		}
	}
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("tmdb status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(target)
}
