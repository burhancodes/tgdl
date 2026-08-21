from __future__ import annotations

import re
from pathlib import PurePath


def natural_sort_key(s: str | PurePath) -> list[int | str]:
    """Key for natural sorting (human sorting of numbers in strings).

    Splits numeric and non-numeric characters so 'file2' comes before 'file10'.
    """
    return [
        int(text) if text.isdigit() else text.lower()
        for text in re.split(r"(\d+)", str(s))
    ]


def natural_path_sort_key(path: str | PurePath) -> tuple[list[int | str], ...]:
    """Key for hierarchical natural sorting of paths.

    Applies natural sort key component-by-component so parent folders and child
    files are sorted properly (e.g., 'dir1/f2.txt' before 'dir1/f10.txt' before 'dir2/f1.txt').
    """
    p = PurePath(path)
    return tuple(natural_sort_key(part) for part in p.parts)
