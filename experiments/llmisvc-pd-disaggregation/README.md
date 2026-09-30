# P/D disaggregation with a KServe LLMInferenceService

> Temporary experiment folder for sharing findings. Remove before merging.

A minimal prefill/decode (P/D) disaggregated deployment of `Qwen/Qwen3-8B` on
AMD MI300X using KServe v0.20: one prefill pod and one decode pod, one GPU
each. vLLM's `NixlConnector` moves the KV cache from the prefill pod to the
decode pod. It is the KServe counterpart of llm-d's
`guides/pd-disaggregation/modelserver/amd/vllm` guide.

Tested on two MI300X nodes without RDMA, with prefill and decode on different
nodes, so the KV cache moved over TCP.

## What gets deployed

Setting `spec.prefill` makes KServe layer its built-in P/D presets under the
spec:

| Object | Preset |
| --- | --- |
| Prefill Deployment `qwen3-8b-pd-kserve-prefill`, vLLM on 8000 | `kserve-config-llm-prefill-template` |
| Decode Deployment `qwen3-8b-pd-kserve`, vLLM on 8001 behind the llm-d routing sidecar on 8000 | `kserve-config-llm-decode-template` |
| llm-d scheduler (EPP) with an always-disaggregate config, and the InferencePool | `kserve-config-llm-scheduler` |
| HTTPRoute to the InferencePool | `kserve-config-llm-router-route` |

`llminferenceservice.yaml` only adds what the presets don't cover. Its
comments explain each setting.

## Deploy and send a request

```bash
NS=llmisvc-examples
kubectl create namespace $NS
kubectl apply -n $NS -f llminferenceservice.yaml
kubectl -n $NS wait pod -l kserve.io/component=workload --for=condition=Ready --timeout=15m
```

The test cluster's gateway can't reach the scheduler (see
[Limitations](#limitations)), so send requests to the decode pod's sidecar and
set the prefill header yourself; the scheduler normally sets it:

```bash
PIP=$(kubectl -n $NS get pod -l llm-d.ai/role=prefill -o jsonpath='{.items[0].status.podIP}')
kubectl -n $NS port-forward deploy/qwen3-8b-pd-kserve 18000:8000 &
curl -s localhost:18000/v1/chat/completions -H 'Content-Type: application/json' \
  -H "x-prefiller-host-port: $PIP:8000" \
  -d '{"model":"Qwen/Qwen3-8B","max_tokens":100,
       "chat_template_kwargs":{"enable_thinking":false},
       "messages":[{"role":"user","content":"Explain P/D disaggregation in two sentences."}]}'
```

Without the header, the request runs on decode only. To confirm a request was
split, check that the prefill pod logged it and decode counted a KV transfer.
vLLM prints its stats about every 10 s:

```bash
kubectl -n $NS logs deploy/qwen3-8b-pd-kserve-prefill -c main | grep 'POST /v1'
kubectl -n $NS logs deploy/qwen3-8b-pd-kserve -c main | grep -E 'External prefix|KV Transfer'
```

## Findings

Each setting below was removed, redeployed and tested with disaggregated
requests. Settings that aren't needed over TCP are commented out in the
manifest.

| Setting | Result |
| --- | --- |
| `VLLM_HTTP_TIMEOUT_KEEP_ALIVE=120` on prefill | Required. vLLM closes idle connections after 5 s, but the routing sidecar reuses them for up to 90 s. Without it, 18 of 150 requests sent at about 5 s of idle failed with HTTP 502. |
| Storage initializer memory limit 8Gi | Required. At KServe's 1Gi default, every start was OOMKilled; the downloader peaks at 3-4 GiB. The 4 CPU limit isn't needed. |
| `VLLM_NIXL_SIDE_CHANNEL_HOST` on decode | Not needed with the default (pull) connector, where decode only connects out: 20 of 20 requests worked without it. Needed with push mode. |
| `IPC_LOCK` | Not needed over TCP, where nothing locks memory: 24 of 24 requests worked without it, also with a 64 KiB memlock limit. Needed for RDMA. |
| `nixl` container port 5600 | Not needed; nothing references it. 11 of 11 requests worked without it. |
| CPU 8 / memory 64Gi | Memory was about 4.5 times too large: the peak was 14.4 GiB, at startup, mostly page cache. CPU fits: 2 cores at idle, up to 6.4 on decode with 32k-token prompts. The manifest uses 16Gi requests and 32Gi limits. |

Failures aren't silent. If the prefill call fails, the sidecar (v0.9.0)
returns the error to the client without retrying. If decode can't load the KV
cache, vLLM fails the request.

## Limitations

- **Gateway traffic is not disaggregated.** The test cluster's Envoy Gateway
  rejects HTTPRoute backends of kind InferencePool, so the scheduler is never
  consulted and the LLMInferenceService stays `Ready=False` with reason
  `HTTPRoutesNotReady`. A gateway that supports InferencePool, such as Envoy
  AI Gateway, fixes it.
- **The KV cache moves over TCP**, at about 350 MB/s. Qwen3-8B uses about
  144 KiB of KV cache per token, so a 1k-token prompt moves about 150 MB. A
  load test topped out at about 2.3 requests per second with 1k-token
  prompts, limited by transfers rather than GPUs.
- **The decode side has the same 5 s keep-alive race.** The sidecar's
  connection to its local vLLM failed 3 of 227 requests sent near 5 s of
  idle, with HTTP 502.
- **KV leases can expire during transfer backlogs.** Prefill holds a
  request's KV blocks for decode for 30 s. In a burst of 256 requests, it
  released 234 of them before decode had read them. Outputs looked complete,
  but weren't checked for correctness.
- **`min_tokens` doesn't work.** The sidecar's prefill call sets `max_tokens`
  to 1 but keeps `min_tokens`, so vLLM rejects the request with HTTP 400.
- **Only a minimal setup was tested:** 1 prefill and 1 decode pod, TP=1, an 8B
  model. The generated scheduler config always disaggregates, even short
  prompts where it only adds latency.

## Next steps

1. Set `VLLM_HTTP_TIMEOUT_KEEP_ALIVE=120` on decode too, and confirm the
   decode-side 502s go away.
2. Find out whether expired KV leases corrupt output. Compare the outputs of
   a 256-request burst at temperature 0 with decode-only outputs, find out why
   decode's lease renewals didn't reach prefill, and check whether a longer
   `kv_lease_duration` avoids it.
3. Get a gateway that accepts InferencePool backends, then check that
   gateway traffic is disaggregated.
4. Move to a cluster with RDMA (below).

## Getting to RDMA performance

Untested. Roughly in order:

1. **Give both roles RDMA devices**, through the device plugin's resource
   (for example `rdma/ib`) or with NICs attached by Multus. Uncomment
   `IPC_LOCK` in the manifest.
2. **Check that UCX uses the NIC.** KServe's `KSERVE_INFER_ROCE` helper only
   handles NVIDIA `mlx5` devices, so configure UCX in the `main` containers
   directly:
   - Set `UCX_IB_GID_INDEX` if UCX picks the wrong RoCE v2 GID.
   - Set `UCX_NET_DEVICES` if a pod sees more than one NIC. Names can differ
     per node.
   - Set `UCX_PROTO_INFO=y` once and check that transfers use a verbs
     transport such as `rc_verbs`, not `tcp`.
3. **Enable GPU-direct RDMA**, with ROCm peer-memory or dma-buf support on the
   nodes. Otherwise UCX stages through host memory.
4. **Set up the network:** lossless or congestion-controlled RoCE (PFC/ECN)
   between nodes, and with TP>1, one NIC per GPU.
5. **Optionally, switch to MoRI-IO**, AMD's RDMA-native connector used by
   llm-d's reference. It needs a vLLM build with `MoRIIOConnector`, a routing
   sidecar build with the `--moriio-*` flags, and the same TP on both roles.
   It has no TCP fallback.
6. **Scale:**
   - Use more prefill than decode replicas for long-prompt workloads.
   - Set TP per role with each role's `--tensor-parallel-size` arg and
     `amd.com/gpu`. The prefill preset reads `spec.parallelism.tensor`, which
     is the decode value.
   - Scale CPU and memory per GPU.
   - Replace the always-disaggregate decider through
     `router.scheduler.config`.
