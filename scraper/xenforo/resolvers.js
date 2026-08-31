import axios from 'axios';
import * as cheerio from 'cheerio';

const DEFAULT_USER_AGENT =
  'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/151.0.7922.173 Safari/537.36';

/**
 * Helper to make HTTP GET/POST with CookieJar and custom headers.
 */
export async function fetchWithCookies(url, {
  method = 'GET',
  headers = {},
  data = null,
  cookieJar = null,
  timeout = 15000,
  responseType = 'text',
} = {}) {
  const cookieHeader = cookieJar ? cookieJar.getCookieHeader(url) : '';
  const mergedHeaders = {
    'User-Agent': DEFAULT_USER_AGENT,
    Accept: 'text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8',
    'Accept-Language': 'en-US,en;q=0.9',
    Referer: url,
    ...(cookieHeader ? { Cookie: cookieHeader } : {}),
    ...headers,
  };

  const response = await axios({
    url,
    method,
    headers: mergedHeaders,
    data,
    timeout,
    responseType,
    maxRedirects: 5,
    validateStatus: status => status < 400 || status === 403 || status === 404,
  });

  // Save Set-Cookie headers if any
  if (cookieJar && response.headers && response.headers['set-cookie']) {
    const setCookies = response.headers['set-cookie'];
    const list = Array.isArray(setCookies) ? setCookies : [setCookies];
    for (const sc of list) {
      cookieJar.setCookie(url, sc);
    }
  }

  return response;
}

/**
 * -----------------------------------------------------------------------------
 * 1. SimpCity Attachments & Video
 * -----------------------------------------------------------------------------
 */
export async function resolveSimpcityAttachment(url, { cookieJar = null, origin = 'https://simpcity.su' } = {}) {
  let cleanUrl = String(url || '').trim();
  if (cleanUrl.startsWith('//')) {
    cleanUrl = `https:${cleanUrl}`;
  } else if (cleanUrl.startsWith('/')) {
    cleanUrl = `${origin.replace(/\/$/, '')}${cleanUrl}`;
  }

  // Attachments may have ?temp_hash query params, keep as is
  const cookieHeader = cookieJar ? cookieJar.getCookieHeader(cleanUrl) : '';
  return [{
    url: cleanUrl,
    name: null,
    folderName: null,
    headers: cookieHeader ? { Cookie: cookieHeader } : {},
    host: 'Simpcity',
  }];
}

/**
 * -----------------------------------------------------------------------------
 * 2. Bunkr (Single & Albums)
 * -----------------------------------------------------------------------------
 */
const BUNKR_DOMAINS = [
  'https://bunkr.cr',
  'https://bunkr.is',
  'https://bunkr.black',
  'https://bunkr.site',
  'https://bunkrr.su',
  'https://bunkr.cat',
  'https://bunkr.media',
  'https://bunkr.ws',
];

async function signBunkrCdnUrl(rawUrl, { cookieJar = null } = {}) {
  try {
    const u = new URL(rawUrl);
    if (u.hostname.includes('cdn.cr')) {
      const signRes = await fetchWithCookies(
        `https://glb-apisign.cdn.cr/sign?path=${encodeURIComponent(u.pathname)}`,
        { timeout: 8000, cookieJar }
      );
      if (signRes.status === 200) {
        const signData = typeof signRes.data === 'object' ? signRes.data : JSON.parse(signRes.data || '{}');
        if (signData && signData.token && signData.ex) {
          u.searchParams.set('token', String(signData.token));
          u.searchParams.set('ex', String(signData.ex));
          return u.toString();
        }
      }
    }
  } catch (e) { }
  return rawUrl;
}

export async function resolveBunkrSingle(url, { cookieJar = null } = {}) {
  let u = String(url || '').trim();
  const slugMatch = /\/v\/([a-zA-Z0-9_-]+)/i.exec(u);
  const slug = slugMatch ? slugMatch[1] : '';

  // 1. If direct stream or cdn url, attempt signing
  if (/\.(mp4|mov|mkv|webm|jpg|jpeg|png|webp|gif|zip|rar|7z)(\?.*)?$/i.test(u) && !slug) {
    const signed = await signBunkrCdnUrl(u, { cookieJar });
    return [{ url: signed, name: null, folderName: null, host: 'Bunkr' }];
  }

  // 2. Try POST /api/vs on candidate domains
  if (slug) {
    for (const base of BUNKR_DOMAINS) {
      try {
        const apiRes = await fetchWithCookies(`${base}/api/vs`, {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
            Origin: base,
            Referer: `${base}/v/${slug}`,
          },
          data: JSON.stringify({ slug }),
          timeout: 8000,
          cookieJar,
        });

        if (apiRes.status === 200 && apiRes.data) {
          const data = typeof apiRes.data === 'object' ? apiRes.data : JSON.parse(apiRes.data || '{}');
          let streamUrl = data.url || data.file || (data.data && (data.data.url || data.data.file));
          let filename = data.name || data.filename || data.title || (data.data && data.data.name);

          if (streamUrl) {
            streamUrl = await signBunkrCdnUrl(streamUrl, { cookieJar });
            return [{
              url: streamUrl,
              name: filename || null,
              folderName: null,
              host: 'Bunkr',
            }];
          }
        }
      } catch (e) { }
    }

    // 3. Fallback: Scrape /v/ page directly
    for (const base of BUNKR_DOMAINS) {
      try {
        const pageRes = await fetchWithCookies(`${base}/v/${slug}`, { timeout: 8000, cookieJar });
        if (pageRes.status === 200) {
          const $ = cheerio.load(pageRes.data);
          let directSrc = $('video source, a[download], a.btn-download, a[href*="cdn.cr"], a[href*="stream."]').attr('src') ||
            $('a[download], a.btn-download, a[href*="cdn.cr"], a[href*="stream."]').attr('href');
          let filename = $('h1, title').first().text().replace(/\|\s*Bunkr.*$/i, '').trim();

          if (directSrc) {
            if (directSrc.startsWith('/')) directSrc = `${base}${directSrc}`;
            directSrc = await signBunkrCdnUrl(directSrc, { cookieJar });
            return [{
              url: directSrc,
              name: filename || null,
              folderName: null,
              host: 'Bunkr',
            }];
          }
        }
      } catch (e) { }
    }
  }

  return [{ url: u, name: null, folderName: null, host: 'Bunkr' }];
}

export async function resolveBunkrAlbum(url, { cookieJar = null } = {}) {
  let u = String(url || '').trim();
  const slugMatch = /\/a\/([a-zA-Z0-9_-]+)/i.exec(u);
  const albumSlug = slugMatch ? slugMatch[1] : '';

  for (const base of BUNKR_DOMAINS) {
    try {
      const albumUrl = albumSlug ? `${base}/a/${albumSlug}` : u;
      const res = await fetchWithCookies(albumUrl, { timeout: 12000, cookieJar });
      if (res.status === 200) {
        const $ = cheerio.load(res.data);
        const folderName = $('h1, .album-title, title').first().text().replace(/\|\s*Bunkr.*$/i, '').trim() || albumSlug || 'Bunkr Album';

        const fileLinks = [];
        $('a[href*="/v/"], .grid-images a, .grid-files a').each((_, a) => {
          let href = $(a).attr('href');
          if (href) {
            if (href.startsWith('/')) href = `${base}${href}`;
            if (href.includes('/v/') && !fileLinks.includes(href)) {
              fileLinks.push(href);
            }
          }
        });

        if (fileLinks.length) {
          const results = [];
          for (const link of fileLinks) {
            const singleItems = await resolveBunkrSingle(link, { cookieJar });
            for (const item of singleItems) {
              item.folderName = folderName;
              results.push(item);
            }
          }
          return results;
        }
      }
    } catch (e) { }
  }

  return [{ url: u, name: null, folderName: null, host: 'Bunkr' }];
}

/**
 * -----------------------------------------------------------------------------
 * 3. Filester (Single & Albums)
 * -----------------------------------------------------------------------------
 */
export async function resolveFilesterSingle(url, { cookieJar = null } = {}) {
  const u = String(url || '').trim();
  const slugMatch = /\/(?:d|v)\/([a-zA-Z0-9_-]+)/i.exec(u);
  const slug = slugMatch ? slugMatch[1] : '';
  if (!slug) return [{ url: u, name: null, folderName: null, host: 'Filester' }];

  try {
    const apiRes = await fetchWithCookies('https://filester.me/v2/api/public/download', {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json;charset=UTF-8',
        Origin: 'https://filester.me',
        Referer: `https://filester.me/d/${slug}`,
      },
      data: JSON.stringify({ file_slug: slug }),
      timeout: 12000,
      cookieJar,
    });

    if (apiRes.status === 200 && apiRes.data) {
      const j = typeof apiRes.data === 'object' ? apiRes.data : JSON.parse(apiRes.data || '{}');
      const server = String(j.server || '').replace(/\/$/, '');
      const file = String(j.file || '');
      const token = String(j.token || '');
      const name = String(j.name || '');

      if (server && file && token) {
        const filePath = file.split('/').map(encodeURIComponent).join('/');
        let streamUrl = `${server}/v2/${filePath}?token=${encodeURIComponent(token)}&download=true`;
        if (name) streamUrl += `&n=${encodeURIComponent(name)}`;
        const finalName = name || `Filester_${slug}`;

        return [{
          url: streamUrl,
          name: finalName,
          folderName: null,
          headers: { Referer: `https://filester.me/d/${slug}` },
          host: 'Filester',
        }];
      }
    }
  } catch (e) { }

  return [{ url: u, name: null, folderName: null, host: 'Filester' }];
}

export async function resolveFilesterAlbum(url, { cookieJar = null } = {}) {
  const u = String(url || '').trim();
  const slugMatch = /\/f\/([a-zA-Z0-9_-]+)/i.exec(u);
  const slug = slugMatch ? slugMatch[1] : '';

  try {
    const albumUrl = slug ? `https://filester.me/f/${slug}` : u;
    const res = await fetchWithCookies(albumUrl, { timeout: 12000, cookieJar });
    if (res.status === 200) {
      const $ = cheerio.load(res.data);
      const folderName = $('h1, title').first().text().replace(/\|\s*Filester.*$/i, '').trim() || slug || 'Filester Album';

      const fileSlugs = [];
      $('a[href*="/d/"]').each((_, a) => {
        const href = $(a).attr('href') || '';
        const m = /\/d\/([a-zA-Z0-9_-]+)/i.exec(href);
        if (m && !fileSlugs.includes(m[1])) {
          fileSlugs.push(m[1]);
        }
      });

      if (fileSlugs.length) {
        const results = [];
        for (const fSlug of fileSlugs) {
          const items = await resolveFilesterSingle(`https://filester.me/d/${fSlug}`, { cookieJar });
          for (const it of items) {
            it.folderName = folderName;
            results.push(it);
          }
        }
        return results;
      }
    }
  } catch (e) { }

  return [{ url: u, name: null, folderName: null, host: 'Filester' }];
}

/**
 * -----------------------------------------------------------------------------
 * 4. Turbo.cr (Single & Albums)
 * -----------------------------------------------------------------------------
 */
export async function resolveTurboSingle(url, { cookieJar = null } = {}) {
  const u = String(url || '').trim();
  const idMatch = /\/(?:embed|v|d)\/([a-zA-Z0-9_-]+)/i.exec(u);
  const id = idMatch ? idMatch[1] : '';
  if (!id) return [{ url: u, name: null, folderName: null, host: 'Turbo' }];

  try {
    const signUrls = [
      `https://turbo.cr/api/sign?v=${encodeURIComponent(id)}`,
      `https://turbo.cr/sign?v=${encodeURIComponent(id)}`,
    ];

    for (const sUrl of signUrls) {
      const signRes = await fetchWithCookies(sUrl, {
        headers: { Referer: `https://turbo.cr/embed/${id}`, Accept: 'application/json' },
        timeout: 8000,
        cookieJar,
      });

      if (signRes.status === 200 && signRes.data) {
        const j = typeof signRes.data === 'object' ? signRes.data : JSON.parse(signRes.data || '{}');
        if (j && j.url) {
          let signedUrl = j.url;
          const origName = j.original_filename || `turbo_${id}.mp4`;
          if (!/[?&]fn=/.test(signedUrl)) {
            signedUrl += (signedUrl.includes('?') ? '&' : '?') + 'fn=' + encodeURIComponent(origName);
          }
          return [{
            url: signedUrl,
            name: origName,
            folderName: null,
            headers: { Referer: `https://turbo.cr/embed/${id}` },
            host: 'Turbo',
          }];
        }
      }
    }
  } catch (e) { }

  return [{ url: u, name: null, folderName: null, host: 'Turbo' }];
}

export async function resolveTurboAlbum(url, { cookieJar = null } = {}) {
  const u = String(url || '').trim();
  const idMatch = /\/a\/([a-zA-Z0-9_-]+)/i.exec(u);
  const id = idMatch ? idMatch[1] : '';

  try {
    const res = await fetchWithCookies(u, { timeout: 12000, cookieJar });
    if (res.status === 200) {
      const $ = cheerio.load(res.data);
      const folderName = $('h1, title').first().text().replace(/\|\s*Turbo.*$/i, '').trim() || id || 'Turbo Album';

      const itemIds = [];
      $('a[href*="/v/"], a[href*="/embed/"], a[href*="/d/"]').each((_, a) => {
        const href = $(a).attr('href') || '';
        const m = /\/(?:v|embed|d)\/([a-zA-Z0-9_-]+)/i.exec(href);
        if (m && !itemIds.includes(m[1])) {
          itemIds.push(m[1]);
        }
      });

      if (itemIds.length) {
        const results = [];
        for (const itemId of itemIds) {
          const items = await resolveTurboSingle(`https://turbo.cr/v/${itemId}`, { cookieJar });
          for (const it of items) {
            it.folderName = folderName;
            results.push(it);
          }
        }
        return results;
      }
    }
  } catch (e) { }

  return [{ url: u, name: null, folderName: null, host: 'Turbo' }];
}

/**
 * -----------------------------------------------------------------------------
 * 5. Goonbox (Images & Albums)
 * -----------------------------------------------------------------------------
 */
export async function resolveGoonboxImage(url, { cookieJar = null } = {}) {
  const u = String(url || '').trim();
  const idMatch = /\/img\/([a-zA-Z0-9_-]+)/i.exec(u);
  const id = idMatch ? idMatch[1] : '';
  if (!id) return [{ url: u, name: null, folderName: null, host: 'Goonbox' }];

  try {
    const apiRes = await fetchWithCookies(`https://goonbox.cr/api/images/${id}`, {
      headers: { Referer: u, Accept: 'application/json' },
      timeout: 10000,
      cookieJar,
    });

    if (apiRes.status === 200 && apiRes.data) {
      const j = typeof apiRes.data === 'object' ? apiRes.data : JSON.parse(apiRes.data || '{}');
      const imgUrl = (j.image && (j.image.original_url || j.image.url)) || j.original_url || j.url;
      const name = (j.image && j.image.name) || j.name || null;
      if (imgUrl) {
        return [{
          url: imgUrl,
          name,
          folderName: null,
          headers: { Referer: u },
          host: 'Goonbox',
        }];
      }
    }
  } catch (e) { }

  // Fallback to direct URL
  return [{ url: u, name: null, folderName: null, host: 'Goonbox' }];
}

export async function resolveGoonboxAlbum(url, { cookieJar = null } = {}) {
  const u = String(url || '').trim();
  const idMatch = /\/a\/([a-zA-Z0-9._~-]+)/i.exec(u);
  const id = idMatch ? idMatch[1] : '';
  if (!id) return [{ url: u, name: null, folderName: null, host: 'Goonbox' }];

  try {
    const results = [];
    let page = 1;
    let folderName = `Goonbox Album ${id}`;

    while (page <= 20) {
      const apiRes = await fetchWithCookies(`https://goonbox.cr/api/albums/${id}/images?page=${page}`, {
        headers: { Referer: u, Accept: 'application/json' },
        timeout: 12000,
        cookieJar,
      });

      if (apiRes.status !== 200 || !apiRes.data) break;
      const j = typeof apiRes.data === 'object' ? apiRes.data : JSON.parse(apiRes.data || '{}');
      if (j.album && j.album.name) folderName = j.album.name;

      const images = j.images || (j.data && j.data.images) || [];
      if (!Array.isArray(images) || !images.length) break;

      for (const img of images) {
        const directUrl = img.original_url || img.url;
        if (directUrl) {
          results.push({
            url: directUrl,
            name: img.name || null,
            folderName,
            headers: { Referer: u },
            host: 'Goonbox',
          });
        }
      }

      if (images.length < 24) break;
      page++;
    }

    if (results.length) return results;
  } catch (e) { }

  return [{ url: u, name: null, folderName: null, host: 'Goonbox' }];
}

/**
 * -----------------------------------------------------------------------------
 * 6. JPGX / jpg.fish / jpg1-7.su / cuckcapital.cr (Single & Albums with Password)
 * -----------------------------------------------------------------------------
 */
export async function resolveJpgxSingle(url) {
  let u = String(url || '').trim();
  u = u.replace(/\.th\./i, '.').replace(/\.md\./i, '.');
  u = u.replace(/simp([1-5])\.jpg\.church/i, 'simp$1.jpg.fish');
  return [{ url: u, name: null, folderName: null, host: 'JPGX' }];
}

export async function resolveJpgxAlbum(url, { passwords = [], cookieJar = null } = {}) {
  let u = String(url || '').trim().replace(/\?.*/, '');
  try {
    let res = await fetchWithCookies(u, { timeout: 12000, cookieJar });
    let html = res.data || '';
    let $ = cheerio.load(html);

    // Check if password protected
    if (html.includes('Please enter your password to continue')) {
      const authToken = $('input[name="auth_token"]').val();
      if (authToken && passwords.length) {
        for (const pw of passwords) {
          const postBody = `auth_token=${encodeURIComponent(authToken)}&content-password=${encodeURIComponent(pw.trim())}`;
          const authRes = await fetchWithCookies(u, {
            method: 'POST',
            headers: {
              'Content-Type': 'application/x-www-form-urlencoded',
              Origin: new URL(u).origin,
              Referer: u,
            },
            data: postBody,
            timeout: 10000,
            cookieJar,
          });

          if (authRes.status === 200 && !authRes.data.includes('Please enter your password to continue')) {
            html = authRes.data;
            $ = cheerio.load(html);
            break;
          }
        }
      }
    }

    const folderName = $('meta[property="og:title"]').attr('content') || $('title').text().replace(/\|\s*JPG.*$/i, '').trim() || 'JPGX Album';
    const images = [];

    const extractPageImages = ($dom) => {
      $dom('.list-item-image a img, .image-container img').each((_, img) => {
        let src = $dom(img).attr('src') || $dom(img).attr('data-src') || '';
        if (src) {
          src = src.replace(/\.th\./i, '.').replace(/\.md\./i, '.');
          src = src.replace(/simp([1-5])\.jpg\.church/i, 'simp$1.jpg.fish');
          if (!images.includes(src)) images.push(src);
        }
      });
    };

    extractPageImages($);

    // Follow pagination
    let nextLink = $('a[data-pagination="next"], a.pagination-next').attr('href');
    let pageCount = 1;
    while (nextLink && pageCount < 20) {
      const nextUrl = new URL(nextLink, u).href;
      const nextRes = await fetchWithCookies(nextUrl, { timeout: 10000, cookieJar });
      if (nextRes.status !== 200) break;
      const $next = cheerio.load(nextRes.data);
      extractPageImages($next);
      nextLink = $next('a[data-pagination="next"], a.pagination-next').attr('href');
      pageCount++;
    }

    if (images.length) {
      return images.map(imgUrl => ({
        url: imgUrl,
        name: null,
        folderName,
        headers: { Referer: u },
        host: 'JPGX',
      }));
    }
  } catch (e) { }

  return [{ url: u, name: null, folderName: null, host: 'JPGX' }];
}

/**
 * -----------------------------------------------------------------------------
 * 7. Ibb.co (Single & Albums)
 * -----------------------------------------------------------------------------
 */
export async function resolveIbbSingle(url, { cookieJar = null } = {}) {
  const u = String(url || '').trim();
  try {
    const res = await fetchWithCookies(u, { timeout: 10000, cookieJar });
    if (res.status === 200) {
      const $ = cheerio.load(res.data);
      const direct = $('.header-content-right a, a.btn-download').attr('href') || $('meta[property="og:image"]').attr('content');
      if (direct) {
        return [{ url: direct, name: null, folderName: null, host: 'Ibb' }];
      }
    }
  } catch (e) { }
  return [{ url: u, name: null, folderName: null, host: 'Ibb' }];
}

export async function resolveIbbAlbum(url, { cookieJar = null } = {}) {
  const u = String(url || '').trim();
  const m = /\/album\/([a-zA-Z0-9_.-]+)/i.exec(u);
  const albumId = m ? m[1] : '';

  try {
    const res = await fetchWithCookies(u, { timeout: 12000, cookieJar });
    if (res.status === 200) {
      const html = res.data;
      const $ = cheerio.load(html);
      const folderName = $('meta[property="og:title"]').attr('content') || $('title').text().trim() || albumId || 'Ibb Album';
      const imageCount = parseInt($('span[data-text="image-count"]').text(), 10) || 32;
      const pageCount = Math.ceil(imageCount / 32);
      const authTokenMatch = /(?:auth_token=")([^"]+)/i.exec(html);
      const authToken = authTokenMatch ? authTokenMatch[1] : '';

      if (authToken) {
        const results = [];
        let seekEnd = '';
        for (let p = 1; p <= pageCount; p++) {
          const body = `action=list&list=images&sort=date_desc&page=${p}&from=album&albumid=${albumId}&params_hidden%5Blist%5D=images&params_hidden%5Bfrom%5D=album&params_hidden%5Balbumid%5D=${albumId}&auth_token=${authToken}&seek=${seekEnd}&items_per_page=32`;
          const apiRes = await fetchWithCookies('https://ibb.co/json', {
            method: 'POST',
            headers: { 'Content-Type': 'application/x-www-form-urlencoded', Referer: u },
            data: body,
            timeout: 10000,
            cookieJar,
          });

          if (apiRes.status === 200 && apiRes.data) {
            const parsed = typeof apiRes.data === 'object' ? apiRes.data : JSON.parse(apiRes.data || '{}');
            if (parsed && parsed.html) {
              const $html = cheerio.load(parsed.html);
              $html('[data-object]').each((_, el) => {
                try {
                  const rawObj = decodeURIComponent($html(el).attr('data-object') || '{}');
                  const obj = JSON.parse(rawObj);
                  if (obj.url) {
                    results.push({
                      url: obj.url,
                      name: obj.name || null,
                      folderName,
                      host: 'Ibb',
                    });
                  }
                } catch (e) { }
              });
              seekEnd = parsed.seekEnd || '';
            }
          }
        }
        if (results.length) return results;
      }
    }
  } catch (e) { }

  return [{ url: u, name: null, folderName: null, host: 'Ibb' }];
}

/**
 * -----------------------------------------------------------------------------
 * 8. Pixhost / Imagebam / Imgbox / Imgvb / Pixeldrain
 * -----------------------------------------------------------------------------
 */
export async function resolvePixhostGallery(url, { cookieJar = null } = {}) {
  const u = String(url || '').trim();
  try {
    const res = await fetchWithCookies(u, { timeout: 12000, cookieJar });
    if (res.status === 200) {
      const $ = cheerio.load(res.data);
      const folderName = $('h1, title').first().text().replace(/\|\s*Pixhost.*$/i, '').trim() || 'Pixhost Gallery';
      const items = [];
      $('img[src*="pixhost.to/thumbs/"], .images img').each((_, img) => {
        let src = $(img).attr('src') || $(img).attr('data-src') || '';
        if (src) {
          src = src.replace(/\/t(\d+)\./gi, 'img$1.').replace(/thumbs\//i, 'images/');
          items.push({ url: src, name: null, folderName, host: 'Pixhost' });
        }
      });
      if (items.length) return items;
    }
  } catch (e) { }
  return [{ url: u, name: null, folderName: null, host: 'Pixhost' }];
}

export async function resolveImagebam(url, { cookieJar = null } = {}) {
  const u = String(url || '').trim();
  try {
    const res = await fetchWithCookies(u, { timeout: 12000, cookieJar });
    if (res.status === 200) {
      const $ = cheerio.load(res.data);
      const direct = $('img.main-image, a.download-link').attr('src') || $('a.download-link').attr('href');
      if (direct) {
        return [{ url: direct, name: null, folderName: null, host: 'Imagebam' }];
      }

      // Gallery
      const items = [];
      $('a[href*="/view/"]').each((_, a) => {
        const href = $(a).attr('href');
        if (href) items.push(href);
      });
      if (items.length) {
        const results = [];
        for (const itemHref of items) {
          const subRes = await fetchWithCookies(itemHref, { timeout: 8000, cookieJar });
          if (subRes.status === 200) {
            const $sub = cheerio.load(subRes.data);
            const subImg = $sub('img.main-image').attr('src');
            if (subImg) results.push({ url: subImg, name: null, folderName: 'Imagebam Gallery', host: 'Imagebam' });
          }
        }
        if (results.length) return results;
      }
    }
  } catch (e) { }
  return [{ url: u, name: null, folderName: null, host: 'Imagebam' }];
}

export async function resolveImgbox(url, { cookieJar = null } = {}) {
  const u = String(url || '').trim();
  if (u.includes('/g/')) {
    try {
      const res = await fetchWithCookies(u, { timeout: 12000, cookieJar });
      if (res.status === 200) {
        const $ = cheerio.load(res.data);
        const folderName = $('#gallery-view h1, title').first().text().trim() || 'Imgbox Gallery';
        const items = [];
        $('#gallery-view-content img').each((_, img) => {
          let src = $(img).attr('src') || '';
          if (src) {
            src = src.replace(/_t\./gi, '_o.').replace(/thumbs/i, 'images');
            items.push({ url: src, name: null, folderName, host: 'Imgbox' });
          }
        });
        if (items.length) return items;
      }
    } catch (e) { }
  }
  const clean = u.replace(/_t\./gi, '_o.').replace(/thumbs/i, 'images');
  return [{ url: clean, name: null, folderName: null, host: 'Imgbox' }];
}

export async function resolvePixeldrain(url) {
  const u = String(url || '').trim();
  const fileMatch = /\/u\/([a-zA-Z0-9_-]+)/i.exec(u);
  if (fileMatch) {
    const id = fileMatch[1];
    return [{
      url: `https://pixeldrain.com/api/file/${id}?download`,
      name: `pixeldrain_${id}`,
      folderName: null,
      host: 'Pixeldrain',
    }];
  }

  const listMatch = /\/l\/([a-zA-Z0-9_-]+)/i.exec(u);
  if (listMatch) {
    const listId = listMatch[1];
    try {
      const res = await fetchWithCookies(`https://pixeldrain.com/api/list/${listId}`, { timeout: 10000 });
      if (res.status === 200 && res.data) {
        const j = typeof res.data === 'object' ? res.data : JSON.parse(res.data || '{}');
        const files = j.files || [];
        const folderName = j.title || `Pixeldrain List ${listId}`;
        return files.map(f => ({
          url: `https://pixeldrain.com/api/file/${f.id}?download`,
          name: f.name || `pixeldrain_${f.id}`,
          folderName,
          host: 'Pixeldrain',
        }));
      }
    } catch (e) { }
  }

  return [{ url: u, name: null, folderName: null, host: 'Pixeldrain' }];
}

/**
 * -----------------------------------------------------------------------------
 * 9. Master Resource Dispatcher
 * -----------------------------------------------------------------------------
 */
export async function resolveResource(resource, { hostName = '', passwords = [], cookieJar = null } = {}) {
  const u = String(resource || '').trim();
  if (!u) return [];

  // SimpCity attachments
  if (/\/attachments\/|\/data\/video\//i.test(u)) {
    return resolveSimpcityAttachment(u, { cookieJar });
  }

  // Bunkr
  if (/bunkrr?r?\.(ac|ax|black|cat|ci|cr|fi|is|media|nu|pk|ph|ps|red|ru|se|si|site|sk|ws|su|org)/i.test(u)) {
    if (/\/a\//i.test(u)) {
      return resolveBunkrAlbum(u, { cookieJar });
    }
    return resolveBunkrSingle(u, { cookieJar });
  }

  // Filester
  if (/filester\.(me|sh|si|gg)/i.test(u)) {
    if (/\/f\//i.test(u)) {
      return resolveFilesterAlbum(u, { cookieJar });
    }
    return resolveFilesterSingle(u, { cookieJar });
  }

  // Turbo
  if (/turbo\.cr/i.test(u)) {
    if (/\/a\//i.test(u)) {
      return resolveTurboAlbum(u, { cookieJar });
    }
    return resolveTurboSingle(u, { cookieJar });
  }

  // Goonbox
  if (/goonbox\.cr/i.test(u)) {
    if (/\/a\//i.test(u)) {
      return resolveGoonboxAlbum(u, { cookieJar });
    }
    return resolveGoonboxImage(u, { cookieJar });
  }

  // JPGX
  if (/cuckcapital\.cr|jpg\d?\.(church|fish|fishing|pet|su|cr)/i.test(u)) {
    if (/\/a\/|\/album\//i.test(u)) {
      return resolveJpgxAlbum(u, { passwords, cookieJar });
    }
    return resolveJpgxSingle(u);
  }

  // Ibb
  if (/ibb\.co/i.test(u)) {
    if (/\/album\//i.test(u)) {
      return resolveIbbAlbum(u, { cookieJar });
    }
    return resolveIbbSingle(u, { cookieJar });
  }

  // Pixhost
  if (/pixhost\.to/i.test(u)) {
    if (/\/gallery\//i.test(u)) {
      return resolvePixhostGallery(u, { cookieJar });
    }
    const clean = u.replace(/\/t(\d+)\./gi, 'img$1.').replace(/thumbs\//i, 'images/');
    return [{ url: clean, name: null, folderName: null, host: 'Pixhost' }];
  }

  // Imagebam
  if (/imagebam\.com/i.test(u)) {
    return resolveImagebam(u, { cookieJar });
  }

  // Imgbox
  if (/imgbox\.com/i.test(u)) {
    return resolveImgbox(u, { cookieJar });
  }

  // Pixeldrain
  if (/pixeldrain\.(com|net|in)/i.test(u)) {
    return resolvePixeldrain(u);
  }

  // Pomf2
  if (/pomf2\.lain\.la/i.test(u)) {
    const clean = u.replace(/pomf2\.lain\.la\/f\/(.*)\.(\w{3,4})(\?.*)?/i, 'pomf2.lain.la/f/$1.$2');
    return [{ url: clean.startsWith('http') ? clean : `https://${clean}`, name: null, folderName: null, host: 'Pomf2' }];
  }

  // Postimg
  if (/(postimg|pixxxels)\.cc/i.test(u)) {
    const clean = u.replace(/https?:\/\/(www\.)?i\.?(postimg|pixxxels)\.cc\/([a-zA-Z0-9]{8})(.*)/i, 'https://postimg.cc/$3');
    try {
      const res = await fetchWithCookies(clean, { timeout: 8000, cookieJar });
      if (res.status === 200) {
        const $ = cheerio.load(res.data);
        const direct = $('.controls a, a#download').attr('href');
        if (direct) return [{ url: direct, name: null, folderName: null, host: 'Postimg' }];
      }
    } catch (e) { }
  }

  // Twitter / Twimg
  if (/twimg\.com/i.test(u) || /nitter\..*\/pic/i.test(u)) {
    const clean = u.replace(/https?:\/\/nitter\.(.{1,20})\/pic\/(orig\/)?media%2F(.{1,15})/i, 'https://pbs.twimg.com/media/$3');
    return [{ url: clean, name: null, folderName: null, host: 'Twitter' }];
  }

  // Default passthrough
  return [{ url: u, name: null, folderName: null, host: hostName || 'Direct' }];
}
