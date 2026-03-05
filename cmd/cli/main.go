// MIT License
//
// Copyright (c) 2025 Advanced Micro Devices, Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	// Import all Kubernetes client auth plugins (e.g. Azure, GCP, OIDC, etc.)
	_ "k8s.io/client-go/plugin/pkg/client/auth"

	kservev1alpha1 "github.com/kserve/kserve/pkg/apis/serving/v1alpha1"
	kservev1beta1 "github.com/kserve/kserve/pkg/apis/serving/v1beta1"
	gatewayapiv1 "sigs.k8s.io/gateway-api/apis/v1"

	aimv1alpha1 "github.com/amd-enterprise-ai/aim-engine/api/v1alpha1"
	"github.com/amd-enterprise-ai/aim-engine/internal/diagnose"
)

const outputJSON = "json"

var (
	scheme  = runtime.NewScheme()
	version = "dev"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(aimv1alpha1.AddToScheme(scheme))
	utilruntime.Must(gatewayapiv1.Install(scheme))
	utilruntime.Must(kservev1alpha1.AddToScheme(scheme))
	utilruntime.Must(kservev1beta1.AddToScheme(scheme))
}

// connFlags holds the shared Kubernetes connection flags inherited by all subcommands.
type connFlags struct {
	kubeconfig    string
	kubecontext   string
	namespace     string
	allNamespaces bool
}

func (f *connFlags) addFlags(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&f.kubeconfig, "kubeconfig", "", "Path to kubeconfig file (defaults to $KUBECONFIG or ~/.kube/config)")
	cmd.PersistentFlags().StringVar(&f.kubecontext, "context", "", "Kubernetes context to use")
	cmd.PersistentFlags().StringVarP(&f.namespace, "namespace", "n", "", "Kubernetes namespace (defaults to current context namespace)")
	cmd.PersistentFlags().BoolVarP(&f.allNamespaces, "all-namespaces", "A", false, "List resources across all namespaces")
}

func (f *connFlags) client() (client.Client, string, error) {
	return newClient(f.kubeconfig, f.kubecontext, f.namespace)
}

func (f *connFlags) validateAllNamespaces(args []string) error {
	if f.allNamespaces && len(args) > 0 {
		return fmt.Errorf("a resource cannot be retrieved by name across all namespaces")
	}
	return nil
}

// listNamespace returns "" (all namespaces) when -A is set, otherwise the resolved namespace.
func (f *connFlags) listNamespace(resolved string) string {
	if f.allNamespaces {
		return ""
	}
	return resolved
}

func main() {
	root := &cobra.Command{
		Use:          "aim",
		Short:        "AIM Engine CLI",
		SilenceUsage: true,
	}
	root.AddCommand(newDebugCmd())
	root.AddCommand(newTreeCmd())
	root.AddCommand(newAnalyzeCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newCompletionCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// debug
// ---------------------------------------------------------------------------

func newDebugCmd() *cobra.Command {
	cf := &connFlags{}
	cmd := &cobra.Command{
		Use:   "debug",
		Short: "Deep-dive diagnostic view of AIM resources",
		Long:  "Fetches an AIM resource and its full dependency graph, then renders a composite diagnostic report including dependency tree, conditions, effective env vars, and recent events.",
	}
	cf.addFlags(cmd)
	cmd.AddCommand(newDebugServiceCmd(cf))
	return cmd
}

func newDebugServiceCmd(cf *connFlags) *cobra.Command {
	return newServiceSubCmd(cf,
		"Debug an AIMService",
		"Without a name, lists AIMServices in the namespace. With a name, shows the full diagnostic report.",
		`  aim debug service
  aim debug service my-llm
  aim debug service my-llm -o json`,
		func(ctx context.Context, c client.Client, opts diagnose.Options, output string) error {
			report, err := diagnose.Run(ctx, c, opts)
			if err != nil {
				return err
			}
			switch output {
			case outputJSON:
				return diagnose.RenderJSON(os.Stdout, report)
			case "":
				return diagnose.Render(os.Stdout, report)
			default:
				return fmtUnsupportedOutput(output)
			}
		},
	)
}

// ---------------------------------------------------------------------------
// tree
// ---------------------------------------------------------------------------

func newTreeCmd() *cobra.Command {
	cf := &connFlags{}
	cmd := &cobra.Command{
		Use:   "tree",
		Short: "Show dependency tree for AIM resources",
		Long:  "Displays the dependency graph of an AIM resource as a tree. Supports -o json for programmatic consumption.",
	}
	cf.addFlags(cmd)
	cmd.AddCommand(newTreeServiceCmd(cf))
	return cmd
}

func newTreeServiceCmd(cf *connFlags) *cobra.Command {
	return newServiceSubCmd(cf,
		"Show dependency tree for an AIMService",
		"Without a name, lists AIMServices in the namespace. With a name, shows the dependency tree.",
		`  aim tree service
  aim tree service my-llm
  aim tree service my-llm -o json`,
		func(ctx context.Context, c client.Client, opts diagnose.Options, output string) error {
			tree, err := diagnose.Tree(ctx, c, opts)
			if err != nil {
				return err
			}
			switch output {
			case outputJSON:
				return diagnose.RenderJSON(os.Stdout, tree)
			case "":
				return diagnose.RenderTreeText(os.Stdout, tree)
			default:
				return fmtUnsupportedOutput(output)
			}
		},
	)
}

// ---------------------------------------------------------------------------
// analyze
// ---------------------------------------------------------------------------

func newAnalyzeCmd() *cobra.Command {
	cf := &connFlags{}
	var output string
	cmd := &cobra.Command{
		Use:   "analyze",
		Short: "Run diagnostic rules against AIM resources",
		Long: `Scans AIM resources for common issues and reports diagnostics with severity and codes.

Without a subcommand, scans all AIMServices in the current namespace (or all
namespaces with -A) and prints a summary of findings — similar to istioctl analyze.`,
		Example: `  aim analyze
  aim analyze -A
  aim analyze -A -o json
  aim analyze service my-llm`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, ns, err := cf.client()
			if err != nil {
				return err
			}
			listNS := cf.listNamespace(ns)
			result, err := diagnose.AnalyzeAll(cmd.Context(), c, listNS)
			if err != nil {
				return err
			}

			scope := fmt.Sprintf("namespace: %s", ns)
			if cf.allNamespaces {
				scope = "across all namespaces"
			}

			switch output {
			case outputJSON:
				if err := diagnose.RenderJSON(os.Stdout, result); err != nil {
					return err
				}
			case "":
				if err := diagnose.RenderScanResult(os.Stdout, result, scope); err != nil {
					return err
				}
			default:
				return fmtUnsupportedOutput(output)
			}

			if diagnose.HasErrors(result.Diagnostics) {
				cmd.SilenceErrors = true
				return fmt.Errorf("found %d error(s)", diagnose.CountErrors(result.Diagnostics))
			}
			return nil
		},
	}
	cf.addFlags(cmd)
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")
	cmd.AddCommand(newAnalyzeServiceCmd(cf))
	return cmd
}

func newAnalyzeServiceCmd(cf *connFlags) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "service [name]",
		Short: "Analyze an AIMService for issues",
		Long:  "Without a name, lists AIMServices in the namespace. With a name, runs diagnostic rules and reports findings.",
		Example: `  aim analyze service
  aim analyze service my-llm
  aim analyze service my-llm -o json`,
		ValidArgsFunction: completeServiceNames(cf),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cf.validateAllNamespaces(args); err != nil {
				return err
			}
			c, ns, err := cf.client()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return listServices(cmd.Context(), c, cf.listNamespace(ns), cf.allNamespaces)
			}
			diagnostics, err := diagnose.Analyze(cmd.Context(), c, diagnose.Options{
				ResourceType: "service",
				Name:         args[0],
				Namespace:    ns,
			})
			if err != nil {
				return err
			}
			switch output {
			case outputJSON:
				if err := diagnose.RenderJSON(os.Stdout, diagnostics); err != nil {
					return err
				}
			case "":
				if err := diagnose.RenderDiagnostics(os.Stdout, diagnostics); err != nil {
					return err
				}
			default:
				return fmtUnsupportedOutput(output)
			}
			if diagnose.HasErrors(diagnostics) {
				cmd.SilenceErrors = true
				return fmt.Errorf("found %d error(s)", diagnose.CountErrors(diagnostics))
			}
			return nil
		},
	}
	cmd.Args = cobra.MaximumNArgs(1)
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")
	return cmd
}

// ---------------------------------------------------------------------------
// version
// ---------------------------------------------------------------------------

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Args:  cobra.NoArgs,
		Run: func(_ *cobra.Command, _ []string) {
			fmt.Println(version)
		},
	}
}

// ---------------------------------------------------------------------------
// completion
// ---------------------------------------------------------------------------

func newCompletionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "completion [bash|zsh|fish|powershell]",
		Short: "Generate shell completion scripts",
		Long: `Generate shell completion scripts for the aim CLI.

To load completions:

  bash:
    source <(aim completion bash)

  zsh:
    source <(aim completion zsh)
    # Or, to load completions for each session:
    aim completion zsh > "${fpath[1]}/_aim"

  fish:
    aim completion fish | source

  powershell:
    aim completion powershell | Out-String | Invoke-Expression`,
		Args:      cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return cmd.Root().GenBashCompletionV2(os.Stdout, true)
			case "zsh":
				return cmd.Root().GenZshCompletion(os.Stdout)
			case "fish":
				return cmd.Root().GenFishCompletion(os.Stdout, true)
			case "powershell":
				return cmd.Root().GenPowerShellCompletionWithDesc(os.Stdout)
			default:
				return fmt.Errorf("unsupported shell %q", args[0])
			}
		},
	}
	return cmd
}

// ---------------------------------------------------------------------------
// shared service subcommand builder
// ---------------------------------------------------------------------------

type serviceActionFunc func(ctx context.Context, c client.Client, opts diagnose.Options, output string) error

func newServiceSubCmd(cf *connFlags, short, long, example string, action serviceActionFunc) *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:               "service [name]",
		Short:             short,
		Long:              long,
		Example:           example,
		ValidArgsFunction: completeServiceNames(cf),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cf.validateAllNamespaces(args); err != nil {
				return err
			}
			c, ns, err := cf.client()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				return listServices(cmd.Context(), c, cf.listNamespace(ns), cf.allNamespaces)
			}
			return action(cmd.Context(), c, diagnose.Options{
				ResourceType: "service",
				Name:         args[0],
				Namespace:    ns,
			}, output)
		},
	}
	cmd.Args = cobra.MaximumNArgs(1)
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output format (json)")
	return cmd
}

func fmtUnsupportedOutput(format string) error {
	return fmt.Errorf("unsupported output format %q (supported: json)", format)
}

// ---------------------------------------------------------------------------
// service listing & completion
// ---------------------------------------------------------------------------

func listServices(ctx context.Context, c client.Client, namespace string, showNamespace bool) error {
	var list aimv1alpha1.AIMServiceList
	if err := c.List(ctx, &list, client.InNamespace(namespace)); err != nil {
		return fmt.Errorf("list AIMServices: %w", err)
	}
	if len(list.Items) == 0 {
		if showNamespace {
			fmt.Fprintln(os.Stderr, "No AIMServices found")
		} else {
			fmt.Fprintf(os.Stderr, "No AIMServices found in namespace %q\n", namespace)
		}
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if showNamespace {
		_, _ = fmt.Fprintf(tw, "NAMESPACE\tNAME\tSTATUS\tAGE\n")
		for i := range list.Items {
			svc := &list.Items[i]
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", svc.Namespace, svc.Name, svc.Status.Status, formatAge(svc.CreationTimestamp.Time))
		}
	} else {
		_, _ = fmt.Fprintf(tw, "NAME\tSTATUS\tAGE\n")
		for i := range list.Items {
			svc := &list.Items[i]
			_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\n", svc.Name, svc.Status.Status, formatAge(svc.CreationTimestamp.Time))
		}
	}
	return tw.Flush()
}

func formatAge(t time.Time) string {
	if t.IsZero() {
		return "<unknown>"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func completeServiceNames(cf *connFlags) func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
	return func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
		if len(args) != 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		c, ns, err := cf.client()
		if err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		var list aimv1alpha1.AIMServiceList
		if err := c.List(context.Background(), &list, client.InNamespace(ns)); err != nil {
			return nil, cobra.ShellCompDirectiveError
		}
		names := make([]string, len(list.Items))
		for i := range list.Items {
			names[i] = list.Items[i].Name
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
}

// ---------------------------------------------------------------------------
// Kubernetes client
// ---------------------------------------------------------------------------

func newClient(kubeconfig, kubecontext, namespace string) (client.Client, string, error) {
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		loadingRules.ExplicitPath = kubeconfig
	}

	configOverrides := &clientcmd.ConfigOverrides{}
	if kubecontext != "" {
		configOverrides.CurrentContext = kubecontext
	}

	clientConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	if namespace == "" {
		var err error
		namespace, _, err = clientConfig.Namespace()
		if err != nil {
			return nil, "", fmt.Errorf("determine namespace: %w", err)
		}
	}

	restConfig, err := clientConfig.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("build rest config: %w", err)
	}

	c, err := client.New(restConfig, client.Options{Scheme: scheme})
	if err != nil {
		return nil, "", fmt.Errorf("create client: %w", err)
	}

	return c, namespace, nil
}
