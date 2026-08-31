import * as cheerio from 'cheerio';

/**
 * Host matcher definitions ported from XenForoPostDownloader (build.user.js).
 * [0: signature(name:category), 1: [single_pattern, album_pattern]]
 */
export const HOST_DEFINITIONS = [
  ['Simpcity:Attachments', [/(\/attachments\/|\/data\/video\/)/]],
  ['Coomer:Profiles', [/coomer\.(st|party)\/[a-zA-Z0-9._-]+\/user/]],
  ['Coomer:image', [/(\w+\.)?coomer\.(st|party)\/(data|thumbnail)/]],
  ['JPGX:image', [
    /(simp\d+\.)?(cuckcapital\.cr|jpg\d?\.(church|fish|fishing|pet|su|cr))\/(?!(img\/|a\/|album\/))/,
    /jpe?g\d?\.(church|fish|fishing|pet|su|cr)(\/a\/|\/album\/)[a-zA-Z0-9-_.]+/,
  ]],
  ['Goonbox:image', [/goonbox\.cr\/img\//, /goonbox\.cr\/a\//]],
  ['kemono:direct link', [/.{2,6}\.kemono\.(cr|party)\/data\//]],
  ['Postimg:image', [/https?:\/\/(www\.)?i\.?(postimg|pixxxels)\.cc\/([a-zA-Z0-9]{8})/]],
  ['Ibb:image', [
    /https?:\/\/(www\.)?([a-z](\d+)?\.)?ibb\.co\/([a-zA-Z0-9_.-]){7}(?:\/|\b)/,
    /ibb\.co\/album\/[a-zA-Z0-9_.-]+/,
  ]],
  ['Ibb:direct link', [/https?:\/\/(www\.)?([a-z](\d+)?\.)?ibb\.co\/([a-zA-Z0-9_.-]){7}/]],
  ['Imagevenue:image', [/https?:\/\/(www\.)?imagevenue\.com\/([a-zA-Z0-9]{8})/]],
  ['Imgvb:image', [/imgvb\.com\/images\//, /imgvb\.com\/album/]],
  ['Imgbox:image', [/(thumbs|images)(\d+)?\.imgbox\.com\//, /imgbox\.com\/g\//]],
  ['Onlyfans:image', [/public\.onlyfans\.com\/files/]],
  ['Reddit:image', [/(\w+)?\.redd\.it/]],
  ['Pomf2:File', [/pomf2\.lain\.la/]],
  ['Nitter:image', [/nitter\.(.{1,20})\/pic/]],
  ['Twitter:image', [/([a-zA-Z0-9.]+\.)?twimg\.com\//]],
  ['Pixhost:image', [/(t|img)(\d+)?\.pixhost\.to\//, /pixhost\.to\/gallery\//]],
  ['Imagebam:image', [/imagebam\.com\/(view|gallery)/]],
  ['Imagebam:full embed', [/images\d?\.imagebam\.com/]],
  ['turbo:video', [/([\w-]+\.)?turbo\.cr\/(embed|v|d)\//]],
  ['turbo:albums', [/([\w-]+\.)?turbo\.cr\/a\//]],
  ['Redgifs:video', [/redgifs\.com(\/|\\\/)ifr.*?(?=["']|&quot;|\s|$)/]],
  ['Redgifs:user', [/redgifs\.com\/users\//]],
  ['Bunkr:', [
    /https:\/\/((stream|cdn(\d+)?)\.)?bunkrr?r?\.(ac|ax|black|cat|ci|cr|fi|is|media|nu|pk|ph|ps|red|ru|se|si|site|sk|ws|su|org)(?!(\/a\/)).*?\.[a-zA-Z0-9]{3,4}|https:\/\/((i|cdn|i-pizza|big-taco-1img)(\d+)?\.)?bunkrr?r?\.(ac|ax|black|cat|ci|cr|fi|is|media|nu|pk|ph|ps|red|ru|se|si|site|sk|ws|su|org)(?!(\/a\/))\/(v\/)?[a-zA-Z0-9_-]+/i,
  ]],
  ['Bunkr:Albums', [/bunkrr?r?\.(ac|ax|black|cat|ci|cr|fi|is|media|nu|pk|ph|ps|red|ru|se|si|site|sk|ws|su|org)\/a\//i]],
  ['Give.xxx:Profiles', [/give\.xxx\/[a-zA-Z0-9_-]+/]],
  ['Pixeldrain:', [/(focus\.)?(?:pixeldrain\.com|pixeldrain\.net|pixeldra\.in)\/[lu]\//]],
  ['Gofile:', [/gofile\.io\/d/]],
  ['Filester:links', [/filester\.(me|sh|si|gg)\/d\//]],
  ['Filester:albums', [/filester\.(me|sh|si|gg)\/f\/[a-zA-Z0-9-_.]+/]],
  ['Box.com:', [/m\.box\.com\//]],
  ['Yandex:', [/(disk\.)?yandex\.[a-z]+/]],
  ['Cyberfile:', [/https:\/\/cyberfile\.(su|me)\/\w+/i, /cyberfile\.(su|me)\/folder\//i]],
  ['Cyberdrop:', [/fs-\d+\.cyberdrop\.[a-z]{2,}\/|cyberdrop\.[a-z]{2,}\/(f|e)\//i, /cyberdrop\.[a-z]{2,}\/a\//i]],
  ['Pornhub:video', [/([a-zA-Z0-9._-]+\.)?pornhub\.com\/view_video/]],
  ['Noodlemagazine:video', [/(adult\.)?noodlemagazine\.com\/watch\//]],
  ['Spankbang:video', [/spankbang\.com\/.*?\/video/]],
];

/**
 * Sanitizes thread title by replacing emojis and invalid path characters.
 * @param {string} title
 * @returns {string}
 */
export function sanitizeTitle(title) {
  if (!title) return 'Thread';
  const emojisPattern =
    /[\u{1f300}-\u{1f5ff}\u{1f900}-\u{1f9ff}\u{1f600}-\u{1f64f}\u{1f680}-\u{1f6ff}\u{2600}-\u{26ff}\u{2700}-\u{27bf}\u{1f191}-\u{1f251}\u{1f004}\u{1f0cf}\u{1f170}-\u{1f171}\u{1f17e}-\u{1f17f}\u{1f18e}\u{3030}\u{2b50}\u{2b55}\u{2934}-\u{2935}\u{2b05}-\u{2b07}\u{2b1b}-\u{2b1c}\u{3297}\u{3299}\u{303d}\u{00a9}\u{00ae}\u{2122}\u{23f3}\u{24c2}\u{23e9}-\u{23ef}\u{25b6}\u{23f8}-\u{23fa}]/gu;

  let clean = title.replace(emojisPattern, '-');
  clean = clean.replace(/[\\/:*?"<>|]/g, '-').replace(/\s+/g, ' ').trim();
  return clean || 'Thread';
}

/**
 * Decodes XenForo outbound redirect protection (e.g. /redirect/?to=...&m=b64 or URL encoded).
 * @param {string} href
 * @param {string} origin
 * @returns {string|null}
 */
export function decodeForumRedirect(href, origin = '') {
  if (!href) return null;
  try {
    const base = origin || 'https://simpcity.su';
    const u = new URL(href, base);
    const p = (u.pathname || '').toLowerCase();

    const looksRedirect = p === '/redirect' || p === '/redirect/' || p.startsWith('/redirect/');
    const looksLinkProxy = p.includes('link-proxy');
    if (!looksRedirect && !looksLinkProxy) return null;

    const to =
      u.searchParams.get('to') ||
      u.searchParams.get('url') ||
      u.searchParams.get('u') ||
      u.searchParams.get('link') ||
      u.searchParams.get('target');
    if (!to) return null;

    const mode = (u.searchParams.get('m') || '').toLowerCase();
    let decoded = null;

    const decodeB64 = (s) => {
      try {
        let b = String(s).trim().replace(/-/g, '+').replace(/_/g, '/');
        while (b.length % 4) b += '=';
        return Buffer.from(b, 'base64').toString('utf8');
      } catch (e) {
        return null;
      }
    };

    if (mode === 'b64' || mode === 'base64') {
      decoded = decodeB64(to);
    }

    if (!decoded) {
      const looksB64 = /^[A-Za-z0-9+/_-]+={0,2}$/.test(to) && to.length >= 16 && to.length % 4 !== 1;
      if (looksB64) decoded = decodeB64(to);
    }

    if (!decoded) {
      try {
        decoded = decodeURIComponent(to);
      } catch (e) {
        decoded = to;
      }
    }

    decoded = String(decoded || '').trim();

    if (decoded && !/^https?:\/\//i.test(decoded) && /%3a%2f%2f/i.test(decoded)) {
      try {
        const d2 = decodeURIComponent(decoded);
        if (/^https?:\/\//i.test(d2)) decoded = d2;
      } catch (e) {}
    }

    if (!/^https?:\/\//i.test(decoded)) return null;
    return decoded;
  } catch (e) {
    return null;
  }
}

/**
 * Extracts spoilers & passwords from post element.
 * @param {cheerio.CheerioAPI} $
 * @param {cheerio.Cheerio<cheerio.Element>} $post
 * @returns {string[]}
 */
export function extractSpoilers($, $post) {
  const spoilers = [];

  // Spoiler blocks
  $post.find('.bbCodeBlock--spoiler > .bbCodeBlock-content, .bbCodeInlineSpoiler').each((_, el) => {
    const $el = $(el);
    if (!$el.find('.bbCodeBlock--unfurl').length) {
      const txt = $el.text().trim();
      if (txt) spoilers.push(txt);
    }
  });

  // Password patterns in post text
  const fullText = $post.text();
  const pwRegex = /(?:pw|pass|passwd|password)(?:\s:|:)?\s+?([a-zA-Z0-9~!@#$%^&*()_+{}|:'"<>?/,;.]+)/gi;
  let match;
  while ((match = pwRegex.exec(fullText)) !== null) {
    if (match[1]) {
      let pw = match[1].trim();
      pw = pw.replace(/^:/, '').replace(/\bp:\b/i, '').replace(/\bpw:\b/i, '').replace(/\bkey:\b/i, '').trim();
      if (pw) spoilers.push(pw);
    }
  }

  // Deduplicate and filter non-empty
  return [...new Set(spoilers.filter(Boolean))];
}

/**
 * Parses XenForo thread HTML document.
 * @param {string} html
 * @param {string} pageUrl
 * @returns {{threadTitle: string, pageNumber: number, nextPageUrl: string|null, posts: Array<object>}}
 */
export function parseXenForoDocument(html, pageUrl = '') {
  const $ = cheerio.load(html);

  // 1. Thread Title
  let rawTitle = $('.p-title-value').text().trim();
  if (!rawTitle) {
    rawTitle = $('title').text().replace(/\|.*$/, '').trim();
  }
  const threadTitle = sanitizeTitle(rawTitle);

  // 2. Page Number
  let pageNumber = 1;
  const pageMatch = /(?:\/page-|\bpage=)(\d+)/i.exec(pageUrl);
  if (pageMatch) {
    pageNumber = parseInt(pageMatch[1], 10) || 1;
  }

  // 3. Next Page URL
  let nextPageUrl = null;
  const nextAnchor = $('a.pageNav-jump--next, a[rel="next"], a[data-pagination="next"]').first();
  if (nextAnchor.length) {
    const href = nextAnchor.attr('href');
    if (href) {
      try {
        nextPageUrl = new URL(href, pageUrl || 'https://simpcity.su').href;
      } catch (e) {}
    }
  }

  // 4. Targeted post ID if URL points to single post permalink (e.g. /post-12345 or /posts/12345)
  let targetPostId = null;
  const targetPostMatch = /(?:post-|posts\/)(\d+)/i.exec(pageUrl);
  if (targetPostMatch) {
    targetPostId = targetPostMatch[1];
  }

  // 5. Extract Posts
  const posts = [];
  const messageNodes = $('article.message--post, div.message--post, .message[data-content]').toArray();
  const nodesToProcess = messageNodes.length > 0 ? messageNodes : $('.message').toArray();

  for (const node of nodesToProcess) {
    const $msg = $(node);

    // Find post ID
    let postId = $msg.attr('data-content') || '';
    if (postId.startsWith('post-')) {
      postId = postId.slice(5);
    }
    if (!postId) {
      const permalink = $msg.find('a[href*="/post-"], a[href*="/posts/"]').last().attr('href') || '';
      const m = /(?:post-|posts\/)(\d+)/i.exec(permalink);
      if (m) postId = m[1];
    }
    if (!postId) {
      const nodeAttr = $msg.attr('id') || '';
      const m = /post-(\d+)/i.exec(nodeAttr);
      if (m) postId = m[1];
    }

    // Filter by single target post if requested
    if (targetPostId && postId && postId !== targetPostId) {
      continue;
    }

    // Find post number
    let postNumber = $msg.find('ul.message-attribution-opposite li:last-child a, .message-attribution a[href*="post-"]').last().text().replace('#', '').trim();
    if (!postNumber) postNumber = postId || `${posts.length + 1}`;

    // Extract message content
    const $userContent = $msg.find('.message-userContent, .message-content').first();
    if (!$userContent.length) continue;

    // Clone content for cleaning
    const $clone = $userContent.clone();

    // Spoilers and password hints
    const spoilers = extractSpoilers($, $clone);

    // Clean post HTML:
    // 1. Remove quotes quoting other posts
    $clone.find('blockquote').each((_, b) => {
      if ($(b).find('.bbCodeBlock-title').length) {
        $(b).remove();
      }
    });

    // 2. Remove icons, unfurls, badges
    $clone.find('.contentRow-figure, .js-unfurl-favicon, .button-text > span, .bbCodeBlock--unfurl').remove();

    // 3. Remove thread links
    $clone.find('.contentRow-header > a[href*="/threads"]').closest('.contentRow').remove();

    // 4. Remove preview images inside attachment links or goonbox links to prevent duplicates
    $clone.find('a[href*="/attachments/"] img, a[href*="goonbox.cr"] img').remove();

    // 5. Decode forum redirects
    const redirectSel = 'a[href*="/redirect/"], a[href^="/redirect"], a[href*="redirect?"], a[href*="link-proxy"]';
    $clone.find(redirectSel).each((_, a) => {
      const $a = $(a);
      const candidates = [$a.attr('href'), $a.attr('data-href'), $a.attr('data-url')].filter(Boolean);
      for (const c of candidates) {
        const decoded = decodeForumRedirect(c, pageUrl);
        if (decoded) {
          $a.attr('href', decoded);
          $a.attr('data-url', decoded);
          $a.attr('data-xfpd-decoded', '1');
          break;
        }
      }
    });

    // Remove preview thumbnails inside decoded redirect links
    $clone.find('a[data-xfpd-decoded="1"] img').each((_, img) => {
      const $img = $(img);
      const src = ($img.attr('data-url') || $img.attr('src') || '').trim();
      if (/thumb|_t\.(jpe?g|png|webp|gif)$|\/thumbs?\//i.test(src)) {
        $img.remove();
      }
    });

    // Extract raw and text content
    const cleanedHtml = $clone.html() || '';
    const textContent = $clone.text() || '';

    // Match hosts
    const parsedHosts = matchHostsInPost(cleanedHtml, pageUrl);

    posts.push({
      postId,
      postNumber,
      pageNumber,
      spoilers,
      cleanedHtml,
      textContent,
      hosts: parsedHosts,
    });
  }

  return {
    threadTitle,
    pageNumber,
    nextPageUrl,
    posts,
  };
}

/**
 * Scans post HTML and matches against all host definitions.
 * @param {string} postHtml
 * @param {string} originUrl
 * @returns {Array<{name: string, type: 'single'|'album', category: string, resources: string[]}>}
 */
export function matchHostsInPost(postHtml, originUrl = '') {
  if (!postHtml) return [];
  const results = [];

  for (const [signature, matchers] of HOST_DEFINITIONS) {
    if (!matchers || !matchers.length) continue;

    const [name, categoryRaw = 'misc'] = signature.split(':');
    const singleMatcher = matchers[0];
    const albumMatcher = matchers.length > 1 ? matchers[1] : null;

    const execPattern = (pattern) => {
      if (!pattern) return [];
      const matches = [];

      // Look for href, src, data-url attributes or plain URLs in text
      const attrRegex = /(?:href|src|data-url|data-src)=["']([^"']+)["']/gi;
      let m;
      while ((m = attrRegex.exec(postHtml)) !== null) {
        const val = m[1].trim();
        if (pattern.test(val)) {
          matches.push(val);
        }
      }

      // Also regex match within raw html
      const rawMatches = postHtml.match(new RegExp(pattern.source, 'gi')) || [];
      for (const rm of rawMatches) {
        // Clean URL
        let u = rm.replace(/&amp;/g, '&').split(/[\s"'<>]/)[0].trim();
        if (u && !/^https?:\/\//i.test(u) && !u.startsWith('/')) {
          u = `https://${u}`;
        }
        if (u && pattern.test(u)) {
          matches.push(u);
        }
      }

      // Normalize and deduplicate
      const cleanUrls = matches.map(u => {
        let clean = u.replace(/&amp;/g, '&').trim();
        if (clean.startsWith('//')) {
          clean = `https:${clean}`;
        } else if (clean.startsWith('/') && originUrl) {
          try {
            clean = new URL(clean, originUrl).href;
          } catch (e) {}
        }
        return clean;
      });

      return [...new Set(cleanUrls.filter(Boolean))];
    };

    if (singleMatcher) {
      const matched = execPattern(singleMatcher);
      if (matched.length) {
        results.push({
          name,
          type: 'single',
          category: categoryRaw.split(',')[0] || 'Links',
          resources: matched,
        });
      }
    }

    if (albumMatcher) {
      const matched = execPattern(albumMatcher);
      if (matched.length) {
        results.push({
          name,
          type: 'album',
          category: `${categoryRaw.split(',')[1] || categoryRaw.split(',')[0] || 'Links'} Albums`,
          resources: matched,
        });
      }
    }
  }

  return results;
}
