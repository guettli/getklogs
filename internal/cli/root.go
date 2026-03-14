package cli

import (
	"fmt"
	"io"
	"os"

	"github.com/guettli/getklogs/internal/getklogs"
	"github.com/spf13/cobra"
)

func NewRootCmd(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	options := getklogs.Options{
		Since: getklogs.DefaultSince,
	}

	cmd := &cobra.Command{
		Use:   "getklogs [term]",
		Short: "Fetch workload and pod logs",
		Long: fmt.Sprintf(`Fetch logs for Kubernetes workloads and pods and sort them by Kubernetes timestamp.

Here, "workload" means a Deployment, DaemonSet, or StatefulSet.

If [term] is given, targets are matched case-insensitively via *term* across workload name, namespace, and kind.
Use --pod to match pods by name instead.
Use --all to process all matches.
By default, getklogs fetches logs from %s.
Use --since 0s to fetch all available logs.
Use --outdir to write files somewhere other than the current directory.

By default, getklogs writes the result to a timestamped file such as:
  capi-kubeadm-bootstrap-controller-manager--mgt-system-2026-03-14_13-09-25Z.log`, getklogs.DescribeSinceWindow(getklogs.DefaultSince)),
		Example: `  getklogs
  getklogs kubeadm-bootstrap
  getklogs --since 0s kubeadm-bootstrap
  getklogs -o raw --tail 50 -n kube-system coredns
  getklogs --pod apiserver
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

			cluster, err := getklogs.NewCluster()
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
	cmd.Flags().BoolVar(&options.All, "all", false, "Process all matching targets; without --pod, also include standalone pods")
	cmd.Flags().BoolVar(&options.Stdout, "stdout", false, "Write output to stdout instead of creating files")
	cmd.Flags().StringVar(&options.OutDir, "outdir", "", "Directory for output files (default: current directory)")
	cmd.Flags().BoolVar(&options.AddSource, "add-source", false, "Include pod and container source information in output")
	cmd.Flags().IntVar(&options.TailLines, "tail", 0, "Only include the last N combined log lines per target")
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
		Long: `Convert log lines from stdin into json, yaml, or raw output without talking to Kubernetes.

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
