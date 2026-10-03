package telegraph

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/burhanverse/tgdl/internal/torrent"
)

func TestHTMLToNodesHeadingMappingAndUnwrapping(t *testing.T) {
	htmlStr := "<div><h1>Title</h1><span>Text</span></div>"
	nodes, err := HTMLToNodes(htmlStr)
	if err != nil {
		t.Fatalf("HTMLToNodes failed: %v", err)
	}

	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2: %+v", len(nodes), nodes)
	}

	first, ok := nodes[0].(NodeElement)
	if !ok {
		t.Fatalf("expected first node to be NodeElement, got %T", nodes[0])
	}
	if first.Tag != "h3" {
		t.Errorf("got tag %s, want h3", first.Tag)
	}
	if len(first.Children) != 1 || first.Children[0] != "Title" {
		t.Errorf("got first children %+v, want ['Title']", first.Children)
	}

	second, ok := nodes[1].(string)
	if !ok || second != "Text" {
		t.Errorf("got second node %v (%T), want 'Text'", nodes[1], nodes[1])
	}
}

func TestHTMLToNodesAttributeWhitelisting(t *testing.T) {
	htmlStr := `<p style="color:red" class="main"><a href="https://example.com" onclick="alert(1)">Link</a></p>`
	nodes, err := HTMLToNodes(htmlStr)
	if err != nil {
		t.Fatalf("HTMLToNodes failed: %v", err)
	}

	if len(nodes) != 1 {
		t.Fatalf("got %d nodes, want 1", len(nodes))
	}

	pNode, ok := nodes[0].(NodeElement)
	if !ok || pNode.Tag != "p" {
		t.Fatalf("expected p NodeElement, got %+v", nodes[0])
	}
	if len(pNode.Attrs) != 0 {
		t.Errorf("p node should have no allowed attrs, got %+v", pNode.Attrs)
	}

	if len(pNode.Children) != 1 {
		t.Fatalf("expected 1 child in pNode, got %d", len(pNode.Children))
	}

	aNode, ok := pNode.Children[0].(NodeElement)
	if !ok || aNode.Tag != "a" {
		t.Fatalf("expected a NodeElement, got %+v", pNode.Children[0])
	}
	if aNode.Attrs["href"] != "https://example.com" {
		t.Errorf("got href %q, want https://example.com", aNode.Attrs["href"])
	}
	if _, hasClick := aNode.Attrs["onclick"]; hasClick {
		t.Error("onclick should be stripped from a node")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestTelegraphClientFloodWait(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":    false,
			"error": "FLOOD_WAIT_15",
		})
	}))
	defer server.Close()

	client := NewClient("test_token", "graph.org", nil)
	client.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			target, _ := url.Parse(server.URL + req.URL.Path)
			req.URL = target
			return server.Client().Transport.RoundTrip(req)
		}),
	}

	_, err := client.CreatePage(context.Background(), "Title", []any{"content"}, "", "", false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	var floodErr *RetryAfterError
	if !strings.Contains(err.Error(), "flood control, retry in 15s") {
		t.Errorf("unexpected error message: %v", err)
	}
	_ = floodErr
}

func TestHelperPublishTorrentResultsWithCinemeta(t *testing.T) {
	// Mock Cinemeta server
	cinemetaServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/catalog/") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"metas": []map[string]any{
					{"id": "tt0499549", "name": "Avatar"},
				},
			})
			return
		}
		if strings.Contains(r.URL.Path, "/meta/") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"meta": map[string]any{
					"name":        "Avatar",
					"year":        "2009",
					"poster":      "https://images.metahub.space/poster/small/tt0499549/img",
					"description": "A paraplegic Marine...",
					"imdbRating":  "7.9",
					"genres":      []string{"Action", "Sci-Fi"},
				},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer cinemetaServer.Close()

	var createdContent []any
	var createdTitle string
	telegraphServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "createAccount") {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"access_token": "token_123"},
			})
			return
		}
		if strings.Contains(r.URL.Path, "createPage") {
			createdTitle = r.FormValue("title")
			var nodes []any
			_ = json.Unmarshal([]byte(r.FormValue("content")), &nodes)
			createdContent = nodes
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":     true,
				"result": map[string]any{"path": "avatar-search-10-03"},
			})
			return
		}
		http.NotFound(w, r)
	}))
	defer telegraphServer.Close()

	helper := NewHelper()
	helper.client = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Host, "cinemeta") {
				target, _ := url.Parse(cinemetaServer.URL + req.URL.Path)
				req.URL = target
				return cinemetaServer.Client().Transport.RoundTrip(req)
			}
			target, _ := url.Parse(telegraphServer.URL + req.URL.Path)
			req.URL = target
			return telegraphServer.Client().Transport.RoundTrip(req)
		}),
	}

	results := []torrent.Result{
		{
			Name:     "Avatar 2009 Remastered 1080p",
			Size:     "2.8 GB",
			Seeders:  150,
			Leechers: 10,
			Magnet:   "magnet:?xt=urn:btih:avatar12345",
			URL:      "https://thepiratebay.org/description.php?id=123",
			Provider: "ThePirateBay",
		},
	}

	pageURL, err := helper.PublishTorrentResults(context.Background(), results, "Avatar", "ThePirateBay")
	if err != nil {
		t.Fatalf("PublishTorrentResults failed: %v", err)
	}

	if pageURL != "https://graph.org/avatar-search-10-03" {
		t.Errorf("unexpected page URL: %s", pageURL)
	}
	if !strings.Contains(createdTitle, "Avatar") {
		t.Errorf("unexpected title: %s", createdTitle)
	}
	if len(createdContent) == 0 {
		t.Errorf("expected createdContent to have nodes")
	}
}
