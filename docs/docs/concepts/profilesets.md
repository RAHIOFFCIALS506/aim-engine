# AIM Profile Sets

`AIMProfileSet` and `AIMClusterProfileSet` are the **derivation engine** of v1alpha2. They select source profiles (by label and spec criteria), apply overrides, and produce derived `AIMProfile` / `AIMClusterProfile` resources.

You'll encounter profile sets in two ways:

1. **Implicitly**, as children of `AIMModel.spec.profiles` — image-backed
   derivation, fine-tuned, and custom-model flows synthesise an
   `AIMProfileSet` named `<model>-profiles-<hash>` and summarise its output.
2. **Directly**, as standalone resources you author yourself when you want to derive profiles without creating an owning `AIMModel`.

The same internal primitive handles both — `AIMModel.spec.profiles` is translated into an `AIMProfileSetSpec` for the synthesised child set.

:::{admonition} v1alpha2
:class: note

`AIMProfileSet`/`AIMClusterProfileSet` are new CRDs in `aim.eai.amd.com/v1alpha2` — v1alpha1 had no first-class derivation resource. The closest legacy primitive is `AIMModel.spec.profileCopy`, which performed an inline copy step against existing AIMServiceTemplates; v1alpha2 reshapes that into `AIMModel.spec.profiles` (which synthesises a child profile set), and `spec.profileCopy` is forbidden on v1alpha2. See [Migrating to v1alpha2 → AIMModel mapping](../legacy/migrating.md#aimmodel-mapping) for the full field-by-field translation.
:::
## Cluster vs namespace scope

| Resource | Scope | Produces |
|---|---|---|
| `AIMProfileSet` | Namespace | `AIMProfile` in the same namespace |
| `AIMClusterProfileSet` | Cluster | `AIMClusterProfile` across the cluster |

For sourcing:

- In real-profile-backed mode, `AIMProfileSet` prefers namespace-scoped
  `AIMProfile` objects in its own namespace and falls back to
  `AIMClusterProfile` objects when no namespace candidate matches. An explicit
  `selector.modelRef.scope` removes that fallback ambiguity.
- `AIMClusterProfileSet` selects from cluster-scoped `AIMClusterProfile` objects.
- `sourceRef.kind: AIMModel` resolves an image-backed `AIMModel` in the
  `AIMProfileSet` namespace.
- `sourceRef.kind: AIMClusterModel` resolves an image-backed
  `AIMClusterModel`. Both profile-set scopes may consume a cluster model.
- `sourceRef.kind: ConfigMap` is read from the same namespace for
  `AIMProfileSet` and from the operator namespace for
  `AIMClusterProfileSet`. Omitting `kind` retains this ConfigMap behavior for
  backwards compatibility.

## Source modes

A profile set selects sources according to `spec.sourceRef`:

| Mode | `spec.sourceRef` | Source pool |
|---|---|---|
| **Real-profile-backed** | omitted | Existing `AIMProfile` / `AIMClusterProfile` objects in the set's scope |
| **Model-catalog-backed** | `kind: AIMModel` or `kind: AIMClusterModel` | The referenced model's complete, current image-discovery catalog |
| **ConfigMap-catalog-backed** | `kind: ConfigMap` or omitted | Entries from the named discovery-cache `ConfigMap` |

The modes are mutually exclusive — the controller doesn't mix source pools in
a single reconcile.

Cache-backed mode is used internally by an `AIMModel` or `AIMClusterModel`
that combines `spec.image` with `spec.profiles`. The model controller injects
its own discovery-cache reference into the child profile set. Users must not
set `profiles.derivedFrom.sourceRef` or `selector.modelRef` in that combined
shape because the image already defines the source scope.

A standalone profile set should normally reference the source model rather than
naming its internal ConfigMap:

```yaml
sourceRef:
  kind: AIMClusterModel
  name: llama-3-1-8b-instruct
```

The controller waits until that model has published a discovery cache for its
current generation, verifies that the cache belongs to the referenced model
and matches its current image/discovery inputs, then reads it. Direct
`kind: ConfigMap` remains available for user-authored or externally managed
catalogs.

The cache contains the complete image catalog, including entries that were not
materialised as profile CRs because their original hardware requirements do not
match the current cluster. A cache-backed set can therefore select one of those
entries, apply overrides, and materialise only the resulting profile. Catalog
candidates participate in the same `role` and `origin` filtering as real
profiles: their role is derived from the source spec and their origin is
`discovered`.

Keeping the complete catalog in the model-owned cache is deliberate. An
alternative would be to materialise every discovered entry as an ordinary
`AIMProfile` / `AIMClusterProfile`, including entries unsupported by the
cluster. That would make unsupported source material visible to normal service
selection, distort model readiness and managed-profile counts, and add API
server, etcd, and watch traffic for resources that cannot be deployed as-is.
The private catalog keeps discovery complete while materialising only profiles
that are either natively supported or explicitly selected and transformed.

`sourceRef` and `selector.modelRef` cannot be combined. They serve different
purposes: `sourceRef` chooses a catalog source, while `selector.modelRef`
filters the pool of already-materialised profiles when `sourceRef` is omitted.
Keeping them separate avoids silently changing existing `modelRef` selectors
from “real profiles” to “the full image catalog.”

A standalone profile set owns its derived profiles. Referencing a model as its
source does not make those outputs part of the model's
`status.managedProfiles`. If services must resolve the outputs through
`spec.model.name`, use `AIMModel.spec.profiles` /
`AIMClusterModel.spec.profiles` so the model owns and readiness-accounts for
the generated profiles. Standalone outputs can instead be selected directly by
`aimId` and the other profile selector fields.

:::{note}
Model-backed profile sets react to both source-model status changes and updates
to the model-owned cache. A ConfigMap with the discovery-catalog shape is also
watched directly. Re-applying or re-annotating the profile set always forces a
fresh read.
:::

## Selector

`spec.selector` chooses candidates from the source pool. At least one matching field must be set (validated by CRD CEL).

| Field | Description |
|---|---|
| `aimId` | Match by source `spec.aimId` (e.g. `meta-llama/Llama-3-8B-Instruct`). Rejected by CEL when `role: base` — target identity for derived profiles lives on `overrides.aimId` instead (see [Identity stamping](#identity-stamping) below). |
| `modelId` | Match by source `spec.modelId`. Rejected by CEL when `role: base` — use `overrides.modelId`. |
| `profileId` | Match by source `spec.profileId`. Rejected by CEL when `role: base` — use `overrides.profileId`. |
| `engine` | Match by inference engine (`vllm`, `tgi`). |
| `metric` | Match `latency` or `throughput`. |
| `precision` | Match by numeric precision (`fp8`, `fp16`, `bf16`, ...). |
| `type` | Match optimization level exactly (`optimized`, `general`, `preview`, `unoptimized`). |
| `minimumType` | Match optimization level as a floor — that tier or better (`optimized > general > preview > unoptimized`), or `any` for no floor. Empty means no floor for derivation selectors (AIMService auto-selection defaults this floor to `optimized` instead). ANDs with `type` when both are set. |
| `acceleratorModel` | Match by accelerator identifier (`MI300X`, `MI325X`, ...). |
| `acceleratorType` | Match `gpu` or `cpu`. |
| `acceleratorCount` | Match by accelerator unit count. |
| `engineArgs` | Partial match on engine args — every provided top-level key must exist on the source with an equal value. |
| `modelRef.name` | In real-profile-backed mode, narrow to materialised profiles produced by a specific AIM(Cluster)Model (matched via the `source-model` label). To read that model's complete image catalog, use `spec.sourceRef.kind: AIMModel` / `AIMClusterModel` instead. |
| `modelRef.scope` | `Auto` (try namespace first, then cluster), `Namespace`, or `Cluster`. Default `Auto`. |
| `role` | `base` or `deployable`. Default `deployable`. |
| `origin` | `discovered`, `derived`, or `user-authored`. When unset, doesn't filter by origin. |

### Identity stamping

Identity for the derived profile is set via `overrides.{aimId,modelId,profileId}`. The selector never writes identity onto the derived profile — its identity fields are always source-side filters (strict equality, empty = wildcard).

When `selector.role: base`, CEL on `AIMProfileSetSpec` and `AIMModelProfilesSpec` enforces this split:

| Rule | Effect |
|---|---|
| `selector.aimId/modelId/profileId` rejected | Base profiles have no source identity to filter on. The fields would silently do nothing useful. |
| `overrides.aimId` required | Base profiles ship with empty `spec.aimId`. The derived profile would otherwise have no identity to bind against from an `AIMService.spec.profile.selector.aimId`. |
| `overrides.modelId` required | Same reasoning for the model-level identifier. (`modelSources[0].modelId` auto-derivation is bypassed for `role: base` — explicit is required.) |
| `overrides.profileId` optional | Most callers don't pin a profileId; the AIMProfile reconciler synthesises one if unset. |

For `role: deployable` derivations (the AIMProfileSet remix case where you're re-deriving an already-deployable profile into accelerator/engine variants), all three identity overrides are optional — the source profile's existing identity carries through unchanged unless you explicitly override it.

The same shape applies to [`AIMModel.spec.profiles.derivedFrom`](../guides/custom-models.md): selector filters source candidates, overrides stamp the target identity onto the derived profile. CEL enforces the split on both surfaces identically.

## Version policy

`spec.versionPolicy` controls how multiple versions of matching source profiles are filtered.

| Policy | Behavior |
|---|---|
| `pinned` (default) | Only match profiles whose version (extracted from `spec.image` tag) equals `spec.version`. Requires `version` to be set. |
| `latest` | Within each (accelerator, precision, metric) group, only match the highest-version profile. |
| `all` | Match every profile that satisfies the selector, regardless of version. |

`pinned` is the typical choice for reproducibility. `latest` auto-tracks upstream improvements. `all` is common for custom-model derivation (where each version represents a different accelerator/precision combo rather than a temporal sequence).

## Overrides

`spec.overrides` mutates selected sources before writing them out as derived profiles.

| Field | Merge semantics |
|---|---|
| `modelSources` | Replaces source `modelSources` entirely. The first source's `modelId` is auto-copied into the derived profile's `spec.modelId`. |
| `acceleratorModel` | Replaces source `acceleratorModel`. |
| `acceleratorCount` | Replaces source `acceleratorCount`. |
| `acceleratorPartitioningMode` | Replaces the source partitioning mode. Must be accompanied by `acceleratorCount`. Select a source whose count/engine arguments already match the intended topology; changing the output count does not automatically rewrite inherited tensor-parallel engine arguments. |
| `containerEnv` | Merges by env-var name; matching names override, new names append. |
| `engineEnv` | Merges by key; matching keys override, new keys append. |
| `engineArgs` | Shallow merge on top of source `engineArgs`; matching top-level keys override, new keys append. |

### Image resolution

When the profile set's top-level `spec.image` is unset, the derived profile's image depends on the source profile's role and whether the override replaces the model weights (`overrides.modelSources`):

| Source profile | Override replaces weights? | Resulting image |
|---|---|---|
| Deployable (`role=deployable`, a model-optimized image) | **Yes** (`modelSources` set) | Resolves back to the source's `status.baseImage` (`AIM_BASE_IMAGE_REF`), normalised to `MAJOR.MINOR` and rebased onto the source image's registry+org. The new weights load onto the clean aim-base runtime. |
| Deployable | **No** (partitioning-only / env-only / engineArgs-only override) | **Keeps the optimized source image.** The model optimizations and tuning still apply, so a re-partition or env tweak stays on the model-optimized image instead of silently downgrading to the base runtime. |
| Base (`role=base`, an aim-base image used for [Custom Models](../guides/custom-models.md)) | — | Keeps the source image unchanged. A base profile already *is* the runtime image; weights come from `overrides.modelSources`. |

An explicit top-level `spec.image` always wins outright, regardless of the above.

Derived profiles inherit the source profile's `status.baseImage` so subsequent derivations or overlays can rebase consistently.

Model-catalog-backed sets also inherit the source model's image pull-secret and
service-account names unless the set overrides them. For a namespace-scoped
`AIMProfileSet` that references an `AIMClusterModel`, resources with those names
must exist in the ProfileSet's destination namespace; Kubernetes secret and
service-account references do not cross namespace boundaries.

### Profile YAML contract inheritance

Derived profiles also inherit the source profile's complete runtime-YAML contract. For real-profile-backed derivation, the contract comes from the source profile's `aim.eai.amd.com/profile-yaml-contract` annotation. For cache-backed derivation, it is inferred directly from each raw profile YAML in the discovery cache. The derived profile carries the same codec, modeled-field presence, and opaque source extensions. Runtime projection uses that information to choose between legacy `gpu` fields, transitional accelerator fields, and strict canonical accelerator fields while retaining runtime-only additions that are not part of `AIMProfile.spec`.

Neither profile derivation nor runtime projection infers compatibility from an image name or tag. In particular, setting the profile set's top-level `spec.image` changes the container image but does **not** inspect that replacement image or choose a new YAML contract. The replacement image must accept the source profile's inherited format.

## Spec shape

```yaml
apiVersion: aim.eai.amd.com/v1alpha2
kind: AIMProfileSet
metadata:
  name: mi308x-variants
  namespace: ml-team
spec:
  selector:
    role: deployable
    modelRef:
      name: llama-3-8b-official
      scope: Auto
    origin: discovered
    acceleratorModel: MI300X
    engineArgs:
      tensor-parallel-size: 2
  versionPolicy: latest
  overrides:
    image: quay.io/acme/aim-base-vllm:0.9.0
    acceleratorModel: MI308X
    acceleratorCount: 4
    engineArgs:
      tensor-parallel-size: 4
      max-model-len: 32768
```

| Field | Description |
|---|---|
| `sourceRef` | Optional catalog source. `kind` is `AIMModel`, `AIMClusterModel`, or `ConfigMap` (the backwards-compatible default); `name` identifies the source. If omitted, source selection uses real profiles. |
| `selector` | Match criteria — see [Selector](#selector). |
| `versionPolicy` | `pinned`, `latest`, `all` — see [Version policy](#version-policy). |
| `version` | Required when `versionPolicy: pinned`. |
| `image` | Optional image override applied to derived profiles. |
| `overrides` | Mutations applied to selected sources — see [Overrides](#overrides). |
| `imagePullSecrets` | Propagated to derived profiles. |
| `serviceAccountName` | Propagated to derived profiles. |

See the [CRD API Reference](../reference/api/v1alpha2.md#aimprofilesetspec) for the full schema.

## Examples

### Real-profile-backed derivation

Standalone profile set that derives MI308X variants from existing MI300X profiles owned by `llama-3-8b-official`:

```yaml
apiVersion: aim.eai.amd.com/v1alpha2
kind: AIMProfileSet
metadata:
  name: mi308x-variants
  namespace: ml-team
spec:
  selector:
    modelRef:
      name: llama-3-8b-official
    acceleratorModel: MI300X
  versionPolicy: latest
  overrides:
    acceleratorModel: MI308X
    acceleratorCount: 4
```

### Model-catalog-backed derivation

Cluster profile set that derives directly from an `AIMClusterModel`'s complete
discovery catalog without naming its internal cache or requiring the source
profile to have been materialised:

```yaml
apiVersion: aim.eai.amd.com/v1alpha2
kind: AIMClusterProfileSet
metadata:
  name: llama-3-1-8b-partitioned
spec:
  sourceRef:
    kind: AIMClusterModel
    name: llama-3-1-8b-instruct
  selector:
    acceleratorModel: MI300X
    acceleratorCount: 1
  versionPolicy: pinned
  version: "0.11.1"
  overrides:
    acceleratorCount: 1
    acceleratorPartitioningMode: CPX-NPS4
```

Use `kind: AIMModel` on an `AIMProfileSet` to resolve a namespace-scoped model
in the same namespace. An `AIMProfileSet` may also use
`kind: AIMClusterModel`; an `AIMClusterProfileSet` cannot reference an
`AIMModel` because a cluster-scoped object has no namespace in which to resolve
one.

For a raw or externally managed catalog, retain the lower-level ConfigMap form:

```yaml
spec:
  sourceRef:
    kind: ConfigMap
    name: qwen3-32b-custom-discovery
  selector:
    acceleratorModel: MI300X
```

### Custom-model derivation (role: base)

The derivation half of the [custom-model flow](../guides/custom-models.md). When authored by an `AIMModel.spec.profiles` it's synthesised automatically; here it's shown standalone for reference:

```yaml
apiVersion: aim.eai.amd.com/v1alpha2
kind: AIMProfileSet
metadata:
  name: custom-derivatives
  namespace: ml-team
spec:
  selector:
    role: base
    modelRef:
      name: aim-base-vllm
      scope: Namespace
  versionPolicy: all
  overrides:
    aimId: acme/custom-transformer
    modelId: acme/custom-transformer
    modelSources:
      - modelId: acme/custom-transformer
        sourceUri: s3://acme-models/custom-transformer
```

## Status

| Field | Description |
|---|---|
| `status` | `Pending`, `Progressing`, `Ready`, `Degraded`, `Failed`, `NotAvailable` |
| `conditions` | Detailed conditions — see [Conditions Reference](../reference/conditions.md) |
| `managedProfiles.total` | Number of derived profiles the set intends to manage |
| `managedProfiles.ready` | Number of managed profiles whose own status is `Ready` |
| `managedProfiles.notAvailable` | Number of managed profiles whose own status is `NotAvailable` |
| `managedProfiles.deployable` | Number of managed profiles that carry `aimId` and `modelSources` (deployable). For typical fine-tune / custom-model derivation this equals `total`. |
| `managedProfiles.base` | Number of managed profiles that don't carry both fields. Non-zero typically means a base-image AIMModel was the source. |

Zero values serialise explicitly (no `omitempty`), so `kubectl` printcolumns and assertions on count fields are always present.

### Conditions

| Condition | Reasons |
|---|---|
| `DerivationReady` | `ManagedProfilesReady`, `NoMatchingProfiles`, `Progressing`, `Degraded` |
| `Ready` | `AllComponentsReady`, `ComponentsNotReady`, `Progressing` |

`DerivationReady=False / NoMatchingProfiles` means the selector matched zero source candidates. This is by far the most common failure mode; see troubleshooting below.

## Derived-profile labels and annotations

Profiles produced by an `AIMProfileSet` carry this metadata:

| Kind | Name | Value |
|---|---|---|
| Annotation | `aim.eai.amd.com/profile-source` | `copy` |
| Annotation | `aim.eai.amd.com/profile-yaml-contract` | Inherited source codec, field presence, and runtime extensions |
| Label | `aim.eai.amd.com/profile-role` | `deployable` (or `base` if the derivation was authored to produce base profiles, which is unusual) |
| Label | `aim.eai.amd.com/profile-origin` | `derived` |
| Label | `aim.eai.amd.com/source-model` | Name of the AIM(Cluster)Model that owns the profile set (when applicable) |
| Label | `aim.eai.amd.com/source-model-scope` | `namespace` or `cluster` |

By default, derived profiles are **not** marked `profile-copyable`, which prevents accidental cascading copy chains. If you want a derived profile to itself be a valid derivation source, set the `aim.eai.amd.com/profile-copyable: "true"` annotation explicitly.

## Source eligibility

For real-profile-backed mode, a source profile must carry `aim.eai.amd.com/profile-copyable="true"` to be considered. This separates provenance ("where did this come from") from source eligibility ("can this be copied"):

- Image-discovery-produced profiles owned by AIM(Cluster)Models are marked copyable by default.
- Derived profiles are not marked copyable by default — opt in explicitly if you want chained derivation.
- Hand-authored profiles need the annotation added explicitly to participate.

Cache-backed mode bypasses this — the entries are taken directly from the discovery catalog.

## Troubleshooting

### `DerivationReady=False / NoMatchingProfiles`

The selector matched zero source candidates. Diagnose by listing candidates and comparing:

```bash
# What did the selector look for?
kubectl get aimprofileset <name> -o jsonpath='{.spec.selector}'

# What profiles are in the pool?
kubectl get aimprofile -n <namespace> -o custom-columns=\
NAME:.metadata.name,\
AIMID:.spec.aimId,\
PRECISION:.spec.precision,\
ACCEL:.spec.acceleratorModel,\
ROLE:.metadata.labels.aim\\.eai\\.amd\\.com/profile-role,\
COPYABLE:.metadata.annotations.aim\\.eai\\.amd\\.com/profile-copyable
```

Common causes:

- A selector field is too restrictive — relax `precision`, `acceleratorModel`, etc.
- `modelRef.name` typo or wrong scope.
- Source profiles lack the `aim.eai.amd.com/profile-copyable: "true"` annotation.
- For model-catalog-backed mode: the referenced model is missing, is not
  image-backed, or has not published a current discovery cache yet.
- For ConfigMap-catalog-backed mode: `spec.sourceRef.name` doesn't reference an
  existing discovery-catalog ConfigMap in the expected namespace.

### `managedProfiles.total == 0` after applying

Either the set hasn't been reconciled yet (look at `status.conditions[?(@.type=='DerivationReady')]`) or every candidate was filtered out before the derived-profile build step. The selector is the most common culprit.

### Model-managed derivation is stuck

When an `AIMModel.spec.profiles` synthesises a profile set, the model's `status.profileSetRef` points at it. Inspect directly:

```bash
kubectl get aimmodel <name> -o jsonpath='{.status.profileSetRef.name}'
kubectl get aimprofileset <profileset-name> -o yaml
```

Conditions on the profile set carry the actionable detail; the model just summarises.

During a non-empty selector or cache transition, stale derived profiles are
retained until every replacement profile exists, matches the desired spec, and
has reported `Ready` for its current generation. A valid zero-match selection
prunes stale outputs immediately because there is no replacement rollout to
wait for.

## Related documentation

- [AIM Models](models.md) — Derivation flows that synthesise profile sets
- [Profiles](profiles.md) — What derived profiles carry; provenance labels
- [Fine-Tuned Models](../guides/fine-tuned-models.md) — End-to-end derivation walkthrough (deployable sources)
- [Custom Models](../guides/custom-models.md) — End-to-end derivation walkthrough (base-profile sources)
- [CRD API Reference (v1alpha2)](../reference/api/v1alpha2.md) — Full schema
- [Conditions Reference](../reference/conditions.md) — Full condition catalogue
