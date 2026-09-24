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

# Shared filter resolution for the hf:// download path.
#
# Sourced (not executed) so the resolved values land in the caller's environment
# and are inherited by the download and both verification stages. If each stage
# resolved for itself they could disagree with one another.

# Importable via PYTHONPATH=/opt; overridable so the scripts can be exercised
# outside the image.
HF_FILTER_MODULE="${HF_FILTER_MODULE:-hf_filter}"

# Resolve the effective filter once per pod. Safe to call repeatedly: the
# entrypoint resolves up front, while adapter-stage.sh reaches hf-download.sh
# directly and resolves there instead.
#
# Resolution needs the repository listing, which simulation mode cannot provide,
# so there it falls back to the operator's raw patterns.
ensure_resolved_filter() {
    [ -n "${AIM_RESOLVED_FILTER_SOURCE:-}" ] && return 0

    if [ -n "${AIM_DEBUG_SIMULATE_HF_DOWNLOAD:-}" ]; then
        AIM_RESOLVED_INCLUDE="${AIM_HF_INCLUDE:-}"
        AIM_RESOLVED_EXCLUDE="${AIM_HF_EXCLUDE:-}"
        AIM_RESOLVED_FILTER_SOURCE="simulated"
    else
        _resolved=$(python -m "$HF_FILTER_MODULE" export) || {
            echo "ERROR: could not resolve the download filter for ${MODEL_PATH:-unknown}" >&2
            return 1
        }
        # One value per line, in the order hf_filter's `export` prints them.
        # `IFS= read -r` takes each line literally, so a pattern that reached
        # the CRD cannot be word-split, glob-expanded or executed here.
        {
            IFS= read -r AIM_RESOLVED_INCLUDE
            IFS= read -r AIM_RESOLVED_EXCLUDE
            IFS= read -r AIM_RESOLVED_FILTER_SOURCE
        } <<EOF
$_resolved
EOF
        if [ -z "${AIM_RESOLVED_FILTER_SOURCE:-}" ]; then
            echo "ERROR: incomplete filter resolution for ${MODEL_PATH:-unknown}" >&2
            return 1
        fi
    fi

    export AIM_RESOLVED_INCLUDE AIM_RESOLVED_EXCLUDE AIM_RESOLVED_FILTER_SOURCE
}
