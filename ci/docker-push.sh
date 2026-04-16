#!/bin/bash

set -e

IMAGE="selenwright/selenwright"

docker login -u="$DOCKERHUB_USERNAME" -p="$DOCKERHUB_TOKEN"
docker buildx build --pull --push -t "$IMAGE" -t "$IMAGE:$1" -t "selenwright/hub:$1" --platform linux/amd64,linux/arm64 .
