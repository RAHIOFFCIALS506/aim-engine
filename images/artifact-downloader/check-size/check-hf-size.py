# MIT License

# Copyright (c) 2025 Advanced Micro Devices, Inc.

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

import os
import sys
from huggingface_hub import HfApi
from huggingface_hub.utils import RepositoryNotFoundError, GatedRepoError

import hf_filter

MODEL_PATH = os.environ['MODEL_PATH']

def apply_filter(siblings):
    # This job runs in its own pod, so it resolves from its own listing. It must
    # reach the same verdict as the download job or the PVC is sized for a
    # different file set than the one that gets downloaded.
    resolved = hf_filter.effective([f.rfilename for f in siblings])
    print(resolved.summary(), file=sys.stderr)
    before = len(siblings)
    siblings = resolved.apply(siblings)
    if before != len(siblings):
        print(f'Filter: {before} files -> {len(siblings)} files', file=sys.stderr)
    return siblings

try:
    info = HfApi().model_info(MODEL_PATH, files_metadata=True)
    siblings = apply_filter(info.siblings or [])
    print(sum(f.size or 0 for f in siblings))
except RepositoryNotFoundError:
    print(f'Repository Not Found: {MODEL_PATH}', file=sys.stderr)
    print('Check the model name or ensure it exists on HuggingFace.', file=sys.stderr)
    sys.exit(1)
except GatedRepoError:
    print(f'Model requires authentication: {MODEL_PATH}', file=sys.stderr)
    print('Set HF_TOKEN environment variable with a valid HuggingFace token.', file=sys.stderr)
    print(f'Cannot access gated repo: {MODEL_PATH}', file=sys.stderr)
    sys.exit(2)
except Exception as e:
    print(f'Failed to fetch model info: {e}', file=sys.stderr)
    sys.exit(3)
