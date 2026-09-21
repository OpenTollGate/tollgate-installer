#!/usr/bin/env python3
"""Resolve the newest FreedomTechFeed/packages release for a channel.

Mirrors the installer's channel semantics (arch.go:selectFeedTagForChannel) so
CI and the binary agree on what "latest" means. Used by the launcher
(install-and-test.sh) and the feed-release-smoke workflow.

Env:
  FEED_CHANNEL  alpha (default) | beta | stable | any | latest
  FEED_REPO     default FreedomTechFeed/packages
  GITHUB_TOKEN  optional; raises the GitHub API rate limit

Prints the tag on success (exit 0); prints nothing and exits 1 when no release
matches or the API call fails.
"""
import json
import os
import re
import sys
import urllib.request


def is_pre(tag: str) -> bool:
    return bool(re.search(r"(pre|alpha|beta|rc)", tag, re.I))


def matches(tag: str, channel: str) -> bool:
    if channel in ("", "any", "latest"):
        return True
    if channel == "alpha":
        return is_pre(tag)
    if channel == "beta":
        return bool(re.search(r"(beta|rc)", tag, re.I))
    if channel == "stable":
        return not is_pre(tag)
    return False


def newest(releases, channel: str):
    candidates = [r for r in releases if matches(r.get("tag_name", ""), channel)]
    candidates.sort(
        key=lambda r: r.get("published_at") or r.get("created_at") or "",
        reverse=True,
    )
    return candidates[0]["tag_name"] if candidates else ""


def main() -> int:
    channel = os.environ.get("FEED_CHANNEL", "alpha").strip().lower()
    repo = os.environ.get("FEED_REPO", "FreedomTechFeed/packages").strip()
    url = "https://api.github.com/repos/%s/releases?per_page=100" % repo
    req = urllib.request.Request(
        url,
        headers={
            "Accept": "application/vnd.github+json",
            "User-Agent": "tollgate-installer-feed-release",
        },
    )
    token = os.environ.get("GITHUB_TOKEN", "").strip()
    if token:
        req.add_header("Authorization", "Bearer %s" % token)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            releases = json.load(resp)
    except Exception as exc:  # noqa: BLE001 - any failure means "unresolved"
        print("resolve error: %s" % exc, file=sys.stderr)
        return 1
    if not isinstance(releases, list):
        return 1
    tag = newest(releases, channel)
    if not tag:
        return 1
    print(tag)
    return 0


if __name__ == "__main__":
    sys.exit(main())
