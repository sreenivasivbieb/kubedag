// Command cli is a thin client for the orchestrator API.
//
// Thin on purpose: every workflow the CLI can do, a 200-line Go program with
// net/http can do. Business logic in the CLI is business logic you cannot
// test and cannot reuse.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/sreenivasivbieb/kubedag/internal/config"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `kubedag-cli <command>

Commands:
  submit <file.json>   Submit a workflow definition
  get <workflowId>     Fetch run and step status
  list                 List runs
  cancel <workflowId>  Cancel a run
  retry <workflowId>   Re-run a failed run from the failure point
  logs <workflowId> <stepId>   Fetch step logs
  pods                 List the runner pod pool

Environment:
  KUBEDAG_API_ADDR   API base URL (default http://127.0.0.1:8080)
`)
}

func run(args []string) error {
	base := os.Getenv("KUBEDAG_API_ADDR")
	if base == "" {
		base = "http://127.0.0.1:8080"
	}

	if len(args) == 0 {
		usage()
		return fmt.Errorf("missing command")
	}

	c := &client{base: base, hc: &http.Client{Timeout: 30 * time.Second}}

	switch args[0] {
	case "submit":
		if len(args) < 2 {
			return fmt.Errorf("usage: submit <file.json>")
		}
		return c.submit(args[1])
	case "get":
		if len(args) < 2 {
			return fmt.Errorf("usage: get <workflowId>")
		}
		return c.simple("GET", "/workflow/"+args[1], nil)
	case "list":
		return c.simple("GET", "/workflow", nil)
	case "cancel":
		if len(args) < 2 {
			return fmt.Errorf("usage: cancel <workflowId>")
		}
		return c.simple("POST", "/workflow/"+args[1]+"/cancel", nil)
	case "retry":
		if len(args) < 2 {
			return fmt.Errorf("usage: retry <workflowId>")
		}
		return c.simple("POST", "/workflow/"+args[1]+"/retry", nil)
	case "logs":
		if len(args) < 3 {
			return fmt.Errorf("usage: logs <workflowId> <stepId>")
		}
		return c.simple("GET", "/workflow/"+args[1]+"/steps/"+args[2]+"/logs", nil)
	case "pods":
		return c.simple("GET", "/pods", nil)
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

type client struct {
	base string
	hc   *http.Client
}

func (c *client) simple(method, path string, body []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	return pretty(resp.StatusCode, resp.Body)
}

func (c *client) submit(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	// Parse before sending so the user gets a useful local error rather than
	// a 400 from the server with no line information.
	var probe map[string]any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	return c.simple("POST", "/workflow", raw)
}

// pretty prints JSON indented when it parses, and raw otherwise, so error
// bodies (which are JSON but small) stay readable.
func pretty(status int, r io.Reader) error {
	raw, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err == nil {
		out, _ := json.MarshalIndent(v, "", "  ")
		fmt.Println(string(out))
	} else {
		fmt.Println(string(raw))
	}
	if status >= 400 {
		return fmt.Errorf("server returned %d", status)
	}
	return nil
}

var _ = config.RoleOrchestrator // keep config import meaningful for later phases
