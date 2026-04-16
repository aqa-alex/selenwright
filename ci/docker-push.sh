#!/bin/bash

set -e

IMAGE="selenwright/hub"

docker login -u="$DOCKERHUB_USERNAME" -p="$DOCKERHUB_TOKEN"
docker buildx build --pull --push -t "$IMAGE" -t "$IMAGE:$1" --platform linux/amd64,linux/arm64 .
