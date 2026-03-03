# AIM CLI Design

Design notes for an `aim` CLI built into aim-engine.

## Design principles

- **Don't duplicate kubectl.** AIM CLI adds value where kubectl can't — debug views, derived name resolution, env merge inspection, diagnostic rules. CRUD and declarative apply are kubectl's job.
- **Follow kubectl conventions** where they overlap (flag names like `-n`, `-o`, `-l`, `--all-namespaces`). Users shouldn't have to learn a new muscle memory.
- **Observe, then explain.** Like `istioctl proxy-config` or `kubectl describe`, diagnostic commands show the *actual* state of the running system as ground truth — never a re-derived prediction when the real data is available. Internal knowledge of operator algorithms (env merge precedence, template selection, derived naming) is used to *annotate* and *explain* the observed state (e.g. which layer an env var originated from), not to replace it. When a resource doesn't exist yet and only inferred state is available, the output must clearly indicate this (e.g. `(not yet deployed)`) so users never mistake a prediction for an observation.
- **Structured output where it adds value.** Commands with machine-consumable results (e.g. `analyze`, `validate`, `model add`) support `-o json|yaml`. Commands that produce composite human-readable output (e.g. `debug`) don't force it — structured subsections are available via the standalone building-block commands.
- **Logic separated from CLI wiring.** Every command is a callable Go function with typed I/O, not code embedded in Cobra handlers. This makes the same logic available to tests, SDK consumers, and a future MCP server without refactoring. See [Architecture guidance](#architecture-guidance) and [MCP readiness](#mcp-readiness).

## Command structure

```
aim debug <resource> <name> [flags]        # single-resource deep-dive (tree + analyze + env)
aim analyze [resource] [name] [flags]      # rule-based diagnostic scanner
aim tree <resource> <name> [flags]         # dependency graph
aim env <resource> <name> [flags]          # env var inspector
aim model add <image...> [flags]           # quick model onboarding
aim validate -f <file|dir|-> [flags]       # offline/online validation
aim wait <resource> <name...> [flags]      # poll until condition
aim test [subcommand] [flags]              # chainsaw wrapper
aim config [subcommand] [flags]            # user preferences
aim completion <shell>                     # shell completions
```

---

## Bulk operations

Where applicable, commands accept multiple targets following `kubectl` conventions:

| Pattern | Example |
|---------|---------|
| Multiple images | `aim model add rocm/vllm-openai:latest rocm/vllm-mistral:latest` |
| Stdin list | `crane ls rocm/vllm-openai \| aim model add --cluster-scoped -` |
| File/dir input | `aim validate -f dir/` |
| Multiple names | `aim wait service svc1 svc2 -n default --for=running` |

`aim model add` accepts multiple positional args with shared flags. Per-model customization uses `kubectl apply -f` with individual resource manifests.

---

## Tier 1 — Primary features

### 1. Resource deep-dive — `aim debug`

The primary command. Answers "why isn't this working?" by combining dependency tree, diagnostic rules, env var inspection, conditions, events, and pod status into a single human-readable output.

```bash
aim debug service my-svc -n default
aim debug model my-model
aim debug template my-tpl -n default
aim debug artifact my-art -n default
```

Example output for a service:

```
$ aim debug service my-svc -n default

━━ DEPENDENCY TREE ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

AIMService/my-svc (Starting)
├── AIMModel/vllm-openai (Ready) [cluster]
│   └── AIMClusterModelSource/registry-amd
├── AIMServiceTemplate/vllm-openai-mi300x-4gpu (Ready, optimized)
│   └── AIMTemplateCache/vllm-openai-mi300x-4gpu-1080a608 (Ready)
│       ├── AIMArtifact/weights-a3f2b1 (Ready, 140Gi)
│       └── AIMArtifact/config-b7c3d2 (Ready, 2Gi)
├── AIMRuntimeConfig/prod-config (Ready)
├── InferenceService/my-svc-1080a608 (Not Ready)
│   └── Pod/my-svc-1080a608-predictor-00001-abc (CrashLoopBackOff)
└── HTTPRoute/my-svc-1080a608

━━ DIAGNOSTICS ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

ERROR [AIM001] AIMService not Running: condition Ready=False, reason: PodFailure
ERROR [AIM003] Pod CrashLoopBackOff — last log: "RuntimeError: CUDA out of memory"

━━ CONDITIONS ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  Ready              False   PodFailure           InferenceService predictor pod failing
  DependenciesReachable True  Resolved             All dependencies resolved
  ConfigValid        True    Valid                Configuration is valid
  Model              Ready   ModelResolved        AIMClusterModel vllm-openai is ready
  Template           Ready   Resolved             AIMServiceTemplate vllm-openai-mi300x-4gpu is ready
  Cache              Ready   CacheReady           Template cache is ready

━━ EFFECTIVE ENV ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  AIM_CACHE_PATH=/aim/cache                  ← system default
  VLLM_ENABLE_METRICS=true                   ← system default
  VLLM_WORKER_MULTIPROC_METHOD=spawn         ← AIMRuntimeConfig/prod-config
  HIP_FORCE_DEV_KERNARG=1                    ← AIMServiceTemplate/vllm-tpl
  MAX_MODEL_LEN=4096                         ← AIMService/my-svc (overrides 8192)

━━ RECENT EVENTS ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

  2m ago  Warning  BackOff  Pod/my-svc-1080a608-predictor-00001-abc  Back-off restarting failed container
  5m ago  Normal   Created  Pod/my-svc-1080a608-predictor-00001-abc  Created container kserve-container
```

`debug` is composite — its sections correspond to standalone building-block commands (`aim tree`, `aim analyze`, `aim env`) that each support `-o json` individually. `debug` itself produces human-readable output only.

Resolves derived names (via `GenerateDerivedName` from `internal/utils/naming.go`) to trace the full resource graph, including hash-suffixed InferenceService and HTTPRoute names that aren't guessable.

### 2. Quick model onboarding — `aim model add`

Collapse AIMClusterModel creation into a single command with smart defaults.

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
- `--custom` — maps to `spec.custom` + `spec.customTemplates`. `--source-uri` populates `customTemplates[0].modelSources[0].sourceUri`, and `--gpu-count`/`--gpu-model` populate `customTemplates[0].hardware.gpu`. For multi-template custom models, use `kubectl apply -f` with a full manifest.

### 3. Validation — `aim validate`

Offline and online manifest validation. Useful standalone and as a CI gate: `aim validate -f deploy/aim/`.

```bash
aim validate -f service.yaml                    # offline checks
aim validate -f ./aim-resources/                # validate a directory
aim validate -f - < manifest.yaml               # stdin (piping from generators)
aim validate -f service.yaml --server-dry-run   # full admission check
aim validate -f service.yaml -o json            # structured output
```

Returns field-level errors, warnings, and the defaults that admission would apply.

---

## Tier 2 — Building-block commands

These are the standalone versions of `debug` subsections. Each supports `-o json` for scripting and MCP integration.

### 4. Diagnostic scanner — `aim analyze`

Rule-based diagnostic scanner. Unlike `debug` (single-resource deep-dive), `analyze` scans across namespaces/clusters and supports offline mode.

```bash
aim analyze                                  # full cluster
aim analyze -n my-namespace                  # namespace-scoped
aim analyze service my-svc -n default        # single resource
aim analyze -f resources.yaml --use-kube=false  # offline validation
aim analyze -o json                          # machine-parseable
```

**Diagnostic rules** (non-exhaustive, pluggable registry):

| Code | Severity | Offline | Check |
|------|----------|---------|-------|
| AIM001 | ERROR | | AIMService not Running — show condition causing failure |
| AIM002 | ERROR | | InferenceService missing for Running/Starting service |
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

Offline rules (AIM100+) run without cluster access via `--use-kube=false`. AIM100–AIM105 mirror CRD CEL/pattern validations. AIM106–AIM109 are cross-reference checks — WARN not ERROR because the referenced resource may already exist in the cluster.

Rules are a registry (`[]Analyzer` with `func Check(ctx, state) []Diagnostic`) — independently testable and extensible.

### 5. Dependency tree — `aim tree`

Standalone version of the debug tree section. Supports `-o json` for programmatic consumption.

```bash
aim tree service my-svc -n default
aim tree service my-svc -n default -o json
```

### 6. Env var inspector — `aim env`

Standalone version of the debug env section. Shows the four-layer merge with source attribution.

```bash
aim env service my-svc -n default
aim env service my-svc -n default -o json
```

Reads actual env vars from the InferenceService container (ground truth), then annotates each with its originating AIM resource layer by checking name-membership against the four precedence layers: system defaults (e.g. `AIM_CACHE_PATH`), RuntimeConfig env, Template env, and Service env (highest wins). When the InferenceService has not been created yet, the env section is omitted.

### 7. Wait / poll — `aim wait`

For CI/CD pipelines and scripting:

```bash
aim wait service my-svc -n default --for=running --timeout=10m
aim wait service svc1 svc2 svc3 -n default --for=running --timeout=10m
aim wait artifact -l aim.eai.amd.com/cache.name=my-cache -n default --for=ready --progress
```

Multiple names and label selectors supported — exits when all targets satisfy the condition (or first failure with `--fail-fast`). `--progress` shows artifact download percentage. Exit code reflects success/failure/timeout.

---

## Tier 3 — Power user features

### 8. Chainsaw wrapper — `aim test`

Speculative — scope and UX to be expanded once the core CLI (v0.1–v0.2) is stable.

```bash
aim test list [--filter pattern]
aim test run path/to/test/ [-n ns] [--timeout 10m]
aim test results <session-id>
```

### 9. Interactive TUI — `aim dashboard`

Optional `bubbletea`-based terminal UI showing live resource status. Think `k9s` but AIM-aware.

### 10. Config profiles — `aim config`

```bash
aim config set-context prod --kubeconfig ~/.kube/prod --namespace aim-prod
aim config use-context prod
aim config set defaults.gpu-model MI300X
```

Stores per-user defaults so `--gpu-model` and `--runtime-config` don't need repeating.

### 11. kubectl plugin compatibility

Name the binary `kubectl-aim` so it works as both `aim debug` and `kubectl aim debug`. Just a naming convention — no code changes required.

**Resource alias mapping** (used across all commands):

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

## Architecture guidance

### Separate logic from CLI wiring

Every command should be a callable function, not embedded in Cobra handlers. This keeps the logic testable, reusable, and callable from other contexts (MCP server, SDK, tests).

```go
// pkg/debug — composite logic, calls tree/analyze/env internally
func Run(ctx context.Context, conn ConnectionProvider, opts Options) (*DebugReport, error)

// pkg/analyze — rule engine, also called by debug
func Run(ctx context.Context, conn ConnectionProvider, opts Options) ([]Diagnostic, error)

// cmd/debug.go — thin Cobra wrapper
func newDebugCmd() *cobra.Command { /* parse flags → call debug.Run → render sections */ }
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

The connection should expose a server-side apply (SSA) capable client. The operator already uses SSA for all resource management — the same `client.Client` with `client.Apply` field manager can be reused. This keeps the door open for `aim apply -f` / `aim delete -f` if CRUD commands become needed later, since SSA-based apply/delete is trivial once the connection layer supports it.

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
| Logic in `pkg/`, not in Cobra handlers | MCP handler calls `debug.Run()`, `analyze.Run()` etc. directly — same functions, different transport |
| Typed `Options` + result structs | Auto-generate MCP tool JSON schemas from Go struct tags |
| `ConnectionProvider` interface | CLI mode: fresh connection per call. MCP mode: pooled connections. Business logic unchanged |
| `-o json` on building-block commands | JSON output schemas = MCP tool response schemas. Already tested and stable |
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
| Env var merge logic (operator) | `internal/aimservice/kserve.go` — `BuildMergedEnvVars()` |
| Env var display (CLI) | `internal/diagnose/service.go` — reads from InferenceService, annotates source layer |
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
| **v0.1** | `debug`, `model add`, `validate` | Core debugging and onboarding — the two most requested workflows |
| **v0.2** | `analyze`, `tree`, `env`, `wait` (standalone) | Building blocks for scripting, CI/CD, and MCP |
| **v0.3** | `test`, `config`, `dashboard` TUI, kubectl plugin | Power-user features |
| **future** | MCP server mode | When IDE agent adoption warrants it |
