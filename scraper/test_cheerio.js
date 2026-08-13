import * as cheerio from 'cheerio';
const xml = `
<item>
    <title>Naruto</title>
    <link>magnet:?xt=urn:btih:ABCDEF1234567890ABCDEF1234567890ABCDEF12</link>
    <nyaa:seeders>10</nyaa:seeders>
    <nyaa:leechers>5</nyaa:leechers>
    <nyaa:size>1.2 GiB</nyaa:size>
    <nyaa:infoHash>ABCDEF1234567890ABCDEF1234567890ABCDEF12</nyaa:infoHash>
</item>`;
const $ = cheerio.load(xml, { xmlMode: true });
console.log("title:", $('item').find('title').text());
console.log("infoHash:", $('item').find('nyaa\\:infoHash').text());
console.log("size raw:", $('item').find('nyaa\\:size').text());
console.log("size parsed:", parseInt($('item').find('nyaa\\:size').text().trim(), 10) || 0);
