package telegraph

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/burhanverse/tgdl/internal/torrent"
)

var defaultFallbackDomains = []string{"graph.org", "telegra.ph"}

// Helper manages Telegraph account tokens, page generation, pagination, and fallback domains.
type Helper struct {
	AuthorName      string
	AuthorURL       string
	DefaultDomain   string
	FallbackDomains []string

	mu     sync.Mutex
	tokens map[string]string
	client *http.Client
}

// NewHelper creates a new Telegraph helper.
func NewHelper() *Helper {
	return &Helper{
		AuthorName:      "TGDL",
		AuthorURL:       "https://github.com/burhanverse/tgdl",
		DefaultDomain:   "graph.org",
		FallbackDomains: defaultFallbackDomains,
		tokens:          make(map[string]string),
		client:          &http.Client{Timeout: 30 * time.Second},
	}
}

func (h *Helper) domainCandidates(domain string) []string {
	primary := domain
	if primary == "" {
		primary = h.DefaultDomain
	}
	candidates := []string{primary}
	for _, fb := range h.FallbackDomains {
		if fb != primary {
			candidates = append(candidates, fb)
		}
	}
	return candidates
}

func (h *Helper) getClient(ctx context.Context, domain string) (*Client, error) {
	if domain == "" {
		domain = h.DefaultDomain
	}
	h.mu.Lock()
	token := h.tokens[domain]
	h.mu.Unlock()

	c := NewClient(token, domain, h.client)
	if token == "" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		shortName := "tgdl_" + hex.EncodeToString(b)[:8]
		newToken, err := c.CreateAccount(ctx, shortName, h.AuthorName, h.AuthorURL)
		if err != nil {
			return nil, fmt.Errorf("failed to create Telegraph account on %s: %w", domain, err)
		}
		h.mu.Lock()
		h.tokens[domain] = newToken
		h.mu.Unlock()
	}
	return c, nil
}

func (h *Helper) createPageWithRetry(ctx context.Context, title string, nodes []any, domain string) (*Page, error) {
	const maxRetries = 2
	const maxRetryWait = 15

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		client, err := h.getClient(ctx, domain)
		if err != nil {
			return nil, err
		}

		page, err := client.CreatePage(ctx, title, nodes, h.AuthorName, h.AuthorURL, false)
		if err == nil {
			return page, nil
		}

		var floodErr *RetryAfterError
		if errors.As(err, &floodErr) {
			if floodErr.Seconds > maxRetryWait || attempt >= maxRetries {
				return nil, floodErr
			}
			slog.Warn("Telegraph flood wait encountered", "domain", domain, "seconds", floodErr.Seconds)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(floodErr.Seconds) * time.Second):
				continue
			}
		}

		lastErr = err
		break
	}
	return nil, lastErr
}

func (h *Helper) editPageWithRetry(ctx context.Context, path, title string, nodes []any, domain string) (*Page, error) {
	client, err := h.getClient(ctx, domain)
	if err != nil {
		return nil, err
	}
	return client.EditPage(ctx, path, title, nodes, h.AuthorName, h.AuthorURL, false)
}

// PublishTorrentResults formats torrent search results into an Instant View Telegraph page.
func (h *Helper) PublishTorrentResults(ctx context.Context, results []torrent.Result, query, site string) (string, error) {
	if len(results) == 0 {
		return "", errors.New("no results to publish")
	}

	safeQuery := html.EscapeString(query)
	safeSite := html.EscapeString(strings.Title(site)) //nolint:staticcheck

	// Cinemeta metadata enrichment
	meta := FetchCinemetaInfo(ctx, h.client, query)

	var header strings.Builder
	header.WriteString(fmt.Sprintf("<h3>Search Results: <code>%s</code></h3>", safeQuery))
	header.WriteString(fmt.Sprintf("<blockquote><b>Source:</b> %s &nbsp;•&nbsp; <b>Count:</b> %d</blockquote>", safeSite, len(results)))

	if meta != nil {
		cName := html.EscapeString(meta.Name)
		if cName == "" {
			cName = safeQuery
		}
		cYear := html.EscapeString(meta.Year)
		cRating := html.EscapeString(meta.Rating)
		cDesc := html.EscapeString(meta.Description)
		cPoster := html.EscapeString(meta.Poster)
		cGenres := html.EscapeString(strings.Join(meta.Genres, ", "))

		header.WriteString("<hr>")
		if cPoster != "" {
			header.WriteString(fmt.Sprintf("<figure><img src='%s'></figure>", cPoster))
		}
		titleLine := fmt.Sprintf("<b>%s</b>", cName)
		if cYear != "" {
			titleLine += fmt.Sprintf(" (%s)", cYear)
		}
		header.WriteString(fmt.Sprintf("<h3>%s</h3>", titleLine))

		var metaDetails []string
		if cRating != "" {
			metaDetails = append(metaDetails, fmt.Sprintf("<b>IMDb Rating:</b> ⭐ %s/10", cRating))
		}
		if cGenres != "" {
			metaDetails = append(metaDetails, fmt.Sprintf("<b>Genres:</b> %s", cGenres))
		}
		if len(metaDetails) > 0 {
			header.WriteString(fmt.Sprintf("<p>%s</p>", strings.Join(metaDetails, " &nbsp;•&nbsp; ")))
		}
		if cDesc != "" {
			header.WriteString(fmt.Sprintf("<blockquote>%s</blockquote>", cDesc))
		}
	}
	header.WriteString("<hr>")

	headerHTML := header.String()

	var chunks []string
	var current strings.Builder
	current.WriteString(headerHTML)

	for i, r := range results {
		if i >= 300 {
			break
		}

		name := html.EscapeString(r.Name)
		size := html.EscapeString(r.Size)
		rawURL := r.URL
		if rawURL == "" {
			rawURL = "#"
		}
		safeURL := html.EscapeString(rawURL)

		var item strings.Builder
		item.WriteString(fmt.Sprintf("<h4>%d. <a href='%s'>%s</a></h4>", i+1, safeURL, name))
		item.WriteString(fmt.Sprintf("<p><b>Size:</b> <code>%s</code> &nbsp;•&nbsp; <b>Seeders:</b> %d &nbsp;•&nbsp; <b>Leechers:</b> %d</p>", size, r.Seeders, r.Leechers))

		var links []string
		if r.Magnet != "" {
			quotedMag := html.EscapeString(url.QueryEscape(r.Magnet))
			links = append(links, fmt.Sprintf("<a href='http://t.me/share/url?url=%s'>Share Magnet</a>", quotedMag))
		}

		prov := r.Provider
		if prov == "" || strings.EqualFold(prov, "unknown") || strings.EqualFold(prov, "none") {
			if rawURL != "#" && !strings.HasPrefix(rawURL, "magnet:") {
				if u, pErr := url.Parse(rawURL); pErr == nil {
					h := u.Hostname()
					if h != "" {
						prov = strings.TrimPrefix(h, "www.")
					}
				}
			}
		}
		if prov == "" {
			prov = "Source"
		}
		safeProv := html.EscapeString(prov)

		targetLink := rawURL
		if targetLink == "#" || targetLink == "" {
			targetLink = r.Magnet
		}
		if targetLink != "" && targetLink != "#" {
			links = append(links, fmt.Sprintf("<a href='%s'>%s</a>", html.EscapeString(targetLink), safeProv))
		} else {
			links = append(links, fmt.Sprintf("<b>%s</b>", safeProv))
		}

		if len(links) > 0 {
			item.WriteString(fmt.Sprintf("<blockquote>%s</blockquote>", strings.Join(links, " &nbsp;•&nbsp; ")))
		}
		item.WriteString("<hr>")

		itemHTML := item.String()
		if current.Len()+len(itemHTML) > 38000 {
			chunks = append(chunks, current.String())
			current.Reset()
			current.WriteString(headerHTML)
		}
		current.WriteString(itemHTML)
	}

	if current.Len() > len(headerHTML) {
		chunks = append(chunks, current.String())
	}

	if len(chunks) == 0 {
		return "", errors.New("empty content after formatting")
	}

	pageTitle := fmt.Sprintf("Torrent Search - %s", query)
	if len(pageTitle) > 35 {
		pageTitle = pageTitle[:35]
	}

	// Try domains in order
	for _, dom := range h.domainCandidates(h.DefaultDomain) {
		var paths []string
		var chunkNodes [][]any
		success := true

		for _, chunkHTML := range chunks {
			nodes, err := HTMLToNodes(chunkHTML)
			if err != nil {
				slog.Warn("HTMLToNodes failed", "domain", dom, "err", err)
				success = false
				break
			}
			chunkNodes = append(chunkNodes, nodes)
			page, err := h.createPageWithRetry(ctx, pageTitle, nodes, dom)
			if err != nil {
				slog.Warn("createPage failed on domain", "domain", dom, "err", err)
				success = false
				break
			}
			paths = append(paths, page.Path)
		}

		if success && len(paths) == len(chunks) {
			// If multi-page, add navigation bar to each page
			if len(paths) > 1 {
				for idx := range paths {
					var nav []string
					if idx > 0 {
						nav = append(nav, fmt.Sprintf("<b><a href=\"https://%s/%s\">‹ Prev</a></b>", dom, paths[idx-1]))
					}
					if idx < len(paths)-1 {
						nav = append(nav, fmt.Sprintf("<b><a href=\"https://%s/%s\">Next ›</a></b>", dom, paths[idx+1]))
					}
					if len(nav) > 0 {
						navBarHTML := fmt.Sprintf("<hr><p align='center'>%s</p>", strings.Join(nav, " &nbsp;|&nbsp; "))
						navNodes, nerr := HTMLToNodes(navBarHTML)
						if nerr == nil {
							updatedNodes := append(chunkNodes[idx], navNodes...)
							_, _ = h.editPageWithRetry(ctx, paths[idx], pageTitle, updatedNodes, dom)
						}
					}
				}
			}
			return fmt.Sprintf("https://%s/%s", dom, paths[0]), nil
		}
	}

	return "", errors.New("failed to publish to all Telegraph domain candidates")
}
