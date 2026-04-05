#!/bin/bash

set -e

export GO111MODULE="on"
go test -tags 's3 metadata' -v -race -coverprofile=coverage.txt -covermode=atomic -coverpkg github.com/aqa-alex/selenwright,github.com/aqa-alex/selenwright/session,github.com/aqa-alex/selenwright/config,github.com/aqa-alex/selenwright/protect,github.com/aqa-alex/selenwright/service,github.com/aqa-alex/selenwright/upload,github.com/aqa-alex/selenwright/info,github.com/aqa-alex/selenwright/jsonerror

go install golang.org/x/vuln/cmd/govulncheck@latest
"$(go env GOPATH)"/bin/govulncheck -tags production ./...
