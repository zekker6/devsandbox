#!/usr/bin/env python3
"""Verify every internal link and anchor in the built site under ./public.

Zensical's own build warns about links it can resolve from the Markdown source,
but it does not look inside snippet-included content (`--8<--`), which is where
the README and CHANGELOG bodies come from. Checking the rendered HTML covers
both, and is also the only way to catch anchors that differ between GitHub's
slug flavour and Python-Markdown's.

External (http/https) links are deliberately not fetched: that would make the
build depend on the network and on third-party uptime.
"""

from __future__ import annotations

import re
import sys
import tomllib
from html.parser import HTMLParser
from pathlib import Path
from urllib.parse import unquote, urldefrag, urljoin, urlparse

# The theme emits a "skip to content" link on every page, but its 404 template
# renders no content wrapper for it to point at. Not ours to fix.
IGNORED_FRAGMENTS = {"__skip"}

EXTERNAL = re.compile(r"^(?:[a-z][a-z0-9+.-]*:|//)", re.IGNORECASE)


class Page(HTMLParser):
    """Collects the ids an HTML page defines and the hrefs it links to."""

    def __init__(self) -> None:
        super().__init__(convert_charrefs=True)
        self.ids: set[str] = set()
        self.hrefs: list[str] = []

    def handle_starttag(self, tag: str, attrs: list[tuple[str, str | None]]) -> None:
        attr = {k: v for k, v in attrs if v is not None}
        if attr.get("id"):
            self.ids.add(attr["id"])
        if tag == "a":
            if attr.get("name"):
                self.ids.add(attr["name"])
            if attr.get("href"):
                self.hrefs.append(attr["href"])


def site_root_url(config: Path, root: Path, docs_dir: Path) -> str:
    """URL path that `root` maps to once deployed.

    `site_url` addresses the docs subtree (…/devsandbox/docs/) while `root` is
    the whole published directory, so the docs subdirectory is trimmed off the
    end. Mismatches abort rather than guess - a wrong prefix would silently
    turn every absolute link into a false positive.
    """
    site_url = tomllib.loads(config.read_text(encoding="utf-8"))["project"]["site_url"]

    path = urlparse(site_url).path
    if not path.endswith("/"):
        path += "/"
    suffix = docs_dir.relative_to(root).as_posix() + "/"
    if not path.endswith(suffix):
        sys.exit(
            f"{config}: site_url path {path!r} does not end in {suffix!r}; "
            f"cannot derive the URL prefix for {str(root)!r}"
        )
    return path[: -len(suffix)]


def collect(root: Path, prefix: str) -> tuple[dict[str, Page], set[str]]:
    """Map deployed URL -> parsed page, plus the URLs of all non-HTML files."""
    pages: dict[str, Page] = {}
    assets: set[str] = set()

    for path in root.rglob("*"):
        if not path.is_file():
            continue
        rel = path.relative_to(root).as_posix()
        if path.suffix != ".html":
            assets.add(prefix + rel)
            continue
        page = Page()
        page.feed(path.read_text(encoding="utf-8"))
        if path.name == "index.html":
            pages[prefix + rel[: -len("index.html")]] = page
        else:
            pages[prefix + rel] = page

    return pages, assets


def check(root: Path, config: Path, docs_dir: Path) -> int:
    prefix = site_root_url(config, root, docs_dir)
    pages, assets = collect(root, prefix)
    if not pages:
        sys.exit(f"{root}: no HTML found - run `task site:build` first")

    broken: list[tuple[str, str, str]] = []

    for url, page in sorted(pages.items()):
        for href in page.hrefs:
            if EXTERNAL.match(href):
                continue

            target, fragment = urldefrag(urljoin(url, href))
            target, fragment = unquote(target), unquote(fragment)

            if target in pages:
                if fragment and fragment not in pages[target].ids:
                    if fragment not in IGNORED_FRAGMENTS:
                        broken.append((url, href, f"no anchor #{fragment} on {target}"))
            elif target not in assets:
                broken.append((url, href, f"no such page: {target}"))

    for url, href, reason in broken:
        print(f"{url}\n    {href}\n    {reason}")

    print(
        f"\nlinkcheck: {len(broken)} broken, "
        f"{sum(len(p.hrefs) for p in pages.values())} links across {len(pages)} pages"
    )
    return 1 if broken else 0


if __name__ == "__main__":
    sys.exit(
        check(
            root=Path("public"),
            config=Path("zensical.toml"),
            docs_dir=Path("public/docs"),
        )
    )
