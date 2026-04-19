#!/bin/bash

set -e

IMAGE="selenwright/hub"

printf '%s' "$DOCKERHUB_TOKEN" | docker login --username "$DOCKERHUB_USERNAME" --password-stdin
docker buildx build --pull --push -t "$IMAGE" -t "$IMAGE:$1" --platform linux/amd64,linux/arm64 .
