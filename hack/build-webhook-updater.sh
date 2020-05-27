#!/bin/bash

set -ex

export GO111MODULE=on
export GOPROXY=off
export GOFLAGS=-mod=vendor
cd cmd/webhook-updater && go build -v .
