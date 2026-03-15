# getklogs

`getklogs` fetches workload and pod logs and sorts them by Kubernetes timestamp.

Repository: <https://github.com/guettli/getklogs>

## Run

You can run directly:

```console
go run github.com/guettli/getklogs@latest my-deployment
```

## Install

```console
go install github.com/guettli/getklogs@latest
```

## Usage

<!-- usage:start -->
```text
Fetch logs for Kubernetes workloads and pods and sort them by Kubernetes timestamp.

Here, "workload" means a Deployment, DaemonSet, or StatefulSet.

If [term] is given, targets are matched case-insensitively via *term* across workload name,
namespace, and kind. If term does not match exactly one workload, an interactive selection
appears so you can choose a workload.

By default, getklogs fetches logs from last 3h.

Use --kubeconfig to set an explicit kubeconfig path.
By default, getklogs uses the KUBECONFIG environment variable when it is set.

Use --node to only include pods scheduled on nodes matching the given glob, for example *node*.

By default, getklogs writes the result to a timestamped file such as:
  deployment-name--namespace-YYYY-MM-DD_HH-MM-SSZ.log

Usage:
  getklogs [term] [flags]
  getklogs [command]

Examples:
  getklogs
  getklogs kubeadm-bootstrap
  getklogs --since 0s kubeadm-bootstrap
  getklogs -o raw --tail 50 -n kube-system coredns
  getklogs --pod apiserver
  getklogs --node '*worker*' --all
  getklogs --all
  getklogs --outdir /tmp/getklogs --all
  getklogs --stdout --tail 50 -n kube-system coredns
  cat foo.log | getklogs tojson

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  tojson      Convert stdin log lines into json, yaml, or raw output

Flags:
      --add-source          Include pod and container source information in output
      --all                 Process all matching targets; without --pod, also include standalone pods
  -h, --help                help for getklogs
      --kubeconfig string   Path to kubeconfig file (default: use KUBECONFIG when set)
  -n, --namespace string    Kubernetes namespace (optional; if omitted: all namespaces)
      --node string         Only include pods on nodes matching this glob pattern, for example *node*
      --outdir string       Output directory (default: current directory)
  -o, --output string       Output format: json, yaml, or raw (default "json")
      --pod                 Match pods by name instead of workloads
      --since duration      Return logs newer than a relative duration like 5s, 2m, or 3h. Use 0s for all available logs (default 3h0m0s)
      --stdout              Write output to stdout instead of creating files
      --tail int            Only include the last N combined log lines per target

Use "getklogs [command] --help" for more information about a command.
```
<!-- usage:end -->

## Feedback

Feedback is welcome. Just open and issue and tell me what you think about `getklogs`.

## Related

* [guettli/check-conditions: Check Conditions of all Kubernets Resources](https://github.com/guettli/check-conditions/)
* [guettli/dumpall: Dump all Kubernetes resources into a directory structure](https://github.com/guettli/dumpall)
* [guettli/wol: Working out Loud](https://github.com/guettli/wol)
