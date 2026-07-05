// cpctl is the CLI for the control plane. It is a plain API client
// (ADR-0007): no privileged path into the core, just the same REST contract
// the Portal will use.
//
//	cpctl apply -f resource.yaml
//	cpctl get <kind> [name]
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"text/tabwriter"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/tonyrosario/setpoint/core/api"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "apply":
		err = apply(os.Args[2:])
	case "get":
		err = get(os.Args[2:])
	case "delete":
		err = deleteCmd(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage:
  cpctl apply -f <file.yaml>   apply a Resource Definition
  cpctl get <kind> [name]      show resources
  cpctl delete <kind> <name>   request deletion of a resource

environment:
  SETPOINT_SERVER   API address (default http://127.0.0.1:8080)`)
	os.Exit(2)
}

func serverURL() string {
	if v := os.Getenv("SETPOINT_SERVER"); v != "" {
		return v
	}
	return "http://127.0.0.1:8080"
}

func actor() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "anonymous"
}

// applyDoc is the Resource Definition file shape: desired state only.
type applyDoc struct {
	Kind string         `yaml:"kind"`
	Name string         `yaml:"name"`
	Spec map[string]any `yaml:"spec"`
}

func apply(args []string) error {
	if len(args) != 2 || args[0] != "-f" {
		return fmt.Errorf("usage: cpctl apply -f <file.yaml>")
	}
	raw, err := os.ReadFile(args[1])
	if err != nil {
		return err
	}

	var doc applyDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", args[1], err)
	}
	if doc.Kind == "" || doc.Name == "" || doc.Spec == nil {
		return fmt.Errorf("%s: kind, name, and spec are all required", args[1])
	}

	spec, err := json.Marshal(doc.Spec)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]json.RawMessage{"spec": spec})
	if err != nil {
		return err
	}

	url := fmt.Sprintf("%s/v1/%s/%s", serverURL(), doc.Kind, doc.Name)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Setpoint-Actor", actor())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return apiError(resp)
	}

	fmt.Printf("%s/%s accepted\n", doc.Kind, doc.Name)
	return nil
}

func get(args []string) error {
	switch len(args) {
	case 1:
		return getList(args[0])
	case 2:
		return getOne(args[0], args[1])
	default:
		return fmt.Errorf("usage: cpctl get <kind> [name]")
	}
}

func deleteCmd(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: cpctl delete <kind> <name>")
	}
	kind, name := args[0], args[1]

	url := fmt.Sprintf("%s/v1/%s/%s", serverURL(), kind, name)
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Setpoint-Actor", actor())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return apiError(resp)
	}

	fmt.Printf("%s/%s deletion requested\n", kind, name)
	return nil
}

func getList(kind string) error {
	resp, err := http.Get(fmt.Sprintf("%s/v1/%s", serverURL(), kind))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}

	var out struct {
		Items []*api.Resource `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	printTable(out.Items)
	return nil
}

func getOne(kind, name string) error {
	resp, err := http.Get(fmt.Sprintf("%s/v1/%s/%s", serverURL(), kind, name))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}

	var res api.Resource
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return err
	}
	printTable([]*api.Resource{&res})
	return nil
}

func printTable(items []*api.Resource) {
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "KIND\tNAME\tREADY\tPHASE\tMESSAGE\tAGE")
	for _, res := range items {
		age := "-"
		if !res.Metadata.CreatedAt.IsZero() {
			age = time.Since(res.Metadata.CreatedAt).Round(time.Second).String()
		}
		fmt.Fprintf(w, "%s\t%s\t%v\t%s\t%s\t%s\n",
			res.Kind, res.Name, res.Status.Ready, res.Status.Phase, res.Status.Message, age)
	}
	w.Flush()
}

func apiError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return fmt.Errorf("%s: %s", resp.Status, e.Error)
	}
	return fmt.Errorf("%s", resp.Status)
}
