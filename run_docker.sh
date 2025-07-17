#!/bin/bash

set -e

if [ -z "$1" ]; then
    echo "usage: $0 <test_name> [test_name2 ...]"
    exit 1
fi

TEST_NAMES="$*"
# replace spaces with |
TEST_NAMES_REGEX="${TEST_NAMES// /|}"

docker build -t stun:0.0.1 -f Dockerfile .
docker run -it --rm \
    --volume "$(pwd)":"$(pwd)" \
    --workdir "$(pwd)" \
    --cap-add=NET_ADMIN \
    --privileged \
    --volume /run/systemd/system:/run/systemd/system \
    --volume /bin/systemctl:/bin/systemctl \
    --volume /var/run/dbus/system_bus_socket:/var/run/dbus/system_bus_socket \
    --volume "$(go env GOCACHE):/root/.cache/go-build" \
    --volume "$(go env GOMODCACHE):/go/pkg/mod" \
    stun:0.0.1 \
    go test -count=1 -v -run ^"${TEST_NAMES_REGEX}"$ ./...