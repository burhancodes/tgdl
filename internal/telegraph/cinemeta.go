package telegraph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// CinemetaInfo holds movie/series metadata fetched from Cinemeta.
type CinemetaInfo struct {
	Name        string
	Year        string
	Poster      string
	Description string
	Rating      string
	Genres      []string
	IMDbID      string
	Type        string
}

// FetchCinemetaInfo queries Cinemeta for rich poster and summary metadata.
func FetchCinemetaInfo(ctx context.Context, client *http.Client, query string) *CinemetaInfo {
	cleanQuery := strings.ToLower(strings.TrimSpace(query))
	if len(cleanQuery) < 2 {
		return nil
	}

	if client == nil {
		client = &http.Client{Timeout: 3500 * time.Millisecond}
	}

	encoded := url.QueryEscape(strings.TrimSpace(query))
	var bestCandidate *CinemetaInfo

	for _, mtype := range []string{"movie", "series"} {
		catalogURL := fmt.Sprintf("https://v3-cinemeta.strem.io/catalog/%s/top/search=%s.json", mtype, encoded)
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, catalogURL, nil)
		if err != nil {
			continue
		}
		req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64)")

		resp, err := client.Do(req)
		if err != nil {
			continue
		}

		var catData struct {
			Metas []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"metas"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&catData)
		resp.Body.Close()
		if decodeErr != nil || len(catData.Metas) == 0 {
			continue
		}

		for idx, item := range catData.Metas {
			if idx >= 5 {
				break
			}
			itemName := strings.ToLower(strings.TrimSpace(item.Name))
			if itemName == cleanQuery || strings.Contains(itemName, cleanQuery) || strings.Contains(cleanQuery, itemName) {
				if item.ID == "" {
					continue
				}
				metaURL := fmt.Sprintf("https://v3-cinemeta.strem.io/meta/%s/%s.json", mtype, item.ID)
				mreq, merr := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
				if merr != nil {
					continue
				}
				mreq.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64)")

				mresp, mdoErr := client.Do(mreq)
				if mdoErr != nil {
					continue
				}

				var mdata struct {
					Meta struct {
						Name        string   `json:"name"`
						Year        string   `json:"year"`
						ReleaseInfo string   `json:"releaseInfo"`
						Poster      string   `json:"poster"`
						Description string   `json:"description"`
						IMDbRating  string   `json:"imdbRating"`
						Genres      []string `json:"genres"`
					} `json:"meta"`
				}
				mdecErr := json.NewDecoder(mresp.Body).Decode(&mdata)
				mresp.Body.Close()
				if mdecErr != nil || mdata.Meta.Name == "" || mdata.Meta.Poster == "" {
					continue
				}

				rawPoster := mdata.Meta.Poster
				smallPoster := strings.ReplaceAll(rawPoster, "/poster/medium/", "/poster/small/")
				smallPoster = strings.ReplaceAll(smallPoster, "/poster/large/", "/poster/small/")

				year := mdata.Meta.Year
				if year == "" {
					year = mdata.Meta.ReleaseInfo
				}

				candidate := &CinemetaInfo{
					Name:        mdata.Meta.Name,
					Year:        year,
					Poster:      smallPoster,
					Description: mdata.Meta.Description,
					Rating:      mdata.Meta.IMDbRating,
					Genres:      mdata.Meta.Genres,
					IMDbID:      item.ID,
					Type:        mtype,
				}

				if itemName == cleanQuery {
					return candidate
				}
				if bestCandidate == nil {
					bestCandidate = candidate
				}
			}
		}
	}

	return bestCandidate
}
