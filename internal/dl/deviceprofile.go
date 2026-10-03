package dl

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// DefaultFallbackUA is used when no device profile can be resolved.
const DefaultFallbackUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36"

var (
	uaCommentRE = regexp.MustCompile(`(?i)^#\s*(?:user-agent|useragent|ua|browser|device)\s*:\s*(.+)$`)
	mobileRE    = regexp.MustCompile(`(?i)Android|iPhone|iPad|iPod|Mobile|webOS|BlackBerry|IEMobile|Opera Mini`)
	edgeRE      = regexp.MustCompile(`(?i)Edg/(\d+)(\.[\d.]+)`)
	operaRE     = regexp.MustCompile(`(?i)(?:OPR|Opera)/(\d+)(\.[\d.]+)`)
	chromeRE    = regexp.MustCompile(`(?i)Chrome/(\d+)(\.[\d.]+)`)
	firefoxRE   = regexp.MustCompile(`(?i)Firefox/(\d+)(\.[\d.]+)`)
	safariRE    = regexp.MustCompile(`(?i)Version/(\d+)(\.[\d.]+).*Safari`)
	macVerRE    = regexp.MustCompile(`(?i)Mac OS X (\d+[._]\d+[._]\d+)`)
	androidRE   = regexp.MustCompile(`(?i)Android (\d+(?:\.\d+)*)`)
)

// ExtractUAFromCookies reads a "# User-Agent: ..." comment from cookies.txt.
func ExtractUAFromCookies(text string) string {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") {
			continue
		}
		if m := uaCommentRE.FindStringSubmatch(line); m != nil {
			ua := strings.TrimSpace(m[1])
			if len(ua) > 10 && strings.Contains(ua, "/") {
				return ua
			}
		}
	}
	return ""
}

// DeviceProfile is the parsed identity of a User-Agent string.
type DeviceProfile struct {
	UA, Engine, Brand, Major, Full, Platform, PlatformVersion string
	Mobile                                                    bool
}

// ParseUserAgent extracts engine, brand and platform hints from a UA string.
func ParseUserAgent(ua string) DeviceProfile {
	ua = strings.TrimSpace(ua)
	if ua == "" {
		ua = DefaultFallbackUA
	}
	p := DeviceProfile{UA: ua, Engine: "Chromium", Brand: "Google Chrome", Major: "133", Full: "133.0.0.0",
		Platform: "Windows", PlatformVersion: "10.0.0", Mobile: mobileRE.MatchString(ua)}
	lower := strings.ToLower(ua)
	switch {
	case strings.Contains(lower, "windows"):
		switch {
		case strings.Contains(ua, "Windows NT 6.3"):
			p.PlatformVersion = "8.1.0"
		case strings.Contains(ua, "Windows NT 6.1"):
			p.PlatformVersion = "7.0.0"
		}
	case strings.Contains(lower, "macintosh") || strings.Contains(lower, "mac os x"):
		p.Platform, p.PlatformVersion = "macOS", "14.0.0"
		if m := macVerRE.FindStringSubmatch(ua); m != nil {
			p.PlatformVersion = strings.ReplaceAll(m[1], "_", ".")
		}
	case strings.Contains(lower, "android"):
		p.Platform, p.PlatformVersion = "Android", "14.0.0"
		if m := androidRE.FindStringSubmatch(ua); m != nil {
			p.PlatformVersion = m[1]
		}
	case strings.Contains(lower, "iphone") || strings.Contains(lower, "ipad") || strings.Contains(lower, "ipod"):
		p.Platform, p.PlatformVersion = "iOS", "17.0"
	case strings.Contains(lower, "linux"):
		p.Platform, p.PlatformVersion = "Linux", "6.5.0"
	}
	set := func(engine, brand string, m []string) {
		p.Engine, p.Brand, p.Major, p.Full = engine, brand, m[1], m[1]+m[2]
	}
	chrome, firefox := chromeRE.FindStringSubmatch(ua), firefoxRE.FindStringSubmatch(ua)
	switch {
	case edgeRE.MatchString(ua):
		set("Chromium", "Microsoft Edge", edgeRE.FindStringSubmatch(ua))
	case operaRE.MatchString(ua):
		set("Chromium", "Opera", operaRE.FindStringSubmatch(ua))
	case chrome != nil:
		set("Chromium", "Google Chrome", chrome)
	case firefox != nil:
		set("Gecko", "Firefox", firefox)
	default:
		if m := safariRE.FindStringSubmatch(ua); m != nil {
			set("WebKit", "Safari", m)
		}
	}
	return p
}

// DeviceHeaders builds a header set consistent with the parsed device profile.
func DeviceHeaders(ua, destType, referer string) map[string]string {
	p := ParseUserAgent(ua)
	h := map[string]string{"User-Agent": p.UA, "Accept-Language": "en-US,en;q=0.9"}
	if referer != "" {
		h["Referer"] = referer
	}
	switch destType {
	case "image":
		h["Accept"] = "image/avif,image/webp,image/apng,image/svg+xml,image/*,*/*;q=0.8"
		h["Sec-Fetch-Dest"], h["Sec-Fetch-Mode"], h["Sec-Fetch-Site"] = "image", "no-cors", "cross-site"
	case "video":
		h["Accept"] = "*/*"
		h["Sec-Fetch-Dest"], h["Sec-Fetch-Mode"], h["Sec-Fetch-Site"] = "video", "no-cors", "cross-site"
	case "json":
		h["Accept"] = "application/json, text/plain, */*"
		h["Sec-Fetch-Dest"], h["Sec-Fetch-Mode"], h["Sec-Fetch-Site"] = "empty", "cors", "same-origin"
	default:
		h["Accept"] = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8"
		h["Upgrade-Insecure-Requests"] = "1"
		h["Sec-Fetch-Dest"], h["Sec-Fetch-Mode"], h["Sec-Fetch-Site"], h["Sec-Fetch-User"] = "document", "navigate", "same-origin", "?1"
	}
	if p.Engine == "Chromium" {
		brand := p.Brand
		h["sec-ch-ua"] = fmt.Sprintf(`"Not(A:Brand";v="99", "%s";v="%s", "Chromium";v="%s"`, brand, p.Major, p.Major)
		mobile := "?0"
		if p.Mobile {
			mobile = "?1"
		}
		h["sec-ch-ua-mobile"] = mobile
		h["sec-ch-ua-platform"] = strconv.Quote(p.Platform)
	}
	return h
}

func readTrim(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// ResolveDeviceUA picks the effective UA: explicit -> cookies comment -> user
// files -> global files -> default.
func ResolveDeviceUA(authDir string, userID int64, cookiesText, custom string) string {
	if c := strings.TrimSpace(custom); len(c) > 10 {
		return c
	}
	if ua := ExtractUAFromCookies(cookiesText); ua != "" {
		return ua
	}
	userDir := ""
	if userID > 0 {
		userDir = filepath.Join(authDir, strconv.FormatInt(userID, 10))
		if ua := ExtractUAFromCookies(readTrim(filepath.Join(userDir, "cookies.txt"))); ua != "" {
			return ua
		}
		for _, n := range []string{"user-agent.txt", "ua.txt"} {
			if s := readTrim(filepath.Join(userDir, n)); len(s) > 10 {
				return s
			}
		}
	}
	for _, p := range []string{filepath.Join(authDir, "user-agent.txt"), "user-agent.txt"} {
		if s := readTrim(p); len(s) > 10 {
			return s
		}
	}
	if ua := ExtractUAFromCookies(readTrim(filepath.Join(authDir, "cookies.txt"))); ua != "" {
		return ua
	}
	return DefaultFallbackUA
}
