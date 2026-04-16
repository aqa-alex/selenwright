#!/bin/bash

set -e

docker login -u="$DOCKERHUB_USERNAME" -p="$DOCKERHUB_TOKEN"
docker buildx build --pull --push -t "$GITHUB_REPOSITORY" -t "$GITHUB_REPOSITORY:$1" -t "selenwright/hub:$1" --platform linux/amd64,linux/arm64 .
