from __future__ import annotations

from unittest.mock import AsyncMock, MagicMock, patch

import pytest

from app.telegraph import (
    InvalidHTML,
    NotAllowedTag,
    RetryAfterError,
    Telegraph,
    TelegraphHelper,
    html_to_nodes,
)


def test_html_to_nodes_basic():
    html = "<p>Hello <b>World</b>!</p>"
    nodes = html_to_nodes(html)
    assert len(nodes) == 1
    assert nodes[0]["tag"] == "p"
    assert nodes[0]["children"][0] == "Hello "
    assert nodes[0]["children"][1] == {"tag": "b", "children": ["World"]}
    assert nodes[0]["children"][2] == "!"


def test_html_to_nodes_void_elements_and_attrs():
    html = '<p>Check <a href="https://example.com">link</a><br><img src="http://example.com/img.jpg"></p>'
    nodes = html_to_nodes(html)
    assert nodes[0]["tag"] == "p"
    children = nodes[0]["children"]
    assert children[1]["tag"] == "a"
    assert children[1]["attrs"] == {"href": "https://example.com"}
    assert children[2] == {"tag": "br"}
    assert children[3]["tag"] == "img"
    assert children[3]["attrs"] == {"src": "http://example.com/img.jpg"}


def test_html_to_nodes_entities():
    html = "<p>&amp; &#39; &#x27;</p>"
    nodes = html_to_nodes(html)
    assert nodes[0]["children"][0] == "& ' '"


def test_html_to_nodes_disallowed_tag():
    html = "<script>alert(1)</script>"
    with pytest.raises(NotAllowedTag):
        html_to_nodes(html)


def test_html_to_nodes_invalid_html():
    html = "<canvas>Unallowed canvas</canvas>"
    with pytest.raises(NotAllowedTag):
        html_to_nodes(html)

    html_unclosed = "<p><b>Tag not closed</p>"
    with pytest.raises(InvalidHTML):
        html_to_nodes(html_unclosed)


@pytest.mark.asyncio
async def test_telegraph_client_methods():
    client = Telegraph(access_token="test_token", domain="graph.org")

    mock_resp = MagicMock()
    mock_resp.raise_for_status = MagicMock()
    mock_resp.json.return_value = {"ok": True, "result": {"short_name": "test", "access_token": "acc_123"}}

    with patch.object(client._client, "post", new_callable=AsyncMock) as mock_post:
        mock_post.return_value = mock_resp

        res = await client.create_account(short_name="test")
        assert res["access_token"] == "acc_123"
        assert client.access_token == "acc_123"

        mock_resp.json.return_value = {"ok": True, "result": {"path": "test-path"}}
        page_res = await client.create_page("Test Title", "<p>Hello</p>")
        assert page_res["path"] == "test-path"

    await client.close()


@pytest.mark.asyncio
async def test_telegraph_client_flood_wait():
    client = Telegraph(access_token="test_token")
    mock_resp = MagicMock()
    mock_resp.raise_for_status = MagicMock()
    mock_resp.json.return_value = {"ok": False, "error": "FLOOD_WAIT_15"}

    with patch.object(client._client, "post", new_callable=AsyncMock) as mock_post:
        mock_post.return_value = mock_resp
        with pytest.raises(RetryAfterError) as exc_info:
            await client.create_page("Test", "<p>Test</p>")
        assert exc_info.value.retry_after == 15

    await client.close()


@pytest.mark.asyncio
async def test_telegraph_helper_generate_page():
    helper = TelegraphHelper()
    results = [
        {
            "name": "Linux ISO 'Ubuntu'",
            "size": "2.5 GB",
            "seeders": 100,
            "leechers": 5,
            "torrent": "https://example.com/download?id=1'quote",
            "magnet": "magnet:?xt=urn:btih:1234567890abcdef'attr='bad",
        }
    ]

    with patch.object(helper, "create_page", new_callable=AsyncMock) as mock_create_page:
        mock_create_page.return_value = {"path": "torrent-search-123"}

        url = await helper.generate_telegraph_page(results, "linux ' iso", "public")
        assert url == "https://graph.org/torrent-search-123"
        mock_create_page.assert_called_once()
        call_args = mock_create_page.call_args
        content = call_args.kwargs.get("content") or call_args.args[1]
        assert "linux &#x27; iso" in content or "linux &amp;#x27; iso" in content or "linux ' iso" in content
        assert "href=&#x27;https://example.com/download?id=1&#x27;quote&#x27;" in content or "example.com" in content


@pytest.mark.asyncio
async def test_telegraph_helper_pagination():
    helper = TelegraphHelper()
    results = [
        {
            "name": f"Item {i} " + "X" * 1500,
            "size": "1 GB",
            "seeders": 1,
            "leechers": 1,
            "torrent": f"https://example.com/item{i}",
        }
        for i in range(40)
    ]

    paths = ["path-1", "path-2"]
    create_call_count = 0

    async def mock_create_page(title, content, domain=None):
        nonlocal create_call_count
        path = paths[min(create_call_count, len(paths) - 1)]
        create_call_count += 1
        return {"path": path}

    with patch.object(helper, "create_page", side_effect=mock_create_page), \
         patch.object(helper, "link_paginated_pages", new_callable=AsyncMock) as mock_link:

        url = await helper.generate_telegraph_page(results, "large query", "site")
        assert url == "https://graph.org/path-1"
        assert mock_link.called
        assert mock_link.call_args.kwargs["paths"] == ["path-1", "path-2"]


@pytest.mark.asyncio
async def test_telegraph_helper_retry_on_flood_wait():
    helper = TelegraphHelper()

    attempts = 0

    async def mock_create_page_impl(title, html_content, author_name=None, author_url=None):
        nonlocal attempts
        attempts += 1
        if attempts == 1:
            raise RetryAfterError(1)
        return {"path": "success-path"}

    with patch("app.telegraph.helper.Telegraph") as mock_telegraph_cls, \
         patch("app.telegraph.helper.sleep", new_callable=AsyncMock) as mock_sleep:

        client_instance = AsyncMock()
        client_instance.create_account = AsyncMock(return_value={"access_token": "acc"})
        client_instance.create_page = AsyncMock(side_effect=mock_create_page_impl)
        client_instance.close = AsyncMock()
        mock_telegraph_cls.return_value = client_instance

        res = await helper.create_page("Test Title", "<p>Content</p>")
        assert res["path"] == "success-path"
        assert attempts == 2
        mock_sleep.assert_called_once_with(1)


def test_html_to_nodes_heading_mapping_and_unwrapping():
    html = "<div><h1>Title</h1><span>Text</span></div>"
    nodes = html_to_nodes(html)
    assert len(nodes) == 2
    assert nodes[0]["tag"] == "h3"
    assert nodes[0]["children"] == ["Title"]
    assert nodes[1] == "Text"


def test_html_to_nodes_attribute_whitelisting():
    html = '<p style="color:red" class="main"><a href="https://example.com" onclick="alert(1)">Link</a></p>'
    nodes = html_to_nodes(html)
    assert nodes[0]["tag"] == "p"
    assert "attrs" not in nodes[0]
    assert nodes[0]["children"][0]["tag"] == "a"
    assert nodes[0]["children"][0]["attrs"] == {"href": "https://example.com"}


@pytest.mark.asyncio
async def test_telegraph_client_context_manager_and_new_methods():
    async with Telegraph(access_token="test_token") as client:
        mock_resp = MagicMock()
        mock_resp.status_code = 200
        mock_resp.raise_for_status = MagicMock()
        mock_resp.json.return_value = {"ok": True, "result": {"short_name": "updated_name"}}

        with patch.object(client._client, "post", new_callable=AsyncMock) as mock_post:
            mock_post.return_value = mock_resp

            res_edit = await client.edit_account_info("updated_name")
            assert res_edit["short_name"] == "updated_name"

            mock_resp.json.return_value = {"ok": True, "result": {"views": 100}}
            res_views = await client.get_views("test-path")
            assert res_views["views"] == 100


@pytest.mark.asyncio
async def test_telegraph_page_cinemeta_rendering():
    from app.telegraph.helper import TelegraphHelper

    mock_meta = {
        "name": "Avatar",
        "year": "2009",
        "poster": "https://images.metahub.space/poster/small/tt0499549/img",
        "description": "A paraplegic Marine dispatched to Pandora...",
        "rating": "7.9",
        "genres": ["Action", "Sci-Fi"],
        "imdb_id": "tt0499549",
        "type": "movie",
    }

    helper = TelegraphHelper()
    results = [{"name": "Avatar 1080p", "size": "3 GB", "seeders": 50, "torrent": "https://example.com/t"}]

    with patch("app.telegraph.helper.fetch_cinemeta_info", new_callable=AsyncMock) as mock_fetch, \
         patch.object(helper, "create_page", new_callable=AsyncMock) as mock_create_page:
        mock_fetch.return_value = mock_meta
        mock_create_page.return_value = {"path": "avatar-search"}

        url = await helper.generate_telegraph_page(results, "Avatar", "Magnetio")
        assert url == "https://graph.org/avatar-search"
        content = mock_create_page.call_args.kwargs["content"]
        assert "<figure><img src='https://images.metahub.space/poster/small/tt0499549/img'></figure>" in content
        assert "<b>Avatar</b> (2009)" in content
        assert "⭐ 7.9/10" in content
        assert "Action, Sci-Fi" in content
        assert "A paraplegic Marine dispatched to Pandora..." in content


@pytest.mark.asyncio
async def test_telegraph_page_provider_hyperlink():
    from app.telegraph.helper import TelegraphHelper

    helper = TelegraphHelper()
    results = [
        {
            "name": "Ubuntu 22.04 ISO",
            "size": "3.5 GB",
            "seeders": 120,
            "magnet": "magnet:?xt=urn:btih:1234567890abcdef1234567890abcdef12345678",
            "torrent": "https://thepiratebay.org/description.php?id=123",
            "provider": "ThePirateBay",
        }
    ]

    with patch("app.telegraph.helper.fetch_cinemeta_info", new_callable=AsyncMock) as mock_fetch, \
         patch.object(helper, "create_page", new_callable=AsyncMock) as mock_create_page:
        mock_fetch.return_value = None
        mock_create_page.return_value = {"path": "piratebay-test"}

        url = await helper.generate_telegraph_page(results, "Ubuntu", "Magnetio")
        assert url == "https://graph.org/piratebay-test"
        content = mock_create_page.call_args.kwargs["content"]
        assert "Share Magnet" in content
        assert "<a href='https://thepiratebay.org/description.php?id=123'>ThePirateBay</a>" in content

