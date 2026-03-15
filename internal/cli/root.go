package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/guettli/getklogs/internal/getklogs"
	"github.com/spf13/cobra"
)

var newCluster = func(kubeconfig string) (getklogs.ClusterAPI, error) {
	return getklogs.NewCluster(kubeconfig)
}

func NewRootCmd(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	options := getklogs.Options{
		Since: getklogs.DefaultSince,
	}

	cmd := &cobra.Command{
		Use:   "getklogs [term]",
		Short: "Fetch workload and pod logs",
		Long: fmt.Sprintf(`Fetch logs for Kubernetes workloads and pods and sort them by Kubernetes timestamp.

Here, "workload" means a Deployment, DaemonSet, or StatefulSet.

If [term] is given, targets are matched case-insensitively via *term* across workload name,
namespace, and kind. If term does not match exactly one workload, an interactive selection
appears so you can choose a workload.

By default, getklogs fetches logs from %s.

Use --kubeconfig to set an explicit kubeconfig path.
By default, getklogs uses the KUBECONFIG environment variable when it is set.

Use --node to only include pods scheduled on nodes matching the given glob, for example *node*.
Use --meta to include metadata such as source_pod and source_container in the output.
Use --per-container to create one output file per container instead of joining all lines for the target.

By default, getklogs writes the result to a timestamped file such as:
  deployment-name--namespace-YYYY-MM-DD_HH-MM-SSZ.log

By default, all log lines for the selected target are joined and sorted by time.
`, getklogs.DescribeSinceWindow(getklogs.DefaultSince)),
		Example: `  getklogs
  getklogs kubeadm-bootstrap
  getklogs --since 0s kubeadm-bootstrap
  getklogs -o raw --tail 50 -n kube-system coredns
  getklogs --pod apiserver
  getklogs --node '*worker*' --all
  getklogs --meta frontend
  getklogs --per-container frontend
  getklogs --all
  getklogs --outdir /tmp/getklogs --all
  getklogs --stdout --tail 50 -n kube-system coredns
  cat foo.log | getklogs tojson`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				options.TermQuery = args[0]
			}
			if err := getklogs.ValidateOptions(options); err != nil {
				return err
			}

			cluster, err := newCluster(options.Kubeconfig)
			if err != nil {
				return err
			}

			app := getklogs.App{
				Cluster: cluster,
				Stdin:   stdin,
				Stdout:  stdout,
				Stderr:  stderr,
			}

			return app.Run(cmd.Context(), options)
		},
	}
	cmd.AddCommand(newToJSONCmd(stdin, stdout))

	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	cmd.Flags().StringVarP(&options.Namespace, "namespace", "n", "", "Kubernetes namespace (optional; if omitted: all namespaces)")
	cmd.Flags().DurationVar(&options.Since, "since", options.Since, "Return logs newer than a relative duration like 5s, 2m, or 3h. Use 0s for all available logs")
	cmd.Flags().BoolVar(&options.Pod, "pod", false, "Match pods by name instead of workloads")
	cmd.Flags().BoolVar(&options.All, "all", false, "Process all matching targets. No interactive workload selection.")
	cmd.Flags().BoolVar(&options.Stdout, "stdout", false, "Write output to stdout instead of creating files")
	cmd.Flags().StringVar(&options.OutDir, "outdir", "", "Output directory (default: current directory)")
	cmd.Flags().StringVar(&options.Kubeconfig, "kubeconfig", "", "Path to kubeconfig file (default: use KUBECONFIG when set)")
	cmd.Flags().StringVar(&options.Node, "node", "", "Only include pods on nodes matching this glob pattern, for example *node*")
	cmd.Flags().BoolVar(&options.Meta, "meta", false, "Include metadata such as source_pod and source_container in the output")
	cmd.Flags().BoolVar(&options.PerContainer, "per-container", false, "Create one output file per container instead of one combined file per target")
	cmd.Flags().IntVar(&options.TailLines, "tail", 0, "Only include the last N combined log lines per target; with --per-container, per container")
	cmd.Flags().StringVarP(&options.Output, "output", "o", getklogs.OutputFormatJSON, "Output format: json, yaml, or raw")

	return cmd
}

func newToJSONCmd(stdin io.Reader, stdout io.Writer) *cobra.Command {
	options := getklogs.Options{
		Output: getklogs.OutputFormatJSON,
	}

	cmd := &cobra.Command{
		Use:     "tojson",
		Aliases: []string{"to-json"},
		Short:   "Convert stdin log lines into json, yaml, or raw output",
		Long: `Convert stdin log lines into json, yaml, or raw output.

Lines that start with an RFC3339 timestamp keep that value as kubernetes_timestamp.
All other lines are parsed as raw log messages.`,
		Example: `  cat foo.log | getklogs tojson
  kubectl logs deploy/coredns -n kube-system --timestamps | getklogs tojson
  cat foo.log | getklogs tojson --output yaml
  cat foo.log | getklogs tojson --output raw`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := getklogs.ValidateOptions(options); err != nil {
				return err
			}
			return getklogs.ConvertInput(stdin, stdout, options)
		},
	}

	cmd.Flags().StringVarP(&options.Output, "output", "o", options.Output, "Output format: json, yaml, or raw")

	return cmd
}

func Execute() {
	if err := NewRootCmd(os.Stdin, os.Stdout, os.Stderr).Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
