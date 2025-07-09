#!/bin/bash
set -eu -o pipefail

mkdir -p third_party/nri/pkg/api
curl -fsLS -o third_party/nri/pkg/api/api.proto https://raw.githubusercontent.com/containerd/nri/refs/tags/v0.9.0/pkg/api/api.proto
curl -fsLS -o third_party/nri/pkg/api/api.pb.go https://raw.githubusercontent.com/containerd/nri/refs/tags/v0.9.0/pkg/api/api.pb.go
curl -fsLS -o third_party/nri/pkg/api/api_ttrpc.pb.go https://raw.githubusercontent.com/containerd/nri/refs/tags/v0.9.0/pkg/api/api_ttrpc.pb.go
curl -fsLS -o third_party/nri/pkg/api/event.go https://raw.githubusercontent.com/containerd/nri/refs/tags/v0.9.0/pkg/api/event.go
