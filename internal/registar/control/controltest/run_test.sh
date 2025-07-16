#!/bin/bash

set -e

if [ -z "$1" ]; then
    echo "usage: $0 <test_name> [test_name2 ...]"
    exit 1
fi

TEST_NAMES="$@"

go build -o peer ./peer.go
go test -c -o ./control.test ../

TEST_NAMES_REGEX=$(echo "$TEST_NAMES" | sed 's/ /|/g')
sudo ./control.test -test.v -test.timeout 30s -test.run ^"${TEST_NAMES_REGEX}"$

rm -f peer control.test