import pLimit from 'p-limit';
import { createCookieJar, getCookiesPath } from './cookieJar.js';
import { parseXenForoDocument } from './parser.js';
import { fetchWithCookies, resolveResource } from './resolvers.js';

/**
 * Checks if a given URL is a supported XenForo / SimpCity forum URL.
 * @param {string} url
 * @returns {boolean}
 */
export function isXenForoUrl(url) {
  if (!url || typeof url !== 'string') return false;
  const u = url.trim().toLowerCase();
  if (u.startsWith('xenforo:') || u.startsWith('simpcity:') || u.startsWith('forum:') || u.startsWith('fpd:')) {
    return true;
  }
  return /simpcity\.(cr|is|cz|hk|rs|ax|su|st|top|to)\//i.test(u) ||
         /\/threads\/[a-zA-Z0-9._-]+\.\d+/i.test(u) ||
         /\/posts\/\d+/i.test(u);
}

/**
 * Main coordinator function to scrape a XenForo thread or post.
 *
 * @param {object} opts
 * @param {string} opts.url - Target XenForo thread/post URL
 * @param {string|number|null} [opts.userId] - Optional user_id to load cookies from auth/{user_id}/cookies.txt
 * @param {string|null} [opts.cookiesTxt] - Raw Netscape cookies.txt content
 * @param {string[]} [opts.passwords] - Explicit password hints / spoilers
 * @param {number} [opts.maxPages=1] - Max number of thread pages to scrape (default 1)
 * @param {string[]|null} [opts.enabledHosts] - Whitelist of host names (null = all)
 * @param {number} [opts.concurrency=8] - Max concurrent resolver tasks
 * @param {string} [opts.baseDir] - Project root directory for cookies path lookup
 * @returns {Promise<object>}
 */
export async function scrapeXenForo({
  url,
  userId = null,
  cookiesTxt = null,
  passwords = [],
  maxPages = 1,
  enabledHosts = null,
  concurrency = 8,
  baseDir = process.cwd(),
} = {}) {
  if (!url) {
    throw new Error('URL is required for XenForo scraping.');
  }

  let cleanUrl = url.trim();
  for (const prefix of ['xenforo:', 'simpcity:', 'forum:', 'fpd:']) {
    if (cleanUrl.toLowerCase().startsWith(prefix)) {
      cleanUrl = cleanUrl.slice(prefix.length).trim();
    }
  }

  if (!/^https?:\/\//i.test(cleanUrl)) {
    cleanUrl = `https://${cleanUrl}`;
  }

  // 1. Initialize CookieJar
  const cookieJar = createCookieJar({ userId, cookiesTxt, baseDir });

  // 2. Fetch initial thread page
  let currentPageUrl = cleanUrl;
  let pagesFetched = 0;
  let allPosts = [];
  let threadTitle = 'Thread';
  const limit = pLimit(concurrency);

  while (currentPageUrl && pagesFetched < maxPages) {
    pagesFetched++;
    const res = await fetchWithCookies(currentPageUrl, { timeout: 20000, cookieJar });
    if (res.status !== 200) {
      if (pagesFetched === 1) {
        throw new Error(`Failed to load forum page (HTTP ${res.status}): ${currentPageUrl}`);
      }
      break;
    }

    const doc = parseXenForoDocument(res.data, currentPageUrl);
    if (doc.threadTitle && threadTitle === 'Thread') {
      threadTitle = doc.threadTitle;
    }

    for (const p of doc.posts) {
      allPosts.push(p);
    }

    // Stop if user requested single post or no next page
    if (cleanUrl.includes('/post-') || cleanUrl.includes('/posts/') || !doc.nextPageUrl) {
      break;
    }

    currentPageUrl = doc.nextPageUrl;
  }

  // 3. Resolve all resources across posts
  const processedPosts = [];
  let totalResources = 0;

  for (const post of allPosts) {
    const postSpoilers = [...new Set([...(post.spoilers || []), ...passwords])];
    const postFolder = `${threadTitle}/Post #${post.postNumber}`;
    const resolvedItems = [];

    // Filter hosts if enabledHosts is set
    const hostsToProcess = (post.hosts || []).filter(h => {
      if (!enabledHosts || !enabledHosts.length) return true;
      return enabledHosts.some(eh => eh.toLowerCase() === h.name.toLowerCase());
    });

    const tasks = [];
    for (const hostGroup of hostsToProcess) {
      for (const resUrl of hostGroup.resources) {
        tasks.push(
          limit(async () => {
            try {
              const items = await resolveResource(resUrl, {
                hostName: hostGroup.name,
                passwords: postSpoilers,
                cookieJar,
              });

              for (const it of items) {
                if (it && it.url) {
                  resolvedItems.push({
                    host: it.host || hostGroup.name,
                    originalUrl: resUrl,
                    resolvedUrl: it.url,
                    filename: it.name || null,
                    folderName: it.folderName || postFolder,
                    headers: it.headers || {},
                  });
                }
              }
            } catch (err) {
              // Gracefully handle resolver errors per resource
              resolvedItems.push({
                host: hostGroup.name,
                originalUrl: resUrl,
                resolvedUrl: resUrl,
                filename: null,
                folderName: postFolder,
                headers: {},
                error: err.message,
              });
            }
          })
        );
      }
    }

    await Promise.all(tasks);
    totalResources += resolvedItems.length;

    processedPosts.push({
      postId: post.postId,
      postNumber: post.postNumber,
      pageNumber: post.pageNumber,
      spoilers: postSpoilers,
      resources: resolvedItems,
    });
  }

  return {
    threadTitle,
    url: cleanUrl,
    pagesScraped: pagesFetched,
    totalPosts: processedPosts.length,
    totalResources,
    posts: processedPosts,
  };
}

/**
 * Resolves a single media / album URL directly.
 *
 * @param {object} opts
 * @param {string} opts.url
 * @param {string|number|null} [opts.userId]
 * @param {string|null} [opts.cookiesTxt]
 * @param {string[]} [opts.passwords]
 * @param {string} [opts.baseDir]
 * @returns {Promise<Array<object>>}
 */
export async function resolveSingleMedia({
  url,
  userId = null,
  cookiesTxt = null,
  passwords = [],
  baseDir = process.cwd(),
} = {}) {
  if (!url) throw new Error('URL is required.');
  const cookieJar = createCookieJar({ userId, cookiesTxt, baseDir });
  return resolveResource(url, { passwords, cookieJar });
}
