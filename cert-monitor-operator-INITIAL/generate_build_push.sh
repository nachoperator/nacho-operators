#!/bin/bash

# --- Configuration ---
# Define the tag for this version
VERSION_TAG="v0.1.18" 

# Define the image repository and name
IMAGE_REPO="eu.gcr.io/mm-k8s-dev-01/operator/cert-monitor-operator"

# Build the full image name with the tag
IMG="${IMAGE_REPO}:${VERSION_TAG}"

# Define the target platform
TARGET_PLATFORM="linux/amd64"
TARGET_GOOS="linux"
TARGET_GOARCH="amd64"

# Define output binary path
BINARY_PATH="bin/manager"
# Define main package path
MAIN_PACKAGE_PATH="cmd/main.go" 

# --- CRD and Chart Paths ---
CRD_SOURCE_DIR="config/crd/bases"
CRD_FILENAME="certmonitor.masorange.com_certificatemonitors.yaml" 
HELM_CHART_CRDS_DIR="charts/cert-monitor-operator/crds" 

# --- Steps ---

# Exit immediately if a command fails
set -e 

echo "--- Running Generation and Manifests ---"
# These generate/update CRDs, RBAC, etc., including the source CRD file
make generate
make manifests

# --- Copy Updated CRD to Helm Chart ---
echo "--- Copiando CRD actualizado a la carpeta del Chart Helm ---"
# Create the Helm chart's crds directory if it doesn't exist
mkdir -p "${HELM_CHART_CRDS_DIR}"
# Copy the generated CRD file to the Helm chart's crds directory
cp "${CRD_SOURCE_DIR}/${CRD_FILENAME}" "${HELM_CHART_CRDS_DIR}/"
echo "CRD copiado a ${HELM_CHART_CRDS_DIR}"
# --- End CRD Copy ---

echo "--- Building Go Binary directly for ${TARGET_PLATFORM} ---"
# Execute go build directly, bypassing 'make build'
# Ensure the output path (bin/) exists if needed
mkdir -p "$(dirname "${BINARY_PATH}")" 
GOARCH=${TARGET_GOARCH} GOOS=${TARGET_GOOS} go build -o "${BINARY_PATH}" "${MAIN_PACKAGE_PATH}"
echo "--- Go build finished ---"

# --- Debugging: Check if binary exists ---
echo "--- Checking for compiled binary at ${BINARY_PATH} ---"
ls -l "${BINARY_PATH}"
if [ $? -ne 0 ]; then
  echo "*** ERROR: Compiled binary not found at ${BINARY_PATH} after go build! ***"
  exit 1
fi
# --- End Debugging ---

echo "--- Building and Pushing Docker Image for ${TARGET_PLATFORM}: ${IMG} ---"
docker buildx build --platform "${TARGET_PLATFORM}" --push -t "${IMG}" -f Dockerfile .
echo "--- Docker Image Push completed for ${IMG} ---"
echo "--- Process ended for ${IMG} ---"


