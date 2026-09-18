package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/akundu/odh-pr-deploy/internal/app"
	"os"
	"path/filepath"
)

func main() {
	if e := run(os.Args[1:]); e != nil {
		fmt.Fprintln(os.Stderr, "error:", e)
		os.Exit(1)
	}
}
func run(a []string) error {
	if len(a) == 0 {
		return usage()
	}
	d := stateDir()
	t := app.New(app.OSRunner{}, d)
	switch a[0] {
	case "inspect":
		f := flag.NewFlagSet("inspect", flag.ContinueOnError)
		ctx := f.String("context", "", "Kubernetes context")
		ns := f.String("namespace", "", "Dashboard applications namespace; auto-discovered by default")
		if e := f.Parse(a[1:]); e != nil {
			return e
		}
		s, e := t.Inspect(context.Background(), *ctx, *ns)
		if e != nil {
			return e
		}
		return output(s)
	case "deploy":
		f := flag.NewFlagSet("deploy", flag.ContinueOnError)
		ctx := f.String("context", "", "Kubernetes context")
		ns := f.String("namespace", "", "Dashboard applications namespace; auto-discovered by default")
		image := f.String("image", "", "GenAI image override; requires --dashboard-image")
		dashboardImage := f.String("dashboard-image", "", "Dashboard image override; requires --image")
		pr := f.Int("pr", 0, "odh-dashboard PR number")
		if e := f.Parse(a[1:]); e != nil {
			return e
		}
		s, e := t.Deploy(context.Background(), app.DeployOptions{Context: *ctx, Namespace: *ns, Image: *image, DashboardImage: *dashboardImage, PR: *pr})
		if e != nil {
			return e
		}
		return output(s)
	case "cleanup":
		f := flag.NewFlagSet("cleanup", flag.ContinueOnError)
		id := f.String("session", "", "session ID")
		if e := f.Parse(a[1:]); e != nil {
			return e
		}
		if *id == "" {
			return fmt.Errorf("--session is required")
		}
		if e := t.Cleanup(context.Background(), *id); e != nil {
			return e
		}
		fmt.Println("restored successfully")
		return nil
	case "status":
		f := flag.NewFlagSet("status", flag.ContinueOnError)
		id := f.String("session", "", "session ID")
		if e := f.Parse(a[1:]); e != nil {
			return e
		}
		s, e := t.Load(*id)
		if e != nil {
			return e
		}
		return output(s)
	case "recover":
		ss, e := t.Sessions()
		if e != nil {
			return e
		}
		for _, s := range ss {
			if !s.Completed {
				fmt.Println(s.ID)
			}
		}
		return nil
	}
	return usage()
}
func stateDir() string {
	if d := os.Getenv("ODH_PR_DEPLOY_STATE_DIR"); d != "" {
		return d
	}
	h, e := os.UserHomeDir()
	if e != nil {
		return ".odh-pr-deploy"
	}
	return filepath.Join(h, ".local", "state", "odh-pr-deploy")
}
func output(v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e == nil {
		fmt.Println(string(b))
	}
	return e
}
func usage() error { return fmt.Errorf("usage: odh-pr-deploy <inspect|deploy|cleanup|status|recover>") }
