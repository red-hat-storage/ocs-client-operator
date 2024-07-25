#!/bin/bash

export CGO_ENABLED=${CGO_ENABLED:-0}
export GOOS=${GOOS:-linux}
export GOARCH=${GOARCH:-amd64}
export GO111MODULE=${GO111MODULE:-on}

set -x

go build -a -o ${GOBIN:-bin}/manager cmd/main.go
go build -a -o ${GOBIN:-bin}/status-reporter ./service/status-report/main.go
