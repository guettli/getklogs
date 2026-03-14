# getklogs

`getklogs` fetches logs for all pods of a Deployment, DaemonSet, or StatefulSet and sorts them by the Kubernetes timestamp.

Repository: <https://github.com/guettli/getklogs>

## Install

```console
go install github.com/guettli/getklogs/cmd/getklogs@latest
```

## Usage

The block below is generated from `getklogs --help` via `task readme`.

<!-- usage:start -->
```text
Fetch logs for the pods of a Deployment, DaemonSet, or StatefulSet and sort them by Kubernetes timestamp.

Usage:
  getklogs [term] [flags]

Examples:
  getklogs
  getklogs -n kube-system coredns
  getklogs cert-manager.log

Flags:
  -h, --help               help for getklogs
  -n, --namespace string   Kubernetes namespace (optional; if omitted: all namespaces)
      --since duration     Return logs newer than a relative duration like 5s, 2m, or 3h (default 3h0m0s)
```
<!-- usage:end -->
