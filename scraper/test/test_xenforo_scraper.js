import assert from 'assert';
import fs from 'fs';
import path from 'path';
import { CookieJar, createCookieJar } from '../xenforo/cookieJar.js';
import {
  DEFAULT_FALLBACK_UA,
  extractUserAgentFromCookies,
  getDeviceHeaders,
  parseUserAgent,
  resolveUserAgent,
} from '../xenforo/deviceProfile.js';
import { sanitizeTitle, decodeForumRedirect, parseXenForoDocument, matchHostsInPost } from '../xenforo/parser.js';
import { isXenForoUrl } from '../xenforo/index.js';
import { resolveResource } from '../xenforo/resolvers.js';

console.log('--- Running XenForo Scraper Node.js Tests ---');

// 1. Test CookieJar & Device UA Extraction
console.log('Testing CookieJar & Device UA Extraction...');
const testCookies = `# Netscape HTTP Cookie File
# http://curl.haxx.se/rfc/cookie_spec.html
# User-Agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36
.simpcity.su\tTRUE\t/\tTRUE\t1999999999\txf_user\t12345%2Cabcd
simpcity.su\tFALSE\t/\tTRUE\t1999999999\txf_session\tsess_token_xyz
#HttpOnly_.gofile.io\tTRUE\t/\tTRUE\t1999999999\taccountToken\tgofile_guest_token
coomer.st\tFALSE\t/user\tFALSE\t1999999999\tsession\tcoomer_sess
`;

const jar = new CookieJar();
jar.parseNetscape(testCookies);

assert.strictEqual(
  jar.getUserAgent(),
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36',
  'CookieJar should automatically extract User-Agent from cookies comments'
);

const simpCookie = jar.getCookieHeader('https://simpcity.su/threads/test.123/');
assert(simpCookie.includes('xf_user=12345%2Cabcd'), 'CookieJar should match .simpcity.su subdomain cookies');
assert(simpCookie.includes('xf_session=sess_token_xyz'), 'CookieJar should match simpcity.su exact cookies');

const gofileCookie = jar.getCookieHeader('https://store1.gofile.io/contents');
assert(gofileCookie.includes('accountToken=gofile_guest_token'), 'CookieJar should match #HttpOnly_ cookies with subdomains');

const unauthCookie = jar.getCookieHeader('https://example.com/test');
assert.strictEqual(unauthCookie, '', 'CookieJar should not send cookies to unmatched domains');
console.log('✓ CookieJar tests passed!');

// 2. Test Device Profile & Client Hints Generation
console.log('Testing Device Profile & Client Hints...');

// Test 2a: Windows Chrome
const winChromeUa = 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36';
const winProfile = parseUserAgent(winChromeUa);
assert.strictEqual(winProfile.brand, 'Google Chrome');
assert.strictEqual(winProfile.platform, 'Windows');
assert.strictEqual(winProfile.isMobile, false);

const winHeaders = getDeviceHeaders(winChromeUa);
assert(winHeaders['sec-ch-ua'].includes('Google Chrome'), 'Should include Google Chrome brand');
assert(winHeaders['sec-ch-ua'].includes('Chromium'), 'Should include Chromium brand');
assert.strictEqual(winHeaders['sec-ch-ua-platform'], '"Windows"');
assert.strictEqual(winHeaders['sec-ch-ua-mobile'], '?0');
assert.strictEqual(winHeaders['Sec-Fetch-Dest'], 'document');

// Test 2b: macOS Safari
const macSafariUa = 'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.2 Safari/605.1.15';
const macProfile = parseUserAgent(macSafariUa);
assert.strictEqual(macProfile.brand, 'Safari');
assert.strictEqual(macProfile.platform, 'macOS');
assert.strictEqual(macProfile.isMobile, false);

const macHeaders = getDeviceHeaders(macSafariUa);
assert.strictEqual(macHeaders['sec-ch-ua'], undefined, 'Safari must NEVER send sec-ch-ua (anti-bot signature prevention)');
assert.strictEqual(macHeaders['sec-ch-ua-platform'], undefined);
assert.strictEqual(macHeaders['User-Agent'], macSafariUa);

// Test 2c: Linux Firefox
const linuxFfUa = 'Mozilla/5.0 (X11; Linux x86_64; rv:135.0) Gecko/20100101 Firefox/135.0';
const ffProfile = parseUserAgent(linuxFfUa);
assert.strictEqual(ffProfile.brand, 'Firefox');
assert.strictEqual(ffProfile.platform, 'Linux');

const ffHeaders = getDeviceHeaders(linuxFfUa);
assert.strictEqual(ffHeaders['sec-ch-ua'], undefined, 'Firefox must NEVER send sec-ch-ua');
assert.strictEqual(ffHeaders['User-Agent'], linuxFfUa);

// Test 2d: Android Chrome
const androidChromeUa = 'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.6943.122 Mobile Safari/537.36';
const androidProfile = parseUserAgent(androidChromeUa);
assert.strictEqual(androidProfile.platform, 'Android');
assert.strictEqual(androidProfile.isMobile, true);

const androidHeaders = getDeviceHeaders(androidChromeUa);
assert.strictEqual(androidHeaders['sec-ch-ua-platform'], '"Android"');
assert.strictEqual(androidHeaders['sec-ch-ua-mobile'], '?1');

console.log('✓ Device Profile & Client Hints tests passed!');

// 3. Test Parser - Title sanitization
console.log('Testing Parser - sanitizeTitle...');
assert.strictEqual(sanitizeTitle('Test / Thread: Name? *<>|'), 'Test - Thread- Name- ----');
assert.strictEqual(sanitizeTitle('My Thread 🌟🔥 [Special]'), 'My Thread -- [Special]');
console.log('✓ sanitizeTitle passed!');

// 4. Test Parser - Redirect Decoding
console.log('Testing Parser - decodeForumRedirect...');
const b64Target = Buffer.from('https://bunkr.cr/v/testslug123').toString('base64');
const redirectUrl = `https://simpcity.su/redirect/?to=${encodeURIComponent(b64Target)}&m=b64`;
const decoded = decodeForumRedirect(redirectUrl);
assert.strictEqual(decoded, 'https://bunkr.cr/v/testslug123', 'Should decode base64 redirect URL');

const urlEncodedTarget = encodeURIComponent('https://filester.me/d/samplefile');
const redirectUrl2 = `https://simpcity.su/redirect/?to=${urlEncodedTarget}`;
const decoded2 = decodeForumRedirect(redirectUrl2);
assert.strictEqual(decoded2, 'https://filester.me/d/samplefile', 'Should decode URL-encoded redirect URL');
console.log('✓ decodeForumRedirect passed!');

// 5. Test Parser - Document Parsing & Spoilers
console.log('Testing Parser - parseXenForoDocument...');
const sampleHtml = `
<!DOCTYPE html>
<html>
<head>
  <title>Sample Model Thread | SimpCity</title>
</head>
<body>
  <h1 class="p-title-value">Sample Model Thread</h1>
  <div class="p-body-main">
    <article class="message message--post" data-content="post-987654" id="js-post-987654">
      <div class="message-attribution">
        <ul class="message-attribution-opposite">
          <li><a href="/threads/sample-model.12345/post-987654">#1</a></li>
        </ul>
      </div>
      <div class="message-userContent">
        <div class="bbCodeBlock bbCodeBlock--spoiler">
          <div class="bbCodeBlock-content">pw: mySecretPassword123</div>
        </div>
        <p>Here is some text with another pass: testPass456</p>
        <p>
          <a href="https://bunkr.cr/v/bunkrvideo123">Bunkr Video</a>
          <a href="https://filester.me/d/filesterdoc123">Filester Doc</a>
          <a href="https://turbo.cr/v/turbovid123">Turbo Vid</a>
          <a href="https://goonbox.cr/img/gbximg123">Goonbox Img</a>
          <a href="https://jpg.fish/images/sample1.jpg">JPG Fish Image</a>
          <a href="https://ibb.co/sample7">Ibb Image</a>
          <a href="/attachments/sample-preview-png.55555/">Simp Attachment</a>
        </p>
      </div>
    </article>
  </div>
</body>
</html>
`;

const doc = parseXenForoDocument(sampleHtml, 'https://simpcity.su/threads/sample-model.12345/');
assert.strictEqual(doc.threadTitle, 'Sample Model Thread', 'Should extract thread title');
assert.strictEqual(doc.posts.length, 1, 'Should extract 1 post');
const post = doc.posts[0];
assert.strictEqual(post.postId, '987654', 'Should extract post ID');
assert.strictEqual(post.postNumber, '1', 'Should extract post number');
assert(post.spoilers.includes('mySecretPassword123'), 'Should extract spoiler block password');
assert(post.spoilers.includes('testPass456'), 'Should extract inline regex password');

const hostNames = post.hosts.map(h => h.name);
assert(hostNames.includes('Bunkr'), 'Should match Bunkr');
assert(hostNames.includes('Filester'), 'Should match Filester');
assert(hostNames.includes('turbo'), 'Should match Turbo');
assert(hostNames.includes('Goonbox'), 'Should match Goonbox');
assert(hostNames.includes('JPGX'), 'Should match JPGX');
assert(hostNames.includes('Ibb'), 'Should match Ibb');
assert(hostNames.includes('Simpcity'), 'Should match Simpcity attachment');
console.log('✓ parseXenForoDocument and matchHostsInPost passed!');

// 6. Test URL Detection
console.log('Testing isXenForoUrl...');
assert(isXenForoUrl('https://simpcity.su/threads/example.12345/'));
assert(isXenForoUrl('https://simpcity.cr/threads/example.12345/page-2'));
assert(isXenForoUrl('https://simpcity.is/threads/example.12345/post-6789'));
assert(isXenForoUrl('xenforo:https://otherforum.com/threads/post.1/'));
assert(isXenForoUrl('simpcity:https://simpcity.top/threads/abc.1/'));
assert(!isXenForoUrl('https://google.com/search?q=test'));
console.log('✓ isXenForoUrl passed!');

// 7. Test Resolvers & Device Headers
console.log('Testing Resolvers with Device Headers...');
(async () => {
  // Test SimpCity attachment resolver with device headers
  const simpRes = await resolveResource('/attachments/sample-file.12345/', { cookieJar: jar });
  assert(simpRes[0].headers.Cookie.includes('xf_user=12345%2Cabcd'), 'Simpcity attachment should have cookie header');
  assert.strictEqual(simpRes[0].headers['User-Agent'], jar.getUserAgent(), 'Simpcity attachment should have spoofed User-Agent');
  assert(simpRes[0].headers['sec-ch-ua'], 'Simpcity attachment should include Client Hints for Chrome');

  // Test Pixeldrain resolver
  const pdRes = await resolveResource('https://pixeldrain.com/u/abc12345');
  assert.strictEqual(pdRes[0].url, 'https://pixeldrain.com/api/file/abc12345?download');

  // Test JPGX single resolver
  const jpgRes = await resolveResource('https://simp1.jpg.church/images/photo.th.jpg');
  assert.strictEqual(jpgRes[0].url, 'https://simp1.jpg.fish/images/photo.jpg');

  // Test Pomf2 resolver
  const pomfRes = await resolveResource('https://pomf2.lain.la/f/test123.mp4?dl=1');
  assert.strictEqual(pomfRes[0].url, 'https://pomf2.lain.la/f/test123.mp4');

  console.log('✓ Resolver basic tests passed!');
  console.log('\nAll XenForo Scraper Node.js tests completed successfully! 🎉');
})();

