import FeedParser from 'feedparser';
import { Readable } from 'stream';

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
