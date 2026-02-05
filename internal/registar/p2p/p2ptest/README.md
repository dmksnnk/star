# P2P Tests

Uses [gont](https://codeberg.org/cunicu/gont) to simulate network topologies (switches, NAT) via Linux network namespaces.

## Running locally

```bash
./run_test_in_docker.sh [TestName]
```

Examples:
```bash
./run_test_in_docker.sh                      # all tests
./run_test_in_docker.sh TestConnectorNAT     # single test
./run_test_in_docker.sh TestConnectorNAT TestConnectorSingleSwitch  # multiple tests
```

## Why systemd volumes?

Gont manages Linux network namespaces and communicates with the host's systemd through D-Bus. The container must access:

- `/run/systemd/system` — systemd runtime units
- `/bin/systemctl` — systemctl binary
- `/var/run/dbus/system_bus_socket` — D-Bus socket for systemd communication

Without these, gont cannot create or manage network namespaces.

## Cleanup after failed tests

If a test fails or is interrupted, network namespaces may be left behind. To clean up, install gontc:

```bash
go install cunicu.li/gont/v2/cmd/gontc@latest
```

Then run:

```bash
sudo $(which gontc) clean
```

Sudo is required because network namespaces are kernel resources that need root privileges to delete.
