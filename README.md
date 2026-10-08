# aim-engine

This branch contains the auto-generated Helm chart and CRDs for aim-engine.

## Installation

CRDs are distributed separately and must be installed first:

```bash
# 1. Install CRDs
kubectl apply -f https://raw.githubusercontent.com/amd-enterprise-ai/aim-engine/artifacts/crd/crds.yaml
kubectl wait --for=condition=Established crd --all --timeout=60s

# 2a. Install operator via Helm OCI (recommended)
helm install aim-engine oci://registry-1.docker.io/amdenterpriseai/aim-engine-chart \
  --version 0.2.7 \
  --namespace aim-system --create-namespace

# 2b. Or install from this branch
helm install aim-engine ./chart --namespace aim-system --create-namespace
```

Gateway activation is disabled by default. The base install does not
select a Gateway implementation or install a gateway extension.

### Optional Envoy Gateway scale-from-zero

Install Envoy Gateway and its Gateway-targeted policy as platform
infrastructure. Edit the policy target, apply it, then deliberately
enable the matching Helm-managed collector:

```bash
curl -LO https://raw.githubusercontent.com/amd-enterprise-ai/aim-engine/artifacts/prereqs/scale-from-zero/envoy-gateway-route-metrics.yaml
kubectl apply -f envoy-gateway-route-metrics.yaml
helm upgrade aim-engine oci://registry-1.docker.io/amdenterpriseai/aim-engine-chart \
  --version 0.2.7 \
  --namespace aim-system \
  --set scaleFromZero.gatewayProvider=envoyGateway
```

See the [Envoy scale-from-zero setup guide](prereqs/scale-from-zero/README.md)
for target customization, verification, and security guidance.

Or clone this branch:

```bash
git clone -b artifacts https://github.com/amd-enterprise-ai/aim-engine.git aim-engine
cd aim-engine

kubectl apply -f crd/crds.yaml
kubectl wait --for=condition=Established crd --all --timeout=60s
helm install aim-engine ./chart --namespace aim-system --create-namespace
```

## Source

Generated from tag: v0.2.7 (commit: 3f585b07c3d7fa215d6ceed20190a809e82dbe11)
