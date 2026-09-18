package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/akundu/odh-pr-deploy/internal/app"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	stateDir := defaultStateDir()
	command := args[0]
	switch command {
	case "inspect":
		fs := flag.NewFlagSet(command, flag.ContinueOnError)
		contextName, namespace, component := commonFlags(fs, &stateDir)
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return inspect(*contextName, *namespace, *component)
	case "deploy":
		fs := flag.NewFlagSet(command, flag.ContinueOnError)
		contextName, namespace, component := commonFlags(fs, &stateDir)
		image := fs.String("image", "", "immutable image reference")
		pr := fs.Int("pr", 0, "GitHub PR number")
		mode := fs.String("mode", "shadow", "shadow or managed")
		allowManaged := fs.Bool("allow-managed-update", false, "acknowledge mutation of the managed Dashboard component")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		tool := app.New(app.OSRunner{}, stateDir)
		session, err := tool.Deploy(context.Background(), app.DeployOptions{Context: *contextName, Namespace: *namespace, Component: *component, Image: *image, PR: *pr, Mode: *mode, AllowManaged: *allowManaged})
		if err != nil {
			return err
		}
		return printJSON(session)
	case "status":
		fs := flag.NewFlagSet(command, flag.ContinueOnError)
		fs.StringVar(&stateDir, "state-dir", stateDir, "session state directory")
		id := fs.String("session", "", "session ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("--session is required")
		}
		session, err := app.New(app.OSRunner{}, stateDir).Load(*id)
		if err != nil {
			return err
		}
		return printJSON(session)
	case "cleanup":
		fs := flag.NewFlagSet(command, flag.ContinueOnError)
		fs.StringVar(&stateDir, "state-dir", stateDir, "session state directory")
		id := fs.String("session", "", "session ID")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("--session is required")
		}
		if err := app.New(app.OSRunner{}, stateDir).Cleanup(context.Background(), *id); err != nil {
			return err
		}
		fmt.Printf("session %s restored successfully\n", *id)
		return nil
	case "recover":
		fs := flag.NewFlagSet(command, flag.ContinueOnError)
		fs.StringVar(&stateDir, "state-dir", stateDir, "session state directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		sessions, err := app.New(app.OSRunner{}, stateDir).Sessions()
		if err != nil {
			return err
		}
		for _, session := range sessions {
			if !session.Completed {
				fmt.Println(session.ID)
			}
		}
		return nil
	default:
		return usage()
	}
}

func commonFlags(fs *flag.FlagSet, stateDir *string) (*string, *string, *string) {
	contextName := fs.String("context", "", "required Kubernetes context")
	namespace := fs.String("namespace", "redhat-ods-applications", "target namespace")
	component := fs.String("component", "gen-ai", "Dashboard component")
	fs.StringVar(stateDir, "state-dir", *stateDir, "session state directory")
	return contextName, namespace, component
}

func inspect(contextName, namespace, component string) error {
	if contextName == "" {
		return fmt.Errorf("--context is required")
	}
	if component != "gen-ai" {
		return fmt.Errorf("unsupported component %q (supported: gen-ai)", component)
	}
	// Use direct read-only invocations; inspection never persists a session.
	runner := app.OSRunner{}
	version, err := runner.Run(context.Background(), "oc", "--context", contextName, "get", "clusterversion", "version", "-o", "json")
	if err != nil {
		return err
	}
	deployment, err := runner.Run(context.Background(), "oc", "--context", contextName, "-n", namespace, "get", "deployment", componentDeployment(component), "-o", "json")
	if err != nil {
		return err
	}
	return printJSON(map[string]json.RawMessage{"clusterVersion": version, "deployment": deployment})
}

func componentDeployment(component string) string {
	if component == "gen-ai" {
		return "gen-ai-ui"
	}
	return component
}

func defaultStateDir() string {
	if configured := os.Getenv("ODH_PR_DEPLOY_STATE_DIR"); configured != "" {
		return configured
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".odh-pr-deploy"
	}
	return filepath.Join(home, ".local", "state", "odh-pr-deploy")
}

func printJSON(value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func usage() error {
	return fmt.Errorf("usage: odh-pr-deploy <inspect|deploy|status|cleanup|recover> [flags]")
}
