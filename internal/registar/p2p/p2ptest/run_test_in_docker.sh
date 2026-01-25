#!/bin/bash

set -e

SCRIPT_PATH="$(readlink -f "$0")"
SCRIPT_DIR="$(dirname "$SCRIPT_PATH")"

# Build test run arguments
RUN_ARG=""
if [ -n "$1" ]; then
    TEST_NAMES="$*"
    # replace spaces with |
    TEST_NAMES_REGEX="${TEST_NAMES// /|}"
    RUN_ARG="-run ^${TEST_NAMES_REGEX}$"
fi

docker build -t stun:0.0.1 -f "$SCRIPT_DIR/Dockerfile" "$SCRIPT_DIR"
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
    go test -count=1 -v $RUN_ARG github.com/dmksnnk/star/internal/registar/p2p
