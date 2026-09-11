#!/usr/bin/env bash
# Print release artifact image references for compile-release output.
#
# Environment (set by the compile-release GHA job or manually):
#   VERSION              Git tag (e.g. v1.2.3)
#   CHART_VERSION        Helm chart version (e.g. 1.2.3)
#   CHART_OCI_REPO       OCI registry prefix (e.g. oci://registry-1.docker.io/silogenai)
#   PUSH_IMAGE_REPO      Registry where images are pushed (optional)
#   PULL_IMAGE_REPO      Registry used when images are not built in CI (optional)
#   BUILD_IMAGES         "true" when this run built and pushed images
#   INT_TEST_CHART_VERSION  Static chart version for integration tests (optional)
#   INT_TEST_IMAGE_TAG   SHA-suffixed operator image tag for int-test (optional)
#   CHART_NAME           Helm chart name (default: aim-engine-chart)
#   CRDS_CHART_NAME      CRDs chart name (default: aim-engine-crds-chart)
#
# Usage:
#   VERSION=v1.2.3 CHART_VERSION=1.2.3 \
#     CHART_OCI_REPO=oci://registry-1.docker.io/silogenai \
#     PUSH_IMAGE_REPO=docker.io/silogenai/aim-engine \
#     ./hack/release/print-artifacts.sh
set -euo pipefail

VERSION="${VERSION:?VERSION is required}"
CHART_VERSION="${CHART_VERSION:?CHART_VERSION is required}"
CHART_OCI_REPO="${CHART_OCI_REPO:?CHART_OCI_REPO is required}"
PUSH_IMAGE_REPO="${PUSH_IMAGE_REPO:-}"
PULL_IMAGE_REPO="${PULL_IMAGE_REPO:-}"
BUILD_IMAGES="${BUILD_IMAGES:-false}"
INT_TEST_CHART_VERSION="${INT_TEST_CHART_VERSION:-}"
INT_TEST_IMAGE_TAG="${INT_TEST_IMAGE_TAG:-}"
CHART_NAME="${CHART_NAME:-aim-engine-chart}"
CRDS_CHART_NAME="${CRDS_CHART_NAME:-aim-engine-crds-chart}"

# oci://registry-1.docker.io/silogenai -> docker.io/silogenai
image_repo="${CHART_OCI_REPO#oci://}"
image_repo="${image_repo/registry-1.docker.io/docker.io}"

operator_image_repo="${PUSH_IMAGE_REPO:-${PULL_IMAGE_REPO}}"
if [[ -z "${operator_image_repo}" ]]; then
	operator_image_repo="${image_repo}/aim-engine"
fi

print_artifacts() {
	echo "${image_repo}/${CHART_NAME}:${CHART_VERSION}"
	echo "${image_repo}/${CRDS_CHART_NAME}:${CHART_VERSION}"
	echo "${operator_image_repo}:${VERSION}"
	echo "${image_repo}/aim-artifact-downloader:${VERSION}"
}

{
	echo "## Release artifacts (${VERSION})"
	echo ""
	echo '```'
	print_artifacts
	echo '```'

	if [[ "${BUILD_IMAGES}" == "true" && -n "${INT_TEST_CHART_VERSION}" ]]; then
		echo ""
		echo "## Integration-test artifacts"
		echo ""
		echo '```'
		echo "${image_repo}/${CHART_NAME}:${INT_TEST_CHART_VERSION}"
		echo "${image_repo}/${CRDS_CHART_NAME}:${INT_TEST_CHART_VERSION}"
		if [[ -n "${INT_TEST_IMAGE_TAG}" ]]; then
			echo "${operator_image_repo}:${INT_TEST_IMAGE_TAG}"
		fi
		echo "${image_repo}/aim-artifact-downloader:${VERSION}"
		echo '```'
	fi
} | {
	if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
		tee -a "${GITHUB_STEP_SUMMARY}"
	else
		cat
	fi
}
