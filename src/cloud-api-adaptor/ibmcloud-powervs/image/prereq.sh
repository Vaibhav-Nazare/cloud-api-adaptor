#!/bin/bash

# FIXME to pickup these values from versions.yaml
GO_VERSION="1.22.12"

# Install dependencies
yum install -y curl libseccomp-devel openssl openssl-devel skopeo clang clang-devel

wget https://rpmfind.net/linux/centos-stream/9-stream/CRB/ppc64le/os/Packages/protobuf-compiler-3.14.0-13.el9.ppc64le.rpm
yum install -y protobuf-compiler-3.14.0-13.el9.ppc64le.rpm

wget https://www.rpmfind.net/linux/centos-stream/9-stream/CRB/ppc64le/os/Packages/device-mapper-devel-1.02.202-6.el9.ppc64le.rpm
yum install -y device-mapper-devel-1.02.202-6.el9.ppc64le.rpm

# Install Golang
curl https://dl.google.com/go/go${GO_VERSION}.linux-ppc64le.tar.gz -o go${GO_VERSION}.linux-ppc64le.tar.gz && \
rm -rf /usr/local/go && tar -C /usr/local -xzf go${GO_VERSION}.linux-ppc64le.tar.gz && \
rm -f go${GO_VERSION}.linux-ppc64le.tar.gz
echo -e '\n# golang environment variables' >> /etc/profile
echo 'export GOROOT=/usr/local/go' >> /etc/profile
echo 'export GOPATH=$HOME/go' >> /etc/profile
echo 'export PATH=$PATH:$GOROOT/bin:$GOPATH/bin' >> /etc/profile
source /etc/profile
