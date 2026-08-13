import asyncio
import aiohttp
import urllib.parse
async def search():
    query = "naruto"
    encoded_query = urllib.parse.quote(query)
    url = f"https://nyaa.si/?page=rss&q={encoded_query}"
    headers = {"User-Agent": "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"}
    try:
        async with aiohttp.ClientSession(headers=headers) as session:
            async with session.get(url, timeout=10) as resp:
                print("Status:", resp.status)
                if resp.status == 200:
                    text = await resp.text()
                    print("Length:", len(text))
    except Exception as e:
        print("Error:", e)
asyncio.run(search())
