#!/usr/bin/env bash

set -e
TAGNAME=$1
GH_REF=github.com/aqa-alex/selenwright.git
git config user.name "${GITHUB_REPOSITORY}"
git config user.email "github-actions[bot]@users.noreply.github.com"
git remote add upstream "https://${GITHUB_TOKEN}@${GH_REF}"
git fetch upstream

git branch -r

echo "Deleting old output"
rm -rf ${GITHUB_WORKSPACE}/docs/output
mkdir ${GITHUB_WORKSPACE}/docs/output
git worktree prune
rm -rf ${GITHUB_WORKSPACE}/.git/worktrees/docs/output/

echo "Checking out gh-pages branch into docs/output"
if git rev-parse --verify upstream/gh-pages >/dev/null 2>&1; then
  git worktree add -B gh-pages ${GITHUB_WORKSPACE}/docs/output upstream/gh-pages
else
  echo "gh-pages branch does not exist yet — creating orphan"
  git worktree add --detach ${GITHUB_WORKSPACE}/docs/output
  cd ${GITHUB_WORKSPACE}/docs/output
  git checkout --orphan gh-pages
  git rm -rf . >/dev/null 2>&1 || true
  cd ${GITHUB_WORKSPACE}
fi

echo "Removing existing files"
mkdir -p ${GITHUB_WORKSPACE}/docs/output/${TAGNAME}
rm -rf ${GITHUB_WORKSPACE}/docs/output/${TAGNAME}/*

if [ -d "${GITHUB_WORKSPACE}/docs/img" ]; then
  echo "Copying images"
  cp -R ${GITHUB_WORKSPACE}/docs/img ${GITHUB_WORKSPACE}/docs/output/${TAGNAME}/img
fi
echo "Generating docs"
docker run -v ${GITHUB_WORKSPACE}/docs/:/documents/ --name asciidoc-to-html asciidoctor/docker-asciidoctor asciidoctor -a revnumber=${TAGNAME} -D /documents/output/${TAGNAME} index.adoc


echo "Updating gh-pages branch"
cd ${GITHUB_WORKSPACE}/docs/output && git add --all && git commit -m "Publishing to gh-pages"


git push upstream HEAD:gh-pages
