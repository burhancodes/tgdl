package telegraph

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

var (
	reWhitespace = regexp.MustCompile(`\s+`)

	allowedTags = map[string]bool{
		"a": true, "aside": true, "b": true, "blockquote": true, "br": true, "code": true,
		"em": true, "figcaption": true, "figure": true, "h3": true, "h4": true, "hr": true,
		"i": true, "iframe": true, "img": true, "li": true, "ol": true, "p": true, "pre": true,
		"s": true, "strong": true, "u": true, "ul": true, "video": true,
	}

	allowedAttrs = map[string]map[string]bool{
		"a":      {"href": true},
		"img":    {"src": true},
		"video":  {"src": true},
		"iframe": {"src": true},
	}

	tagMap = map[string]string{
		"h1": "h3",
		"h2": "h3",
		"h5": "h4",
		"h6": "h4",
	}

	unwrapTags = map[string]bool{
		"div": true, "span": true, "article": true, "section": true, "header": true,
		"footer": true, "main": true, "html": true, "body": true, "font": true,
		"center": true, "tbody": true, "thead": true, "tfoot": true, "tr": true,
		"td": true, "th": true, "table": true,
	}

	ignoreTags = map[string]bool{
		"script": true, "style": true, "head": true, "meta": true, "title": true,
	}
)

// NodeElement is a Telegraph DOM element node.
type NodeElement struct {
	Tag      string            `json:"tag"`
	Attrs    map[string]string `json:"attrs,omitempty"`
	Children []any             `json:"children,omitempty"`
}

// HTMLToNodes converts an HTML string into Telegraph DOM nodes.
func HTMLToNodes(htmlStr string) ([]any, error) {
	doc, err := html.Parse(strings.NewReader(htmlStr))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	// Locate the <body> node, or root
	var body *html.Node
	var findBody func(*html.Node)
	findBody = func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.Body {
			body = n
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			findBody(c)
			if body != nil {
				return
			}
		}
	}
	findBody(doc)

	target := doc
	if body != nil {
		target = body
	}

	var nodes []any
	for c := target.FirstChild; c != nil; c = c.NextSibling {
		nodes = append(nodes, processNode(c, false)...)
	}

	return cleanNodes(nodes), nil
}

func processNode(n *html.Node, inPre bool) []any {
	if n == nil {
		return nil
	}

	switch n.Type {
	case html.TextNode:
		text := n.Data
		if !inPre {
			text = reWhitespace.ReplaceAllString(text, " ")
		}
		if text == "" || (!inPre && strings.TrimSpace(text) == "" && text != " ") {
			return nil
		}
		return []any{text}

	case html.ElementNode:
		tag := strings.ToLower(n.Data)
		if ignoreTags[tag] {
			return nil
		}

		if mapped, ok := tagMap[tag]; ok {
			tag = mapped
		}

		nowInPre := inPre || tag == "pre"

		if unwrapTags[tag] || !allowedTags[tag] {
			// Unwrap children directly into parent list
			var unwrapped []any
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				unwrapped = append(unwrapped, processNode(c, nowInPre)...)
			}
			return unwrapped
		}

		// Extract allowed attributes
		var attrs map[string]string
		if allowed, ok := allowedAttrs[tag]; ok {
			for _, a := range n.Attr {
				k := strings.ToLower(a.Key)
				if allowed[k] {
					if attrs == nil {
						attrs = make(map[string]string)
					}
					attrs[k] = a.Val
				}
			}
		}

		// Collect children
		var children []any
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			children = append(children, processNode(c, nowInPre)...)
		}

		elem := NodeElement{
			Tag:   tag,
			Attrs: attrs,
		}
		if len(children) > 0 {
			elem.Children = cleanNodes(children)
		}
		return []any{elem}

	default:
		return nil
	}
}

// cleanNodes consolidates adjacent string nodes and trims leading/trailing empty nodes.
func cleanNodes(nodes []any) []any {
	var out []any
	for _, n := range nodes {
		str, isStr := n.(string)
		if isStr {
			if len(out) > 0 {
				if prevStr, prevIsStr := out[len(out)-1].(string); prevIsStr {
					out[len(out)-1] = prevStr + str
					continue
				}
			}
			if str == "" {
				continue
			}
		}
		out = append(out, n)
	}
	return out
}

// NodesToJSON converts DOM nodes into a compact JSON string required by Telegraph API.
func NodesToJSON(nodes []any) (string, error) {
	b, err := json.Marshal(nodes)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
