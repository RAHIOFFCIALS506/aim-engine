# AIM CLI Design

Design notes for an `aim` CLI built into aim-engine.

## Design principles

- **Follow kubectl conventions** where they exist (verb ordering, `-n`, `-o`, `-l`, `-f`, `--all-namespaces`). Users shouldn't have to learn a new muscle memory.
- **Bulk operations are first-class.** Most commands accept multiple names, label selectors, and file/stdin input. This matches `kubectl delete pod foo bar`, `kubectl delete -l app=x`, and `kubectl apply -f dir/`.
- **Structured output everywhere.** Every command supports `-o json|yaml|wide|name`. JSON schemas are stable and versioned — they're the integration contract for scripts, CI/CD, and future MCP exposure.
- **Logic separated from CLI wiring.** Every command is a callable Go function with typed I/O, not code embedded in Cobra handlers. This makes the same logic available to tests, SDK consumers, and a future MCP server without refactoring. See [Architecture guidance](#architecture-guidance) and [MCP readiness](#mcp-readiness).

## Command structure

```
aim <resource> <verb> [name...] [flags]    # resource CRUD (multi-name)
aim analyze [resource] [name] [flags]      # diagnostics
aim tree <resource> <name> [flags]         # dependency graph
aim env <resource> <name> [flags]          # env var inspector
aim validate -f <file|dir|-> [flags]       # offline/online validation
aim wait <resource> <name...> [flags]      # poll until condition
aim test [subcommand] [flags]              # chainsaw wrapper
aim apply -f <file|dir|-> [flags]          # declarative apply
aim config [subcommand] [flags]            # user preferences
aim completion <shell>                     # shell completions
```

---

## Bulk operations

Every destructive or read command should accept multiple targets, matching `kubectl` conventions:

| Pattern | Example | Precedent |
|---------|---------|-----------|
| Multiple names | `aim service delete svc1 svc2 -n default` | `kubectl delete pod foo bar` |
| Label selector | `aim service list -l aim.eai.amd.com/model=my-model` | `kubectl get pods -l app=web` |
| All in namespace | `aim model list -n default` | `kubectl get pods -n default` |
| All namespaces | `aim service list -A` | `kubectl get pods -A` |
| File/dir input | `aim apply -f dir/` or `aim validate -f -` | `kubectl apply -f dir/` |
| Stdin list | `crane ls rocm/vllm-openai \| aim model add -` | `kubectl delete pods -` (not standard, but common in tooling) |

For `aim model add` specifically — multiple positional args with shared flags:

```bash
aim model add rocm/vllm-openai:latest rocm/vllm-mistral:latest rocm/vllm-llama:latest
```

All share the same `--cluster-scoped`, `--extract-metadata`, etc. Per-model customization uses `aim apply -f` with individual resource manifests.

For `aim wait` — multiple names, exit when all satisfy the condition:

```bash
aim wait service svc1 svc2 svc3 -n default --for=running --timeout=10m
```

---

## Tier 1 — Primary features

### 1. Quick model onboarding — `aim model add`

The most-requested workflow. Collapse AIMClusterModel creation into a single command with smart defaults.

```bash
# Single image
aim model add rocm/vllm-openai:latest
aim model add rocm/vllm-openai:latest --name my-model --namespace dev

# Multiple images (shared flags)
aim model add rocm/vllm-openai:latest rocm/vllm-mistral:latest --cluster-scoped

# From a list (one image per line)
cat images.txt | aim model add -
crane ls rocm/vllm-openai | aim model add --cluster-scoped -

# Custom model
aim model add --custom --image rocm/vllm-openai:latest \
  --source-uri hf://meta-llama/Llama-3.1-70B --gpu-count 4 --gpu-model MI300X
```

Defaults: cluster-scoped, `--extract-metadata`, `--create-templates` on, name derived from image tag. These match the most common usage and can be overridden.

**Flag mechanics:**

- `--extract-metadata` — the CLI reads OCI image labels client-side and pre-fills `spec.imageMetadata`, so the operator skips remote extraction and can create templates immediately. Without this flag, `imageMetadata` is left empty and the operator extracts it asynchronously.
- `--create-templates` — implied by metadata presence. When metadata contains `RecommendedDeployments`, the model controller creates matching AIMServiceTemplates automatically. Disabling this flag would set `spec.discovery.enabled=false` to suppress template creation.
- `--custom` — maps to `spec.custom` + `spec.customTemplates`. `--source-uri` populates `customTemplates[0].modelSources[0].sourceUri`, and `--gpu-count`/`--gpu-model` populate `customTemplates[0].hardware.gpu`. For multi-template custom models, use `aim apply -f` with a full manifest.

### 2. Cluster/namespace diagnostics — `aim analyze`

Modeled on `istioctl analyze`. Targets "something is wrong but I don't know where to start." Runs a suite of checks and reports structured diagnostics.

```bash
aim analyze                                  # full cluster
aim analyze -n my-namespace                  # namespace-scoped
aim analyze service my-svc -n default        # single resource deep-dive
aim analyze -f resources.yaml --use-kube=false  # offline validation
aim analyze -o json                          # machine-parseable
```

**Diagnostic rules** (examples, non-exhaustive, should be a pluggable registry):

| Code | Severity | Offline | Check |
|------|----------|---------|-------|
| AIM001 | ERROR | | AIMService not Running — show condition causing failure |
| AIM002 | ERROR | | InferenceService missing for Running/Starting service (label-selector lookup) |
| AIM003 | ERROR | | Pod CrashLoopBackOff / ImagePullBackOff — include last log line |
| AIM004 | WARN | | Template using unoptimized profile — discovery may not have completed |
| AIM005 | WARN | | Artifact download stalled (progress unchanged >30m) |
| AIM006 | WARN | | RuntimeConfig conditions False — may block dependent services |
| AIM007 | WARN | | ModelSource sync not completed recently |
| AIM008 | INFO | | Template in preview profile — not yet production-optimized |
| AIM009 | INFO | | Discovery job running — template profile pending |
| AIM100 | ERROR | yes | AIMServiceModel: not exactly one of `name`, `image`, or `custom` set |
| AIM101 | ERROR | yes | AIMServiceModelCustom: no model sources specified |
| AIM102 | ERROR | yes | AIMArtifact `sourceUri` doesn't match `^(hf\|s3)://` |
| AIM103 | ERROR | yes | AIMHardwareRequirements: neither `gpu` nor `cpu` specified |
| AIM104 | ERROR | yes | AIMGpuRequirements: `model` and `minVram` both set (mutually exclusive) |
| AIM105 | ERROR | yes | AIMModelSpec with `modelSources` but no hardware on `custom` or `customTemplates` |
| AIM106 | WARN | yes | AIMService references model `name` not found in file set |
| AIM107 | WARN | yes | AIMService references template `name` not found in file set |
| AIM108 | WARN | yes | AIMServiceTemplate `modelName` not found in file set |
| AIM109 | WARN | yes | AIMService/AIMModel/AIMServiceTemplate `runtimeConfigName` not found in file set |

Offline rules (AIM100+) run without cluster access via `--use-kube=false`. AIM100–AIM105 mirror CRD CEL/pattern validations. AIM106–AIM109 are cross-reference checks — WARN not ERROR because the referenced resource may already exist in the cluster. Use `--server-dry-run` via `aim validate` for authoritative admission checks.

Output format follows istioctl convention:
```
ERROR [AIM001] (AIMService/my-svc.default) Not running: condition Ready=False, reason: PodFailure
WARN  [AIM004] (AIMServiceTemplate/llama-tpl.default) Unoptimized profile — discovery attempts: 3
```

Making rules a registry (`[]Analyzer` with `func Check(ctx, state) []Diagnostic`) keeps them independently testable and easy to extend.

### 3. Service lifecycle — `aim service`

Full CRUD for the most common resource type:

```bash
aim service create my-svc --model my-model -n default
aim service create my-svc --image rocm/vllm-openai:latest -n default
aim service list [-n ns] [--status pending|starting|running|degraded|failed] [-l key=val]
aim service list -A                        # all namespaces
aim service get my-svc -n default
aim service describe my-svc -n default     # conditions + pods + events + ISVC
aim service edit my-svc -n default --replicas 3
aim service delete svc1 svc2 -n default    # multiple names
aim service delete -l aim.eai.amd.com/model=my-model -n default
aim service logs my-svc -n default [-f] [--container name]
aim service events my-svc -n default
aim service pods my-svc -n default
```

`logs`, `events`, and `pods` resolve the derived InferenceService name (via `GenerateDerivedName`) to find the correct child resources — this is the key value-add over raw `kubectl`, since the hash-suffixed names aren't guessable.

`create` supports two of the three `AIMServiceModel` modes: `--model <name>` (reference by name) and `--image <uri>` (by container image). The third mode (`custom`, with inline model sources) requires more fields than flags can ergonomically express — use `aim apply -f` with a YAML manifest for that path.

`describe` is the power subcommand — gathers everything a user needs to debug a service in one call: status, conditions, resolved model/template/runtimeconfig, InferenceService health, pod status, recent events. Note: resolved references (`ResolvedModel`, `ResolvedTemplate`, `ResolvedRuntimeConfig`) are only populated when the upstream resource is Ready. `describe` should handle nil references gracefully (e.g. "Model: pending resolution").

### 4. Full resource CRUD

Every CRD type gets the standard verb set:

```
aim {service|model|template|artifact|cache|runtimeconfig|modelsource} \
    {create|get|list|describe|edit|delete|clone} [name...] [flags]
```

Bulk-capable verbs: `get`, `delete`, `describe` accept multiple names. `list` accepts `-l` selectors and `-A`. `clone` is single-resource only (needs a `--name` for the copy).

Cluster-scoped resources (`model`, `template`, `runtimeconfig`, `modelsource`) accept `--cluster-scoped` or have dedicated aliases (`aim clustermodel`).

**Resource alias mapping:**

| CLI alias | CRD(s) |
|-----------|--------|
| `service` | AIMService |
| `model` | AIMModel / AIMClusterModel |
| `template` | AIMServiceTemplate / AIMClusterServiceTemplate |
| `artifact` | AIMArtifact |
| `cache` | AIMTemplateCache (shorthand — no `AIMCache` CRD exists) |
| `runtimeconfig` | AIMRuntimeConfig / AIMClusterRuntimeConfig |
| `modelsource` | AIMClusterModelSource (cluster-scoped only) |

---

## Tier 2 — High-value features

### 5. Dependency tree — `aim tree`

AIM's derived naming (SHA-256 hash suffixes via `GenerateDerivedName`) makes it hard to manually trace resource relationships. This command resolves the full graph.

```
$ aim tree service my-svc -n default

AIMService/my-svc (Running)
├── AIMModel/vllm-openai (Ready) [cluster]
│   └── AIMClusterModelSource/registry-amd
├── AIMServiceTemplate/vllm-openai-mi300x-4gpu (Ready, optimized)
│   └── AIMTemplateCache/vllm-openai-mi300x-4gpu-1080a608 (Ready)
│       ├── AIMArtifact/weights-a3f2b1 (Ready, 140Gi)
│       └── AIMArtifact/config-b7c3d2 (Ready, 2Gi)
├── AIMRuntimeConfig/prod-config (Ready)
├── InferenceService/my-svc-1080a608 (Ready)
│   └── Pod/my-svc-1080a608-predictor-00001-abc (Running, 4×MI300X)
└── HTTPRoute/my-svc-1080a608
```

Key advantage: this code lives in aim-engine and can import `GenerateDerivedName` directly for exact derived-name computation rather than heuristic matching.

### 6. Env var inspector — `aim env`

The four-layer env merge and the spec-vs-profile distinction confuse users. A dedicated command shows exactly what ends up on the container and where each value comes from.

```
$ aim env service my-svc -n default

EFFECTIVE ENV (lowest → highest precedence: system defaults → RuntimeConfig → Template → Service)
  AIM_CACHE_PATH=/aim/cache                  ← system default
  VLLM_ENABLE_METRICS=true                   ← system default
  VLLM_WORKER_MULTIPROC_METHOD=spawn         ← AIMRuntimeConfig/prod-config
  HIP_FORCE_DEV_KERNARG=1                    ← AIMServiceTemplate/vllm-tpl
  NCCL_MIN_NCHANNELS=112                     ← AIMServiceTemplate/vllm-tpl
  MAX_MODEL_LEN=4096                         ← AIMService/my-svc (overrides 8192)

PROFILE ENV (informational — raw discovery output, may differ from effective):
  VLLM_USE_TRITON_FLASH_ATTN=0
  NCCL_NET_GDR_LEVEL=3
```

This reuses the `buildMergedEnvVars` logic from `internal/aimservice/kserve.go`. The four precedence layers are: system defaults (immutable, e.g. `AIM_CACHE_PATH`), RuntimeConfig env, Template env, and Service env (highest wins). `AIM_ENGINE_ARGS` is deep-merged as JSON across layers rather than overwritten.

### 7. Wait / poll — `aim wait`

For CI/CD pipelines and scripting:

```bash
aim wait service my-svc -n default --for=running --timeout=10m
aim wait service svc1 svc2 svc3 -n default --for=running --timeout=10m
aim wait artifact -l aim.eai.amd.com/cache.name=my-cache -n default --for=ready --progress
```

Multiple names and label selectors supported — exits when all targets satisfy the condition (or first failure with `--fail-fast`). `--progress` shows artifact download percentage. Exit code reflects success/failure/timeout.

### 8. Declarative apply — `aim apply`

```bash
aim apply -f service.yaml
aim apply -f ./aim-resources/          # all YAML in directory
aim apply -f - < manifest.yaml         # stdin
aim apply -f svc.yaml -f model.yaml    # multiple files
```

Follows `kubectl apply` conventions: directory recursion, multi-doc YAML, stdin. Combined with `aim validate`, enables a safe pipeline: `aim validate -f dir/ && aim apply -f dir/`.

### 9. Chainsaw wrapper — `aim test`

Speculative — scope and UX to be expanded once the core CLI (v0.1–v0.2) is stable.

```bash
aim test list [--filter pattern]
aim test run path/to/test/ [-n ns] [--timeout 10m]
aim test results <session-id>
```

---

## Tier 3 — Power user features

### 10. Validation — `aim validate`

Exposes the operator's actual admission validation logic. Useful standalone and as a library for other tools.

```bash
aim validate -f service.yaml                    # offline checks
aim validate -f ./aim-resources/                # validate a directory
aim validate -f - < manifest.yaml               # stdin (piping from generators)
aim validate -f service.yaml --server-dry-run   # full admission check
aim validate -f service.yaml -o json            # structured output
```

Returns field-level errors, warnings, and the defaults that admission would apply. Multi-file/dir support makes this a CI gate: `aim validate -f deploy/aim/`.

### 11. Interactive TUI — `aim dashboard`

Optional `bubbletea`-based terminal UI showing live resource status. Think `k9s` but AIM-aware.

### 12. Config profiles — `aim config`

```bash
aim config set-context prod --kubeconfig ~/.kube/prod --namespace aim-prod
aim config use-context prod
aim config set defaults.gpu-model MI300X
```

Stores per-user defaults so `--gpu-model` and `--runtime-config` don't need repeating.

### 13. kubectl plugin compatibility

Name the binary `kubectl-aim` so it works as both `aim analyze` and `kubectl aim analyze`. Just a naming convention — no code changes required.

---

## Architecture guidance

### Separate logic from CLI wiring

Every command should be a callable function, not embedded in Cobra handlers. This keeps the logic testable, reusable, and callable from other contexts (MCP server, SDK, tests).

```go
// pkg/analyze — pure logic
func Run(ctx context.Context, conn ConnectionProvider, opts Options) ([]Diagnostic, error)

// cmd/analyze.go — thin Cobra wrapper
func newAnalyzeCmd() *cobra.Command { /* parse flags → call analyze.Run → format output */ }
```

### Typed input/output structs

Every command gets an `Options` input and a typed result. These naturally become JSON schemas for `-o json` output and, if needed later, MCP tool definitions.

### Centralize K8s connection management

```go
type ConnectionProvider interface {
    GetConnection(kubeconfig, context string) (*K8sConnection, error)
}
```

CLI mode creates a fresh connection per invocation. A future daemon mode could maintain a pool. Business logic is agnostic to which mode it's in.

### Analyzer rule registry

```go
type Analyzer interface {
    Name() string
    Check(ctx context.Context, state *ClusterState) []Diagnostic
}
```

Rules are independently testable, documentable, and extensible. New CRD features get new analyzers without touching existing code.

### Dynamic shell completions

Use Cobra's `ValidArgsFunction` to query the cluster for resource names, namespaces, and contexts at tab-completion time. This significantly improves the CLI experience for interactive use.

---

## MCP readiness

Not building an MCP server now, but the architecture above keeps the door open. An `aim serve` subcommand could expose CLI capabilities as MCP tools — persistent K8s connections, self-describing tool schemas, and watch-backed state for instant `analyze`/`tree` results.

The key architecture decisions that enable this:

| Architecture decision | How it enables MCP |
|---|---|
| Logic in `pkg/`, not in Cobra handlers | MCP handler calls `analyze.Run()` directly — same function, different transport |
| Typed `Options` + result structs | Auto-generate MCP tool JSON schemas from Go struct tags |
| `ConnectionProvider` interface | CLI mode: fresh connection per call. MCP mode: pooled connections. Business logic unchanged |
| `-o json` on every command | JSON output schemas = MCP tool response schemas. Already tested and stable |
| Analyzer rule registry | MCP can expose `analyze` as a single tool or individual rules as separate tools |

No premature abstraction needed — these are just good Go patterns. If MCP never ships, nothing is wasted.

---

## Source and documentation reference

Key source locations for CLI implementation:

| What | Path |
|------|------|
| CRD type definitions | `api/v1alpha1/*_types.go` — all Spec, Status, enums, CEL markers |
| Shared types (namespace/cluster pairs) | `api/v1alpha1/aimmodel_shared.go`, `aimservicetemplate_shared.go`, `aimruntimeconfig_shared.go` |
| Common types and enums | `api/v1alpha1/common_types.go`, `runtime.go`, `discovery.go` |
| Constants (status values, env var names) | `internal/constants/constants.go` |
| Derived name generation | `internal/utils/naming.go` — `GenerateDerivedName()` |
| Env var merge logic | `internal/aimservice/kserve.go` — `buildMergedEnvVars()` |
| Template auto-selection | `internal/aimservice/selection.go` |
| Runtime config merge | `internal/aimruntimeconfig/observe.go` — `FetchMergedRuntimeConfig()` |
| Domain reconcilers | `internal/aim{service,model,artifact,servicetemplate,templatecache,clustermodelsource}/` |
| Controller wiring | `internal/controller/*_controller.go` |
| Pipeline framework | `internal/controller/utils/reconciler.go` |

Existing documentation:

| Topic | Path |
|-------|------|
| CRD API reference | `docs/docs/reference/crds/v1alpha1/aim.eai.amd.com.md` |
| Concepts (service, models, templates, caching, runtime-config, model-sources) | `docs/docs/concepts/` |
| Controller patterns and state engine | `docs/docs/dev/controller-patterns.md`, `state-engine.md` |
| Usage guides (services, kv-cache) | `docs/docs/usage/` |
| KServe admin config | `docs/docs/admin/kserve-configuration.md` |

**Documentation gaps** (not blocking for CLI, but worth creating):

- No dedicated doc for the env merge precedence model — currently only documented in `kserve.go` comments. A `docs/docs/concepts/env-vars.md` would benefit both CLI `aim env` implementation and end users.
- No doc covering the derived naming scheme (`GenerateDerivedName`, hash suffixes, 63-char limits). A brief reference would help CLI `aim tree` implementers and users debugging resource names.
- No doc for the template auto-selection algorithm — logic lives only in `internal/aimservice/selection.go`. Relevant for `aim service create` (implicit template) and `aim analyze` (template selection diagnostics).

---

## Priority ordering

| Phase | Features | Rationale |
|-------|----------|-----------|
| **v0.1** | `model add`, `service` CRUD, `analyze`, `validate`, output formats | Core onboarding and debugging workflows |
| **v0.2** | `tree`, `env`, `wait`, `apply`, remaining resource CRUD | Fills out the operational toolkit |
| **v0.3** | `test`, `config`, `dashboard` TUI, kubectl plugin | Power-user and CI/CD features |
| **future** | MCP server mode | When IDE agent adoption warrants it |
