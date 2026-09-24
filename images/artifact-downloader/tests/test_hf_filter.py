# MIT License
#
# Copyright (c) 2026 Advanced Micro Devices, Inc.
#
# Permission is hereby granted, free of charge, to any person obtaining a copy
# of this software and associated documentation files (the "Software"), to deal
# in the Software without restriction, including without limitation the rights
# to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
# copies of the Software, and to permit persons to whom the Software is
# furnished to do so, subject to the following conditions:
#
# The above copyright notice and this permission notice shall be included in all
# copies or substantial portions of the Software.
#
# THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
# IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
# FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
# AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
# LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
# OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
# SOFTWARE.

import os
import sys
import subprocess
import tempfile
import unittest
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

import hf_filter

SCRIPT_DIR = Path(__file__).resolve().parents[1]

FLAT_REPO = ["config.json", "model.safetensors", "onnx/model.onnx", "original/weights.pth"]
DIFFUSERS_REPO = [
    "model_index.json",
    "README.md",
    "vae/config.json",
    "vae/diffusion_pytorch_model.safetensors",
    "tokenizer/tokenizer.json",
    "text_encoder/model.safetensors",
]


def env(**overrides):
    return patch.dict(os.environ, overrides, clear=True)


class ResolveTests(unittest.TestCase):
    def test_default_applies_to_a_flat_repository(self):
        with env(AIM_HF_EXCLUDE_DEFAULT="*/*"):
            resolved = hf_filter.resolve(FLAT_REPO)

        self.assertEqual(resolved.source, hf_filter.SOURCE_DEFAULT)
        self.assertEqual(resolved.exclude, ["*/*"])
        self.assertEqual(
            [f for f in FLAT_REPO if resolved.matches(f)],
            ["config.json", "model.safetensors"],
        )

    def test_default_is_dropped_for_a_multi_directory_layout(self):
        with env(AIM_HF_EXCLUDE_DEFAULT="*/*"):
            resolved = hf_filter.resolve(DIFFUSERS_REPO)

        self.assertEqual(resolved.source, hf_filter.SOURCE_LAYOUT_OVERRIDE)
        self.assertFalse(resolved.active)
        self.assertEqual([f for f in DIFFUSERS_REPO if resolved.matches(f)], DIFFUSERS_REPO)

    def test_user_filter_wins_over_the_default_even_on_a_diffusers_repo(self):
        # Someone deliberately skipping a variant must not have that undone by
        # layout detection.
        with env(AIM_HF_EXCLUDE_DEFAULT="*/*", AIM_HF_EXCLUDE="text_encoder/*"):
            resolved = hf_filter.resolve(DIFFUSERS_REPO)

        self.assertEqual(resolved.source, hf_filter.SOURCE_USER)
        self.assertEqual(resolved.exclude, ["text_encoder/*"])
        self.assertNotIn("text_encoder/model.safetensors", [f for f in DIFFUSERS_REPO if resolved.matches(f)])
        self.assertIn("vae/config.json", [f for f in DIFFUSERS_REPO if resolved.matches(f)])

    def test_user_filter_supplied_as_raw_env_also_wins(self):
        # spec.env overrides the operator's computed defaults, so a filter can
        # reach the pod without ever appearing in spec.downloadFilter.
        with env(AIM_HF_EXCLUDE_DEFAULT="*/*", AIM_HF_INCLUDE="*.safetensors"):
            resolved = hf_filter.resolve(DIFFUSERS_REPO)

        self.assertEqual(resolved.source, hf_filter.SOURCE_USER)
        self.assertEqual(resolved.include, ["*.safetensors"])

    def test_marker_below_the_root_is_not_the_marker(self):
        # A manifest inside a subdirectory describes that subdirectory, not this
        # repository, so the default must survive.
        files = ["config.json", "variant/model_index.json", "variant/weights.bin"]
        with env(AIM_HF_EXCLUDE_DEFAULT="*/*"):
            resolved = hf_filter.resolve(files)

        self.assertEqual(resolved.source, hf_filter.SOURCE_DEFAULT)
        self.assertEqual(resolved.exclude, ["*/*"])

    def test_no_patterns_at_all_means_no_filtering(self):
        with env():
            resolved = hf_filter.resolve(DIFFUSERS_REPO)

        self.assertEqual(resolved.source, hf_filter.SOURCE_NONE)
        self.assertFalse(resolved.active)

    def test_include_and_exclude_are_both_applied(self):
        with env(AIM_HF_INCLUDE="*.json", AIM_HF_EXCLUDE="vae/*"):
            resolved = hf_filter.resolve(DIFFUSERS_REPO)

        self.assertEqual(
            [f for f in DIFFUSERS_REPO if resolved.matches(f)],
            ["model_index.json", "tokenizer/tokenizer.json"],
        )

    def test_patterns_are_split_and_trimmed(self):
        # The operator comma-joins the CRD's pattern list, so whitespace and
        # empty entries have to survive the round trip harmlessly.
        with env(AIM_HF_EXCLUDE=" *.bin , */* ,, "):
            self.assertEqual(hf_filter.resolve(FLAT_REPO).exclude, ["*.bin", "*/*"])

        with env(AIM_HF_EXCLUDE=""):
            self.assertEqual(hf_filter.resolve(FLAT_REPO).source, hf_filter.SOURCE_NONE)

    def test_apply_filters_sibling_objects(self):
        siblings = [SimpleNamespace(rfilename=name, size=1) for name in FLAT_REPO]
        with env(AIM_HF_EXCLUDE_DEFAULT="*/*"):
            kept = hf_filter.resolve(FLAT_REPO).apply(siblings)

        self.assertEqual([s.rfilename for s in kept], ["config.json", "model.safetensors"])


class PodWideResolutionTests(unittest.TestCase):
    """The download and both verification stages must share one verdict."""

    def test_effective_prefers_the_resolution_already_made_for_this_pod(self):
        with env(
            AIM_HF_EXCLUDE_DEFAULT="*/*",
            AIM_RESOLVED_FILTER_SOURCE=hf_filter.SOURCE_LAYOUT_OVERRIDE,
            AIM_RESOLVED_INCLUDE="",
            AIM_RESOLVED_EXCLUDE="",
        ):
            # A stage that re-derived from its own listing would reapply the
            # default here, because this listing has no marker.
            resolved = hf_filter.effective(FLAT_REPO)

        self.assertEqual(resolved.source, hf_filter.SOURCE_LAYOUT_OVERRIDE)
        self.assertFalse(resolved.active)

    def test_effective_falls_back_to_resolving_when_nothing_resolved_yet(self):
        with env(AIM_HF_EXCLUDE_DEFAULT="*/*"):
            resolved = hf_filter.effective(FLAT_REPO)

        self.assertEqual(resolved.source, hf_filter.SOURCE_DEFAULT)

    def test_from_resolved_env_is_none_without_a_recorded_source(self):
        with env(AIM_RESOLVED_EXCLUDE="*/*"):
            self.assertIsNone(hf_filter.from_resolved_env())


class CapacityGuardTests(unittest.TestCase):
    """The spec.size path never lists the repository, so the download pod is the
    only thing that can notice the volume is too small."""

    def check(self, total, used_blocks, available_blocks, source="default", **extra_env):
        stat = SimpleNamespace(
            f_frsize=1024,
            f_blocks=used_blocks + available_blocks,
            f_bfree=available_blocks,
            f_bavail=available_blocks,
        )
        with env(MOUNT_PATH="/cache", **extra_env):
            with patch("os.statvfs", return_value=stat):
                return hf_filter.capacity_error(total, source)

    def test_passes_when_the_resolved_set_fits(self):
        self.assertIsNone(self.check(500_000, used_blocks=100, available_blocks=900))

    def test_reports_a_shortfall_before_downloading(self):
        message = self.check(5_000_000, used_blocks=100, available_blocks=900)

        self.assertIn("do not fit the cache volume", message)

    def test_counts_bytes_already_on_a_resumed_volume_as_capacity(self):
        # 900 KiB already downloaded, 200 KiB free: comparing against free space
        # alone would abort a resume that has room to finish.
        self.assertIsNone(self.check(1000 * 1024, used_blocks=900, available_blocks=200))

    def test_explains_the_layout_override_when_that_is_what_grew_the_set(self):
        message = self.check(
            5_000_000,
            used_blocks=100,
            available_blocks=900,
            source=hf_filter.SOURCE_LAYOUT_OVERRIDE,
            EXPECTED_SIZE_BYTES="1000000",
        )

        self.assertIn("required components in subdirectories", message)
        self.assertIn("1000000", message)

    def test_is_inert_when_the_total_is_unknown(self):
        self.assertIsNone(self.check(0, used_blocks=1, available_blocks=1))

    def test_is_inert_when_the_volume_cannot_be_measured(self):
        with env(MOUNT_PATH="/cache"):
            with patch("os.statvfs", side_effect=OSError("no such mount")):
                self.assertIsNone(hf_filter.capacity_error(5_000_000, "default"))

    def test_is_inert_when_no_mount_is_configured(self):
        with env():
            self.assertIsNone(hf_filter.capacity_error(5_000_000, "default"))


class ExportProtocolTests(unittest.TestCase):
    """The shell reads `export` output positionally, so the two sides have to
    agree on the line order and the shell must never interpret a value."""

    def run_ensure(self, emitted_lines, exit_code=0):
        with tempfile.TemporaryDirectory() as temp:
            module = Path(temp) / "fake_export.py"
            body = "\n".join(f"print({line!r})" for line in emitted_lines)
            module.write_text(f"import sys\n{body}\nsys.exit({exit_code})\n")

            # The image provides `python`; dev machines usually only have python3.
            fake_bin = Path(temp) / "bin"
            fake_bin.mkdir()
            shim = fake_bin / "python"
            shim.write_text(f'#!/bin/sh\nexec "{sys.executable}" "$@"\n')
            shim.chmod(0o755)

            script = (
                f'. "{SCRIPT_DIR}/hf-filter-lib.sh"\n'
                "ensure_resolved_filter || exit 1\n"
                'printf "%s\\n" "$AIM_RESOLVED_INCLUDE" "$AIM_RESOLVED_EXCLUDE" '
                '"$AIM_RESOLVED_FILTER_SOURCE"\n'
            )
            return subprocess.run(
                ["sh", "-c", script],
                env={
                    "PATH": f"{fake_bin}:{os.environ['PATH']}",
                    "PYTHONPATH": str(temp),
                    "HF_FILTER_MODULE": "fake_export",
                    "MODEL_PATH": "org/model",
                },
                text=True,
                capture_output=True,
                check=False,
            )

    def test_values_land_in_the_order_python_emits_them(self):
        result = self.run_ensure(["*.safetensors", "*/*", "default"])

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(
            result.stdout.splitlines(),
            ["*.safetensors", "*/*", "default"],
        )

    def test_a_value_is_never_word_split_or_executed(self):
        hostile = "my files/*;echo INJECTED"
        result = self.run_ensure([hostile, "", "user"])

        # Had the shell interpreted it, the value would be truncated at the
        # space and the echo would contribute a line of its own.
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), [hostile, "", "user"])

    def test_empty_patterns_still_produce_a_usable_resolution(self):
        result = self.run_ensure(["", "", "layout-override"])

        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.splitlines(), ["", "", "layout-override"])

    def test_a_failed_resolution_is_fatal(self):
        result = self.run_ensure([], exit_code=3)

        self.assertEqual(result.returncode, 1)
        self.assertIn("could not resolve the download filter", result.stderr)

    def test_truncated_output_is_fatal_rather_than_half_applied(self):
        result = self.run_ensure(["*.json"])

        self.assertEqual(result.returncode, 1)
        self.assertIn("incomplete filter resolution", result.stderr)


if __name__ == "__main__":
    unittest.main()
