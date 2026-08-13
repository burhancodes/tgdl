import FeedParser from 'feedparser';
import { Readable } from 'stream';

/**
 * Parses an RSS/Atom feed XML string and returns an array of items.
 *
 * @param {string} xmlString The raw XML string of the feed.
 * @returns {Promise<Array<object>>} A promise that resolves to an array of feedparser items.
 */
export function parseFeed(xmlString) {
  return new Promise((resolve, reject) => {
    const feedparser = new FeedParser();
    const items = [];

    feedparser.on('error', (error) => {
      reject(error);
    });

    feedparser.on('readable', function () {
      let item;
      while ((item = this.read())) {
        items.push(item);
      }
    });

    feedparser.on('end', () => {
      resolve(items);
    });

    Readable.from([xmlString]).pipe(feedparser);
  });
}
