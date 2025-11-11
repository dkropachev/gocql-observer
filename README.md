# gocql-observer

A tiny observer that maintains two independent [gocql](https://github.com/gocql/gocql) sessions, emits heartbeats against each cluster, and stores every per-cluster log in both a dedicated file and stdout (prefixed with the cluster name). The process runs forever or for a configurable duration.

## Configuration
All settings are provided through environment variables. Anything not set falls back to sane defaults.

| Variable                  | Description                                                     | Default |
|---------------------------|-----------------------------------------------------------------| --- |
| `CLUSTERn_NAME`           | Friendly display name used in logs                              | `cluster1` / `cluster2` |
| `CLUSTERn_CONTACT_POINTS` | Comma/space separated list of contact points                    | `127.0.0.1` / `127.0.0.2` |
| `CLUSTERn_KEYSPACE`       | Keyspace used when opening each session                         | `system` |
| `CLUSTERn_USERNAME`       | Optional username per cluster                                   | empty |
| `CLUSTERn_PASSWORD`       | Optional password per cluster                                   | empty |
| `LOG_DIRECTORY`           | Directory used when `CLUSTERn_LOG_FILE` is not set              | `./logs` |
| `QUERY_INTERVAL`          | Go duration between heartbeat queries                           | `15s` |
| `RUN_DURATION`            | Total runtime before exiting; `0` keeps it running indefinitely | `0` |

Logs are appended to the relevant log file **and** mirrored to stdout with the cluster name prefix, e.g. `[west] 2024-01-01T12:00:00Z ...`.

## Run locally
```bash
go run .
# or override targets
CLUSTER1_CONTACT_POINTS="192.168.10.10" \
CLUSTER2_CONTACT_POINTS="192.168.20.10" \
LOG_DIRECTORY="/tmp/observer-logs" \
go run .
```

## Docker image
Build and run the container locally:
```bash
make build-image
docker run --rm -e CLUSTER1_CONTACT_POINTS=192.168.10.10 -e CLUSTER2_CONTACT_POINTS=192.168.20.10 \
  -e LOG_DIRECTORY=/observer/logs -v $(pwd)/logs:/observer/logs gocql-observer:latest
```
The Docker image exposes `/observer/logs` as a volume so log files survive container restarts.

## Kubernetes deployment
A sample manifest is provided in `k8s/deployment.yaml`. Update the `ConfigMap` with real contact points (and credentials if needed), push the built image to your registry, then deploy:
```bash
docker build -t ghcr.io/<org>/gocql-observer:latest .
docker push ghcr.io/<org>/gocql-observer:latest
kubectl apply -f k8s/deployment.yaml
```
The manifest mounts an `emptyDir` volume at `/var/log/gocql` so log files are available via `kubectl exec` or sidecar shipping.
