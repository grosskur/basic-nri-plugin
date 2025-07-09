# basic-nri-plugin

## Background

[NRI][NRI] provides a plugin interface to make controlled changes or perform
actions at specific points in the container lifecycle. NRI plugins are
long-running servers that connect to the NRI socket (`/var/run/nri/nri.sock`)
and interact via [ttrpc][ttrpc].

Typically, when developing an NRI plugin your code needs to import:

* github.com/containerd/nri/pkg/api: Provides the necessary protobuf types and
  ttrpc service definitions.

* github.com/containerd/nri/pkg/stub: Provides a framework for managing the
  lifecycle of your plugin and relevent hooks.

However, using these packages brings in additional transitive Go module
dependencies, and also means you have less control over certain aspects.

## Motivation

`basic-nri-plugin` is a demo to show how you can write an NRI plugin with
fewer dependencies and more control over the lifecycle. It uses the lower-level
github.com/containerd/nri/pkg/net package and vendors only the necessary API
definitions under the `third_party` directory.

It is not meant to be a reusable framework or library. Rather, it is a starting
point that can be forked and customized.

## Building

```
go build ./cmd/basic-nri-plugin
```

## Updating generated protobufs

```
./scripts/update-third-party.sh
```

[nri]: https://github.com/containerd/nri "NRI"
[ttrpc]: https://github.com/containerd/ttrpc "ttrpc"
