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
By default, getklogs fetches logs from last 3h.
Use --since 0s to fetch all available logs.
Use --outdir to write files somewhere other than the current directory.

By default, getklogs writes the result to a timestamped file such as:
  capi-kubeadm-bootstrap-controller-manager--mgt-system-2026-03-14_13-09-25Z.log

Usage:
  getklogs [term] [flags]
  getklogs [command]

Examples:
  getklogs
  getklogs kubeadm-bootstrap
  getklogs --since 0s kubeadm-bootstrap
  getklogs -o raw --tail 50 -n kube-system coredns
  getklogs --pod apiserver
  getklogs --all
  getklogs --outdir /tmp/getklogs --all
  getklogs --stdout --tail 50 -n kube-system coredns
  cat foo.log | getklogs tojson

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  tojson      Convert stdin log lines into json, yaml, or raw output

Flags:
      --add-source         Include pod and container source information in output
      --all                Process all matching targets; without --pod, also include standalone pods
  -h, --help               help for getklogs
  -n, --namespace string   Kubernetes namespace (optional; if omitted: all namespaces)
      --outdir string      Directory for output files (default: current directory)
  -o, --output string      Output format: json, yaml, or raw (default "json")
      --pod                Match pods by name instead of workloads
      --since duration     Return logs newer than a relative duration like 5s, 2m, or 3h. Use 0s for all available logs (default 3h0m0s)
      --stdout             Write output to stdout instead of creating files
      --tail int           Only include the last N combined log lines per target

Use "getklogs [command] --help" for more information about a command.
```
<!-- usage:end -->
