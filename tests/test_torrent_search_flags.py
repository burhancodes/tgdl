from __future__ import annotations

import pytest

from app.handlers.torrent_search import parse_search_query_and_providers


def test_parse_search_query_single_flag() -> None:
    query, providers = parse_search_query_and_providers("/ts -yts Inception 2010")
    assert query == "Inception 2010"
    assert providers == ["yts"]


def test_parse_search_query_multiple_flags() -> None:
    query, providers = parse_search_query_and_providers("/ts -tpb -1337x Oppenheimer")
    assert query == "Oppenheimer"
    assert providers == ["thepiratebay", "leetx"]


def test_parse_search_query_generic_provider_flag() -> None:
    query, providers = parse_search_query_and_providers("/search -p=nyaa Naruto Shippuden")
    assert query == "Naruto Shippuden"
    assert providers == ["nyaa"]


def test_parse_search_query_no_flags() -> None:
    query, providers = parse_search_query_and_providers("/torsearch Avatar 2009")
    assert query == "Avatar 2009"
    assert providers is None


def test_parse_search_query_empty() -> None:
    query, providers = parse_search_query_and_providers("/ts -yts")
    assert query == ""
    assert providers == ["yts"]
