#!/bin/bash

# FIXME to pickup these values from versions.yaml
GO_VERSION="1.22.12"

# Install dependencies
yum install -y curl libseccomp-devel openssl openssl-devel skopeo clang clang-devel protobuf-compiler device-mapper-devel

# Install Golang
curl https://dl.google.com/go/go${GO_VERSION}.linux-ppc64le.tar.gz -o go${GO_VERSION}.linux-ppc64le.tar.gz && \
rm -rf /usr/local/go && tar -C /usr/local -xzf go${GO_VERSION}.linux-ppc64le.tar.gz && \
rm -f go${GO_VERSION}.linux-ppc64le.tar.gz
