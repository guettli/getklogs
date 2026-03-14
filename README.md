# getklogs

`getklogs` fetches workload and pod logs and sorts them by Kubernetes timestamp.

Repository: <https://github.com/guettli/getklogs>

## Install

```console
go install github.com/guettli/getklogs@latest
```

## Usage

The block below is generated from `getklogs --help` via `task readme`.

<!-- usage:start -->
```text
Fetch logs for Kubernetes workloads and pods and sort them by Kubernetes timestamp.

Here, "workload" means a Deployment, DaemonSet, or StatefulSet.

If [term] is given, targets are matched case-insensitively via *term* across workload name, namespace, and kind.
Use --pod to match pods by name instead.
Use --all to process all matches.

By default, getklogs writes the result to a timestamped file such as:
  capi-kubeadm-bootstrap-controller-manager--mgt-system-2026-03-14_13-09-25Z.log

Usage:
  getklogs [term] [flags]

Examples:
  getklogs
  getklogs kubeadm-bootstrap
  getklogs --pod apiserver
  getklogs --all
  getklogs --stdout --tail 50 -n kube-system coredns

Flags:
      --add-source         Include pod and container source information in output
      --all                Process all matching targets; without --pod, also include standalone pods
  -h, --help               help for getklogs
  -n, --namespace string   Kubernetes namespace (optional; if omitted: all namespaces)
      --no-to-json         Keep original log lines instead of converting output to JSON lines
  -o, --output string      Output format: json or yaml (default "json")
      --pod                Match pods by name instead of workloads
      --since duration     Return logs newer than a relative duration like 5s, 2m, or 3h (default 3h0m0s)
      --stdout             Write output to stdout instead of creating files
      --tail int           Only include the last N combined log lines per target
```
<!-- usage:end -->
