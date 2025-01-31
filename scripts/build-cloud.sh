#!/bin/bash
set -ex

VERSION=$1
if [ -z "$VERSION" ]; then
  VERSION="dev"
fi

# Clean up any existing containers/images
docker rm cloud-builder-tmp-${VERSION} 2>/dev/null || true
docker rmi serviceradar-cloud-build:${VERSION} 2>/dev/null || true

# Build the Docker image
DOCKER_DEFAULT_PLATFORM=linux/amd64 docker build -f Dockerfile.cloud \
  --build-arg VERSION="${VERSION}" \
  -t serviceradar-cloud-build:${VERSION} .

# Extract binary
docker create --name cloud-builder-tmp-${VERSION} serviceradar-cloud-build:${VERSION}
mkdir -p dist/cloud_linux_amd64_v1
docker cp cloud-builder-tmp-${VERSION}:/src/serviceradar-cloud dist/cloud_linux_amd64_v1/
docker rm cloud-builder-tmp-${VERSION}

# Verify binary exists and make executable
if [ ! -f dist/cloud_linux_amd64_v1/serviceradar-cloud ]; then
  echo "Binary not found after build"
  exit 1
fi

chmod +x dist/cloud_linux_amd64_v1/serviceradar-cloud