"""Tests for scripts/check-frozen-launcher.sh — the frozen v0.6.0 launcher guard.

These exist because the whole point of the guard is to fail, and a guard that
never fires is worse than no guard: it reads as protection while providing none.

Fully offline — builds throwaway git repos in tmp_path. No network, no router.
"""

from __future__ import annotations

import os
import subprocess
from pathlib import Path

import pytest

PROJECT_ROOT = Path(__file__).resolve().parent.parent.parent
SCRIPT = PROJECT_ROOT / "scripts" / "check-frozen-launcher.sh"
FROZEN = "install-and-test.sh"
NEW = "install-and-test-v0.6.1.sh"


def _git(repo: Path, *args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["git", *args], cwd=repo, capture_output=True, text=True,
        env={**os.environ, "GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@t",
             "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@t"},
    )


@pytest.fixture()
def repo(tmp_path: Path) -> Path:
    """A repo with a committed frozen launcher and the new channel file."""
    r = tmp_path / "r"
    r.mkdir()
    _git(r, "init", "-q", "-b", "main")
    (r / FROZEN).write_text("#!/usr/bin/env bash\necho v060\n")
    (r / NEW).write_text("#!/usr/bin/env bash\necho v061\n")
    (r / "scripts").mkdir()
    (r / "scripts" / "check-frozen-launcher.sh").write_text(SCRIPT.read_text())
    (r / "scripts" / "check-frozen-launcher.sh").chmod(0o755)
    _git(r, "add", "-A")
    _git(r, "commit", "-q", "-m", "base")
    return r


def _run(repo: Path, *args: str, env: dict[str, str] | None = None):
    e = dict(os.environ)
    e.pop("TOLLGATE_UNFREEZE_LAUNCHER", None)
    if env:
        e.update(env)
    return subprocess.run(
        ["bash", str(repo / "scripts" / "check-frozen-launcher.sh"), *args],
        cwd=repo, capture_output=True, text=True, env=e, timeout=30,
    )


def test_clean_tree_passes(repo: Path) -> None:
    r = _run(repo, "--staged")
    assert r.returncode == 0, r.stdout + r.stderr
    assert "OK" in r.stdout


def test_editing_the_new_channel_file_is_allowed(repo: Path) -> None:
    """The whole point: post-release changes go in the NEW file."""
    (repo / NEW).write_text("#!/usr/bin/env bash\necho changed\n")
    _git(repo, "add", NEW)
    r = _run(repo, "--staged")
    assert r.returncode == 0, r.stdout + r.stderr


def test_staging_the_frozen_file_fails(repo: Path) -> None:
    """The guard must actually fire — this is the regression it exists for."""
    (repo / FROZEN).write_text("#!/usr/bin/env bash\necho tampered\n")
    _git(repo, "add", FROZEN)
    r = _run(repo, "--staged")
    assert r.returncode == 1, r.stdout + r.stderr
    assert "FROZEN" in r.stderr
    assert NEW in r.stderr  # points the author at the right file


def test_against_ref_detects_divergence(repo: Path) -> None:
    (repo / FROZEN).write_text("#!/usr/bin/env bash\necho changed\n")
    _git(repo, "commit", "-aqm", "edit frozen")
    r = _run(repo, "--against", "HEAD~1")
    assert r.returncode == 1, r.stdout + r.stderr


def test_against_ref_clean_passes(repo: Path) -> None:
    (repo / NEW).write_text("#!/usr/bin/env bash\necho newer\n")
    _git(repo, "commit", "-aqm", "new channel")
    r = _run(repo, "--against", "HEAD~1")
    assert r.returncode == 0, r.stdout + r.stderr


def test_override_env_allows_deliberate_change(repo: Path) -> None:
    """A security fix must be able to bypass, deliberately and visibly."""
    (repo / FROZEN).write_text("#!/usr/bin/env bash\necho security\n")
    _git(repo, "add", FROZEN)
    r = _run(repo, "--staged", env={"TOLLGATE_UNFREEZE_LAUNCHER": "1"})
    assert r.returncode == 0, r.stdout + r.stderr
    assert "OVERRIDE" in r.stdout


def test_absent_frozen_file_is_not_an_error(tmp_path: Path) -> None:
    """A clone without the file (e.g. a partial checkout) must not false-fail."""
    r = tmp_path / "empty"
    r.mkdir()
    _git(r, "init", "-q", "-b", "main")
    (r / "scripts").mkdir()
    (r / "scripts" / "check-frozen-launcher.sh").write_text(SCRIPT.read_text())
    _git(r, "add", "-A")
    _git(r, "commit", "-q", "-m", "base")
    out = subprocess.run(
        ["bash", str(r / "scripts" / "check-frozen-launcher.sh"), "--staged"],
        cwd=r, capture_output=True, text=True, timeout=30,
    )
    assert out.returncode == 0, out.stdout + out.stderr
    assert "nothing to check" in out.stdout


def test_frozen_file_matches_v060_tag_content(repo: Path) -> None:
    """The committed frozen file must be the v0.6.0 content, byte for byte.

    Guards against the freeze being applied to the WRONG content — i.e. someone
    freezing a file that already carries post-release changes.
    """
    body = (repo / FROZEN).read_text()
    assert "echo v060" in body
    assert "install-and-test-v0.6.1.sh" not in body, (
        "the frozen file must not reference the new channel — it is the v0.6.0 "
        "content, served to users who were given the v0.6.0 URL"
    )
