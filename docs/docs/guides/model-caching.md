# Model Caching

Model caching pre-downloads model artifacts to shared persistent volumes, reducing startup time and bandwidth usage across service replicas and restarts.

:::{admonition} v1alpha2
:class: note

Examples on this page use `aim.eai.amd.com/v1alpha2`. Caching is keyed by the **resolved profile** (via `AIMProfileCache`) instead of the v1alpha1 template (`AIMTemplateCache`). The `spec.caching.mode` field and its semantics are unchanged across versions. For the legacy template-based cache, see [Legacy AIMService](../legacy/aimservice-v1alpha1.md).
:::
## Caching Modes

Control caching behavior with `spec.caching.mode`:

| Mode | Behavior |
|------|----------|
| `Shared` | Reuses shared cache assets across services that resolve to the same profile. This is the **default**. |
| `Dedicated` | Creates service-owned dedicated cache assets, isolated from other services. |

```yaml
apiVersion: aim.eai.amd.com/v1alpha2
kind: AIMService
metadata:
  name: qwen-chat
  annotations:
    aim.eai.amd.com/reconciler-pipeline: profile
spec:
  model:
    name: qwen-qwen3-32b
  caching:
    mode: Shared
```

:::{note}
The caching mode is immutable after creation. Legacy values `Always`, `Auto`, and `Never` are accepted for backward compatibility (`Always`/`Auto` map to `Shared`, `Never` maps to `Dedicated`).
:::
## How Caching Works

When caching is active, AIM Engine creates a hierarchy of resources:

1. **AIMProfileCache** (v1alpha2) — Groups all model artifacts for a specific resolved profile on a shared PVC. (v1alpha1 used `AIMTemplateCache`, keyed by template.)
2. **AIMArtifact** — Manages the download of individual model sources
3. **PVC + Download Job** — The actual storage and download execution

When `AIMProfile.spec.caching.enabled` is set, the shared profile cache is owned
by the profile, so multiple services using that profile reuse it. A dedicated
cache is owned by its service and is garbage-collected with the service.

A profile-owned cache resolves the namespace or cluster RuntimeConfig named
`default`; `AIMProfile.spec.caching` has no `runtimeConfigName` selector.
Consequently, selecting a different RuntimeConfig on an `AIMService` does not
retarget that shared cache. Service-owned caches use the service's selected
RuntimeConfig.

## Download Protocols

For HuggingFace models (`hf://` sources), AIM Engine tries download protocols in sequence:

| Protocol | Description |
|----------|-------------|
| `XET` | XetHub protocol (fastest for large models) |
| `HF_TRANSFER` | HuggingFace Transfer (optimized multi-part download) |
| `HTTP` | Standard HTTP download (slowest, most compatible) |

The default order is `XET,HF_TRANSFER`. If a protocol fails, the downloader cleans up incomplete files and tries the next one.

Override the protocol order via runtime configuration:

```yaml
apiVersion: aim.eai.amd.com/v1alpha1
kind: AIMRuntimeConfig
metadata:
  name: default
  namespace: ml-team
spec:
  env:
    - name: AIM_DOWNLOADER_PROTOCOL
      value: "HF_TRANSFER,HTTP"
```

## Storage Sizing

AIM Engine automatically sizes PVCs based on discovered model sizes plus a headroom percentage (default 10%). Configure the headroom via cluster runtime config:

```yaml
apiVersion: aim.eai.amd.com/v1alpha1
kind: AIMClusterRuntimeConfig
metadata:
  name: default
spec:
  storage:
    pvcHeadroomPercent: 15
    defaultStorageClassName: longhorn
```

## Repositories with required subdirectories

When no download filter is configured, AIM Engine excludes files in
subdirectories (equivalent to `exclude: ["*/*"]`). This suits flat model
repositories, where subdirectories usually hold alternate formats such as
`onnx/` or `original/` that the runtime does not need.

Repository layouts such as Diffusers are the opposite case: the transformer,
VAE, text encoder, scheduler, and tokenizer each live in their own
subdirectory, and the model cannot load without them. **These are detected and
downloaded whole, with no configuration.** A repository is treated this way
when its root contains `model_index.json`, the manifest a Diffusers pipeline
uses to locate its components.

This is based on repository layout, not inference engine. A Diffusers model
served by vLLM-Omni is downloaded whole, while a flat repository served by the
same engine still has its subdirectories skipped.

Detection keys off the manifest, so a repository that splits its weights across
subdirectories without publishing one is not detected and has those
subdirectories skipped. Model families often ship both an original-format
repository and a converted `-Diffusers` one; only the converted repository
carries the manifest, and the `Diffusers` tag on a model page describes the
library that can load it, not the layout on disk. If a repository keeps
required weights in subdirectories but has no root `model_index.json`, set an
[explicitly empty filter](#overriding-the-behaviour).

### Overriding the behaviour

Configuring a filter disables the detection for that artifact, because an
explicit filter is applied exactly as written. This matters if you want to skip
part of a multi-directory repository — for example a large alternate-precision
variant:

```yaml
apiVersion: aim.eai.amd.com/v1alpha1
kind: AIMRuntimeConfig
metadata:
  name: diffusers-model
  namespace: ml-team
spec:
  storage:
    downloadFilter:
      exclude: ["*.fp32.safetensors"]
```

For a service-owned cache, reference that RuntimeConfig from the relevant
`AIMService`:

```yaml
spec:
  runtimeConfigName: diffusers-model
```

For a profile-owned shared cache, configure the download filter on the
namespace or cluster RuntimeConfig named `default` instead.

To force a complete download for a layout that is not detected, set an
explicitly empty filter:

```yaml
spec:
  storage:
    downloadFilter: {}
```

### Recovering an incomplete cache

Detection applies at download time only. An artifact cached before this
behaviour existed stays `Ready` with its subdirectories missing, and the model
keeps failing to load with a missing tokenizer or scheduler. Recreate the
artifact to re-download it — see
[Deletion behaviour](../concepts/caching.md#deletion-behaviour) for what each
delete does and in what order.

## Download Verification

After each download completes, AIM Engine automatically verifies that all expected files are present on disk and persisted to storage. If verification fails, the download job retries with a clean state. No configuration is required — verification runs by default for all HuggingFace downloads.

For details on how verification works, see [Download Verification](../concepts/caching.md#download-verification).

## Monitoring Cache Status

Check the status of profile caches and artifacts:

```bash
# List profile caches (v1alpha2)
kubectl get aimprofilecache -n <namespace>

# List artifacts
kubectl get aimartifact -n <namespace>

# Check artifact download progress
kubectl get aimartifact <name> -n <namespace> -o jsonpath='{.status}' | jq
```

For services still managed by the v1alpha1 template pipeline, list caches with `kubectl get aimtemplatecache -n <namespace>` instead.

## Next Steps

- [Model Caching Concepts](../concepts/caching.md) — Cache hierarchy, ownership, and deletion behavior
- [Storage Configuration](../admin/storage-configuration.md) — PVC and storage class setup
- [Environment Variables](../reference/environment-variables.md) — Downloader configuration
