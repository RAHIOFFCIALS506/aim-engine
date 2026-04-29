#!/usr/bin/env bash
set -euo pipefail

DEFAULT_REGISTRY="docker.io"
DEFAULT_NAMESPACE="amdenterpriseai"
DEFAULT_FILTER="aim-*"
DEFAULT_SECRET_NAME="ghcr-pull-secret"

usage() {
  cat <<USAGE
List container images from a registry namespace.

Discovers repositories and tags the same way AIMClusterModelSource does,
using the Docker Hub API or GitHub Packages API as appropriate.
Output is one image reference per line, suitable for piping to gen-cluster-models.sh.

By default only repos matching "${DEFAULT_FILTER}" are listed. Use --all to list everything.

Usage:
  $(basename "$0") [OPTIONS] [REGISTRY/NAMESPACE]
  $(basename "$0") -h

Options:
  -c, --config FILE          Path to a .dockerconfigjson file for authentication
  -f, --filter PATTERN       Filter repos and optionally tags (default: ${DEFAULT_FILTER})
                             PATTERN may be "repo-glob" or "repo-glob:tag-glob"
                             e.g. "aim-*" "aim-*:0.9" "aim-base:0.*"
  -a, --all                  Disable default filter, list all repos and tags
  -S, --create-secret [NAME]  Emit a Kubernetes docker-registry Secret YAML to stderr
                             (requires --config, default name: ${DEFAULT_SECRET_NAME})
  -n, --namespace NS         Kubernetes namespace for the secret (default: default)
  -h, --help                 Show this help message

Arguments:
  REGISTRY/NAMESPACE    Registry and namespace to list (default: ${DEFAULT_REGISTRY}/${DEFAULT_NAMESPACE})
                        A bare namespace (no slash) is treated as docker.io/NAMESPACE.
                        e.g. docker.io/amdenterpriseai  or  ghcr.io/silogen

Examples:
  $(basename "$0")
  $(basename "$0") --all amdenterpriseai
  $(basename "$0") -c dockerconfig.json ghcr.io/silogen
  $(basename "$0") -f "aim-*:0.9" docker.io/amdenterpriseai
  $(basename "$0") -c dockerconfig.json -f "aim-*" ghcr.io/silogen
  $(basename "$0") | gen-cluster-models.sh /dev/stdin
  $(basename "$0") -c creds.json -S ghcr-pull-secret ghcr.io/silogen \
    | gen-cluster-models.sh -s ghcr-pull-secret /dev/stdin

  # Create secret + models in one shot:
  $(basename "$0") -c creds.json -S ghcr-pull-secret ghcr.io/silogen > images.txt
  { cat secret.yaml; gen-cluster-models.sh -s ghcr-pull-secret images.txt; } | kubectl apply -f -
USAGE
}

config_file=""
filter="$DEFAULT_FILTER"
target=""
create_secret=""
secret_namespace="default"

while [[ $# -gt 0 ]]; do
  case "$1" in
    -c|--config)         config_file="$2"; shift 2 ;;
    -f|--filter)         filter="$2"; shift 2 ;;
    -a|--all)            filter=""; shift ;;
    -S|--create-secret)
      if [[ $# -ge 2 && "$2" != -* ]]; then
        create_secret="$2"; shift 2
      else
        create_secret="$DEFAULT_SECRET_NAME"; shift
      fi
      ;;
    -n|--namespace)      secret_namespace="$2"; shift 2 ;;
    -h|--help)           usage; exit 0 ;;
    -*)                  echo "Error: unknown option: $1" >&2; echo >&2; usage >&2; exit 1 ;;
    *)                   target="$1"; shift ;;
  esac
done

if [[ -z "$target" ]]; then
  target="${DEFAULT_REGISTRY}/${DEFAULT_NAMESPACE}"
fi

registry="${target%%/*}"
namespace="${target#*/}"

# Bare namespace (no slash) assumes docker.io
if [[ "$registry" == "$namespace" ]]; then
  namespace="$registry"
  registry="$DEFAULT_REGISTRY"
fi

# ---- Create Secret YAML ----

if [[ -n "$create_secret" ]]; then
  if [[ -z "$config_file" ]]; then
    echo "Error: --create-secret requires --config" >&2
    exit 1
  fi
  if [[ ! -f "$config_file" ]]; then
    echo "Error: config file not found: $config_file" >&2
    exit 1
  fi
  encoded=$(base64 -w0 < "$config_file")
  cat >&2 <<EOF
---
apiVersion: v1
kind: Secret
metadata:
  name: ${create_secret}
  namespace: ${secret_namespace}
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: ${encoded}
EOF
  echo "# Secret YAML for '${create_secret}' written to stderr" >&2
fi

# ---- Split filter into repo and tag parts ----

repo_filter=""
tag_filter=""
if [[ -n "$filter" ]]; then
  if [[ "$filter" == *:* ]]; then
    repo_filter="${filter%%:*}"
    tag_filter="${filter#*:}"
  else
    repo_filter="$filter"
  fi
fi

matches_glob() {
  local pattern="$1" string="$2"
  [[ "$string" == $pattern ]]
}

# ---- Docker Hub ----

list_dockerhub_repos() {
  local ns="$1"
  local url="https://hub.docker.com/v2/namespaces/${ns}/repositories?page_size=100"
  while [[ -n "$url" && "$url" != "null" ]]; do
    local resp
    resp=$(curl -sf "$url")
    echo "$resp" | jq -r '.results[].name'
    url=$(echo "$resp" | jq -r '.next // empty')
  done
}

list_dockerhub_tags() {
  local repo="$1"
  local url="https://hub.docker.com/v2/repositories/${repo}/tags?page_size=100"
  while [[ -n "$url" && "$url" != "null" ]]; do
    local resp
    resp=$(curl -sf "$url") || break
    echo "$resp" | jq -r '.results[].name'
    url=$(echo "$resp" | jq -r '.next // empty')
  done
}

# ---- GHCR ----

extract_ghcr_token() {
  if [[ -z "$config_file" ]]; then
    echo "Error: GHCR requires --config with ghcr.io credentials" >&2
    exit 1
  fi
  local token
  token=$(jq -r '
    .auths["ghcr.io"] //
    .auths["https://ghcr.io"] //
    empty |
    if .password and .password != "" then .password
    elif .auth and .auth != "" then (.auth | @base64d | split(":") | .[1])
    else empty end
  ' < "$config_file" 2>/dev/null)
  if [[ -z "$token" ]]; then
    echo "Error: no ghcr.io credentials found in $config_file" >&2
    exit 1
  fi
  echo "$token"
}

list_ghcr_packages() {
  local org="$1" token="$2"
  local page=1
  while true; do
    local resp count
    resp=$(curl -sf \
      -H "Accept: application/vnd.github+json" \
      -H "Authorization: Bearer ${token}" \
      -H "X-GitHub-Api-Version: 2022-11-28" \
      "https://api.github.com/orgs/${org}/packages?package_type=container&per_page=100&page=${page}")
    count=$(echo "$resp" | jq 'length')
    echo "$resp" | jq -r '.[].name'
    [[ "$count" -lt 100 ]] && break
    ((page++))
  done
}

list_ghcr_tags() {
  local repo="$1" token="$2"
  local url="https://ghcr.io/v2/${repo}/tags/list"
  local resp
  local challenge
  challenge=$(curl -si "$url" 2>/dev/null | grep -i www-authenticate || true)
  if [[ "$challenge" =~ realm=\"([^\"]+)\" ]]; then
    local realm="${BASH_REMATCH[1]}"
    local scope service
    [[ "$challenge" =~ scope=\"([^\"]+)\" ]] && scope="${BASH_REMATCH[1]}"
    [[ "$challenge" =~ service=\"([^\"]+)\" ]] && service="${BASH_REMATCH[1]}"
    local bearer
    bearer=$(curl -sf -u "USERNAME:${token}" "${realm}?scope=${scope}&service=${service}" | jq -r '.token')
    resp=$(curl -sf -H "Authorization: Bearer ${bearer}" "$url")
  else
    resp=$(curl -sf -H "Authorization: Bearer ${token}" "$url")
  fi
  echo "$resp" | jq -r '.tags[]?' 2>/dev/null
}

# ---- Parallel tag fetching ----

MAX_PARALLEL=10
_tmpdir=""

setup_tmpdir() {
  _tmpdir=$(mktemp -d)
  trap 'rm -rf "$_tmpdir"' EXIT
}

safe_name() {
  echo "${1//\//__}"
}

limit_jobs() {
  while (( $(jobs -r | wc -l) >= MAX_PARALLEL )); do
    sleep 0.1
  done
}

# ---- Main ----

echo "# Listing images from ${registry}/${namespace} (filter: ${filter:-none}) ..." >&2

count=0
case "$registry" in
  docker.io)
    matching_repos=()
    for repo in $(list_dockerhub_repos "$namespace"); do
      if [[ -n "$repo_filter" ]] && ! matches_glob "$repo_filter" "$repo"; then
        continue
      fi
      matching_repos+=("$repo")
    done

    if [[ ${#matching_repos[@]} -gt 0 ]]; then
      echo "# Fetching tags for ${#matching_repos[@]} repos ..." >&2
      setup_tmpdir

      for repo in "${matching_repos[@]}"; do
        (
          n=0
          for tag in $(list_dockerhub_tags "${namespace}/${repo}"); do
            if [[ -n "$tag_filter" ]] && ! matches_glob "$tag_filter" "$tag"; then
              continue
            fi
            echo "${namespace}/${repo}:${tag}"
            n=$((n + 1))
          done
          echo "$n" > "${_tmpdir}/$(safe_name "$repo").count"
        ) &
        limit_jobs
      done
      wait

      for repo in "${matching_repos[@]}"; do
        cfile="${_tmpdir}/$(safe_name "$repo").count"
        if [[ -f "$cfile" ]]; then
          count=$((count + $(cat "$cfile")))
        fi
      done
    fi
    ;;

  ghcr.io)
    token=$(extract_ghcr_token)
    matching_pkgs=()
    for pkg in $(list_ghcr_packages "$namespace" "$token"); do
      if [[ -n "$repo_filter" ]] && ! matches_glob "$repo_filter" "$pkg"; then
        continue
      fi
      matching_pkgs+=("$pkg")
    done

    if [[ ${#matching_pkgs[@]} -gt 0 ]]; then
      echo "# Fetching tags for ${#matching_pkgs[@]} packages ..." >&2
      setup_tmpdir

      for pkg in "${matching_pkgs[@]}"; do
        (
          n=0
          for tag in $(list_ghcr_tags "${namespace}/${pkg}" "$token"); do
            if [[ -n "$tag_filter" ]] && ! matches_glob "$tag_filter" "$tag"; then
              continue
            fi
            echo "ghcr.io/${namespace}/${pkg}:${tag}"
            n=$((n + 1))
          done
          echo "$n" > "${_tmpdir}/$(safe_name "$pkg").count"
        ) &
        limit_jobs
      done
      wait

      for pkg in "${matching_pkgs[@]}"; do
        cfile="${_tmpdir}/$(safe_name "$pkg").count"
        if [[ -f "$cfile" ]]; then
          count=$((count + $(cat "$cfile")))
        fi
      done
    fi
    ;;

  *)
    echo "Error: unsupported registry '$registry'. Supported: docker.io, ghcr.io" >&2
    exit 1
    ;;
esac

echo "# Found $count images" >&2