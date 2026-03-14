package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/guettli/getklogs/internal/getklogs"
	"github.com/spf13/cobra"
)

func NewRootCmd(stdin io.Reader, stdout, stderr io.Writer) *cobra.Command {
	options := getklogs.Options{
		Since:  getklogs.DefaultSince,
		Output: getklogs.OutputFormatJSON,
	}

	cmd := &cobra.Command{
		Use:   "getklogs [term]",
		Short: "Fetch logs for all pods of a workload",
		Long: `Fetch logs for the pods of a Deployment, DaemonSet, or StatefulSet and sort them by Kubernetes timestamp.

If [term] is given, workloads are matched case-insensitively via *term* across workload name, namespace, and kind.

By default, getklogs writes the result to a timestamped file such as:
  capi-kubeadm-bootstrap-controller-manager--mgt-system-2026-03-14_13-09-25Z.log`,
		Example: `  getklogs
  getklogs kubeadm-bootstrap
  getklogs -n kube-system coredns`,
		Args:          cobra.MaximumNArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				options.TermQuery = args[0]
			}
			options = getklogs.NormalizeOptions(options)

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

			return app.Run(context.Background(), options)
		},
	}

	cmd.SetOut(stdout)
	cmd.SetErr(stderr)

	cmd.Flags().StringVarP(&options.Namespace, "namespace", "n", "", "Kubernetes namespace (optional; if omitted: all namespaces)")
	cmd.Flags().DurationVar(&options.Since, "since", options.Since, "Return logs newer than a relative duration like 5s, 2m, or 3h")
	cmd.Flags().BoolVar(&options.AddSource, "add-source", false, "Include pod and container source information in output")
	cmd.Flags().BoolVar(&options.NoToJSON, "no-to-json", false, "Keep original log lines instead of converting output to JSON lines")
	cmd.Flags().StringVarP(&options.Output, "output", "o", options.Output, "Output format: json or yaml")

	return cmd
}

func Execute() {
	if err := NewRootCmd(os.Stdin, os.Stdout, os.Stderr).Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
