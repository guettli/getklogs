# getklogs

`getklogs` fetches logs for all pods of a Deployment, DaemonSet, or StatefulSet and sorts them by the Kubernetes timestamp.

Repository: <https://github.com/guettli/getklogs>

## Install

```console
go install github.com/guettli/getklogs@latest
```

## Usage

The block below is generated from `getklogs --help` via `task readme`.

<!-- usage:start -->
```text
Fetch logs for the pods of a Deployment, DaemonSet, or StatefulSet and sort them by Kubernetes timestamp.

If [term] is given, workloads are matched case-insensitively via *term* across workload name, namespace, and kind.

By default, getklogs writes the result to a timestamped file such as:
  capi-kubeadm-bootstrap-controller-manager--mgt-system-2026-03-14_13-09-25Z.log

Usage:
  getklogs [term] [flags]

Examples:
  getklogs
  getklogs kubeadm-bootstrap
  getklogs -n kube-system coredns

Flags:
      --add-source         Include pod and container source information in output
  -h, --help               help for getklogs
  -n, --namespace string   Kubernetes namespace (optional; if omitted: all namespaces)
      --no-to-json         Keep original log lines instead of converting output to JSON lines
  -o, --output string      Output format: json or yaml (default "json")
      --since duration     Return logs newer than a relative duration like 5s, 2m, or 3h (default 3h0m0s)
```
<!-- usage:end -->
