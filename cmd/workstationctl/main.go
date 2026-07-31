package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type client struct {
	baseURL string
	session string
	http    *http.Client
}

func main() {
	api := env("WORKSTATION_MANAGER_URL", "http://127.0.0.1:8080")
	session := os.Getenv("WORKSTATION_MANAGER_SESSION")
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	c := &client{
		baseURL: strings.TrimRight(api, "/"), session: session,
		http: &http.Client{Timeout: 15 * time.Minute},
	}
	var err error
	switch args[0] {
	case "status":
		err = c.print(http.MethodGet, "/api/v1/status", nil)
	case "list":
		err = c.print(http.MethodGet, "/api/v1/workstations", nil)
	case "create":
		err = create(c, args[1:])
	case "start", "stop", "restart", "update", "delete":
		if len(args) != 2 {
			err = fmt.Errorf("usage: workstationctl %s <workstation-id>", args[0])
			break
		}
		err = c.print(http.MethodPost, "/api/v1/workstations/"+args[1]+"/actions/"+args[0], map[string]any{})
	case "apps":
		if len(args) == 2 && args[1] == "list" {
			err = c.print(http.MethodGet, "/api/v1/apps", nil)
		} else {
			err = errors.New("usage: workstationctl apps list")
		}
	case "templates":
		if len(args) == 2 && args[1] == "list" {
			err = c.print(http.MethodGet, "/api/v1/templates", nil)
		} else {
			err = errors.New("usage: workstationctl templates list")
		}
	case "reconcile":
		err = c.print(http.MethodPost, "/api/v1/admin/reconcile", map[string]any{})
	case "backup":
		path := ""
		if len(args) == 2 {
			path = args[1]
		}
		err = c.print(http.MethodPost, "/api/v1/admin/backup", map[string]string{"path": path})
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func create(c *client, args []string) error {
	set := flag.NewFlagSet("create", flag.ContinueOnError)
	name := set.String("name", "", "workstation name")
	templateID := set.String("template", "terminal", "template id")
	apps := set.String("apps", "", "comma-separated app ids (defaults to template)")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *name == "" {
		return errors.New("--name is required")
	}
	var selected []string
	for _, app := range strings.Split(*apps, ",") {
		if app = strings.TrimSpace(app); app != "" {
			selected = append(selected, app)
		}
	}
	return c.print(http.MethodPost, "/api/v1/workstations", map[string]any{
		"name": *name, "template_id": *templateID, "apps": selected,
	})
}

func (c *client) print(method, path string, input any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	request, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if c.session != "" {
		request.AddCookie(&http.Cookie{Name: "wm_session", Value: c.session})
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", c.baseURL)
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	var value any
	if json.Unmarshal(data, &value) == nil {
		formatted, _ := json.MarshalIndent(value, "", "  ")
		fmt.Println(string(formatted))
	} else {
		fmt.Print(string(data))
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("controller returned %s", response.Status)
	}
	return nil
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: workstationctl <command>

commands:
  status
  list
  create --name NAME [--template ID] [--apps terminal,files]
  start|stop|restart|update|delete WORKSTATION_ID
  apps list
  templates list
  reconcile
  backup [PATH]

environment:
  WORKSTATION_MANAGER_URL       controller URL
  WORKSTATION_MANAGER_SESSION   wm_session cookie value`)
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
