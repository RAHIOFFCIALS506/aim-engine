# MIT License

# Copyright (c) 2026 Advanced Micro Devices, Inc.

# Permission is hereby granted, free of charge, to any person obtaining a copy
# of this software and associated documentation files (the "Software"), to deal
# in the Software without restriction, including without limitation the rights
# to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
# copies of the Software, and to permit persons to whom the Software is
# furnished to do so, subject to the following conditions:

# The above copyright notice and this permission notice shall be included in all
# copies or substantial portions of the Software.

# THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
# IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
# FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
# AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
# LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
# OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
# SOFTWARE.

"""Single source of truth for the effective HuggingFace download filter.

The operator sends two kinds of pattern. User-authored patterns arrive in
AIM_HF_INCLUDE / AIM_HF_EXCLUDE and are applied verbatim. The operator's own
fallback arrives in the _DEFAULT variants and is applied only when the
repository is not a multi-directory layout, because that fallback excludes
``*/*`` and would otherwise discard the tokenizer, text encoder, transformer
and VAE directories a Diffusers pipeline cannot load without.

Every consumer (the size estimator, the downloader, and both verification
stages) must resolve identically: the size estimate provisions the PVC that the
download has to fit in.
"""

import os
import sys
from fnmatch import fnmatch
from typing import List, Optional

# Diffusers writes this manifest at the repository root, mapping each pipeline
# component to the subdirectory holding it. Its presence is the repository
# declaring that its subdirectories are load-bearing. Matched at the root only:
# a copy inside a subdirectory describes that subdirectory, not this repository.
LAYOUT_MARKER = "model_index.json"

USER_INCLUDE_ENV = "AIM_HF_INCLUDE"
USER_EXCLUDE_ENV = "AIM_HF_EXCLUDE"
DEFAULT_INCLUDE_ENV = "AIM_HF_INCLUDE_DEFAULT"
DEFAULT_EXCLUDE_ENV = "AIM_HF_EXCLUDE_DEFAULT"

RESOLVED_INCLUDE_ENV = "AIM_RESOLVED_INCLUDE"
RESOLVED_EXCLUDE_ENV = "AIM_RESOLVED_EXCLUDE"
RESOLVED_SOURCE_ENV = "AIM_RESOLVED_FILTER_SOURCE"

SOURCE_USER = "user"
SOURCE_DEFAULT = "default"
SOURCE_LAYOUT_OVERRIDE = "layout-override"
SOURCE_NONE = "none"


class ResolvedFilter:
    """A DownloadFilter-shaped include/exclude pair plus the provenance that produced it.

    Mirrors s3_downloader.filters.DownloadFilter deliberately: same semantics,
    same member names, so the HF and S3 paths read as one idea.
    """

    def __init__(self, include: List[str], exclude: List[str], source: str):
        self.include = list(include)
        self.exclude = list(exclude)
        self.source = source

    @property
    def active(self) -> bool:
        return bool(self.include or self.exclude)

    def matches(self, relpath: str) -> bool:
        """Return True if ``relpath`` should be downloaded."""
        if self.include and not any(fnmatch(relpath, pat) for pat in self.include):
            return False
        if self.exclude and any(fnmatch(relpath, pat) for pat in self.exclude):
            return False
        return True

    def apply(self, siblings):
        """Filter huggingface_hub sibling objects on their repo-relative path."""
        return [s for s in siblings if self.matches(s.rfilename)]

    def summary(self) -> str:
        if self.source == SOURCE_LAYOUT_OVERRIDE:
            return (
                f"Download filter: none — {LAYOUT_MARKER} found at the repository "
                "root, so the default subdirectory exclude was dropped and the "
                "full repository will be downloaded"
            )
        if self.source == SOURCE_NONE:
            return "Download filter: none configured — downloading the full repository"
        origin = "user-configured" if self.source == SOURCE_USER else "default"
        return (
            f"Download filter ({origin}): "
            f"include={self.include or '<none>'} exclude={self.exclude or '<none>'}"
        )


def _split_env(name: str) -> List[str]:
    raw = os.environ.get(name, "")
    return [p.strip() for p in raw.split(",") if p.strip()]


def resolve(filenames) -> ResolvedFilter:
    """Resolve the effective filter for a repository listing.

    filenames are repo-relative paths, so a root-level file has no separator.
    """
    include = _split_env(USER_INCLUDE_ENV)
    exclude = _split_env(USER_EXCLUDE_ENV)
    if include or exclude:
        return ResolvedFilter(include, exclude, SOURCE_USER)

    include = _split_env(DEFAULT_INCLUDE_ENV)
    exclude = _split_env(DEFAULT_EXCLUDE_ENV)
    if not (include or exclude):
        return ResolvedFilter([], [], SOURCE_NONE)

    if LAYOUT_MARKER in set(filenames):
        return ResolvedFilter([], [], SOURCE_LAYOUT_OVERRIDE)

    return ResolvedFilter(include, exclude, SOURCE_DEFAULT)


def from_resolved_env() -> Optional[ResolvedFilter]:
    """The filter already resolved for this pod, or None if nothing resolved it.

    The entrypoint resolves once and exports the result so that the download and
    both verification stages cannot disagree with each other.
    """
    source = os.environ.get(RESOLVED_SOURCE_ENV)
    if not source:
        return None
    return ResolvedFilter(
        _split_env(RESOLVED_INCLUDE_ENV),
        _split_env(RESOLVED_EXCLUDE_ENV),
        source,
    )


def effective(filenames):
    """Reuse this pod's resolution when there is one, else resolve from listing."""
    resolved = from_resolved_env()
    if resolved is not None:
        return resolved
    return resolve(filenames)


def capacity_error(total: int, source: str) -> Optional[str]:
    """Explain why the resolved set cannot fit the cache volume, or None if it can.

    Layout detection can only grow the selected set, and when spec.size is set on
    the artifact the PVC was sized from that number without anything ever listing
    the repository. Without this check the download runs for hours and dies on
    ENOSPC, which says nothing about the real cause.

    Returns None whenever the volume cannot be measured: an unmeasurable volume
    is the pre-existing behaviour, whereas refusing to download would turn a
    missing mount into an outage.
    """
    mount = os.environ.get("MOUNT_PATH") or os.environ.get("TARGET_DIR")
    if total <= 0 or not mount:
        return None
    try:
        st = os.statvfs(mount)
    except OSError:
        return None

    # Capacity (used + available), not free space: bytes already present from a
    # partial download count toward the total measured above, so comparing
    # against available alone would abort the resumes that would have succeeded.
    capacity = ((st.f_blocks - st.f_bfree) + st.f_bavail) * st.f_frsize
    if capacity <= 0 or total <= capacity:
        return None

    lines = [
        "The selected files do not fit the cache volume.",
        f"  required:  {total} bytes (filter: {source})",
        f"  volume:    {capacity} bytes at {mount}",
        f"  declared:  {os.environ.get('EXPECTED_SIZE_BYTES', 'unset')} bytes",
    ]
    if source == SOURCE_LAYOUT_OVERRIDE:
        lines.append(
            "This repository stores required components in subdirectories, so the "
            "whole repository must be downloaded. Raise spec.size on the AIMArtifact "
            "to at least the required size and recreate it."
        )
    else:
        lines.append(
            "Raise spec.size on the AIMArtifact to at least the required size, "
            "or narrow spec.downloadFilter, then recreate it."
        )
    return "\n".join(lines)


def _cli_export():
    """Print the resolved filter for the shell to read: include, exclude, source.

    Deliberately plain values rather than shell assignments: the caller reads
    them with `read`, so nothing here is ever executed and a pattern arriving
    from a CRD field cannot become a command.
    """
    # Deferred, not top-level: resolution itself needs no HuggingFace SDK, and
    # importing it at module scope would make the unit tests require a package
    # that only exists inside the image. This is the one function that cannot
    # run outside it regardless.
    from huggingface_hub import HfApi
    from huggingface_hub.utils import GatedRepoError, RepositoryNotFoundError

    model_path = os.environ["MODEL_PATH"]
    try:
        info = HfApi().model_info(model_path, files_metadata=True)
    except RepositoryNotFoundError:
        print(f"Repository Not Found: {model_path}", file=sys.stderr)
        return 1
    except GatedRepoError:
        print(f"Model requires authentication: {model_path}", file=sys.stderr)
        print("Set HF_TOKEN environment variable with a valid HuggingFace token.", file=sys.stderr)
        return 2
    except Exception as e:  # noqa: BLE001 - surfaced verbatim to the pod log
        print(f"Failed to fetch model info: {e}", file=sys.stderr)
        return 3

    siblings = info.siblings or []
    resolved = resolve([s.rfilename for s in siblings])
    total = sum(s.size or 0 for s in resolved.apply(siblings))

    print(f"{resolved.summary()} ({total} bytes selected)", file=sys.stderr)

    shortfall = capacity_error(total, resolved.source)
    if shortfall:
        print(shortfall, file=sys.stderr)
        return 5

    values = [
        ",".join(resolved.include),
        ",".join(resolved.exclude),
        resolved.source,
    ]
    # One value per line is the entire protocol, so a newline inside one would
    # silently shift every value after it into the wrong variable.
    if any("\n" in v for v in values):
        print("Filter patterns must not contain newlines", file=sys.stderr)
        return 4
    for value in values:
        print(value)
    return 0


def main(argv):
    if len(argv) != 2 or argv[1] != "export":
        print(f"Usage: {argv[0]} export", file=sys.stderr)
        return 64
    return _cli_export()


if __name__ == "__main__":
    sys.exit(main(sys.argv))
