package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/docker/docker/client"
	"github.com/tanq16/bunshin/internal/dockercontroller"
	"github.com/tanq16/bunshin/internal/envmanager"
	"github.com/tanq16/bunshin/internal/stackmanager"
)

//go:embed frontend/*
var staticFiles embed.FS

func main() {
	dataPath := "./data"
	for i, arg := range os.Args {
		if arg == "--data" && i+1 < len(os.Args) {
			dataPath = os.Args[i+1]
		}
	}

	pw := os.Getenv("BUNSHIN_ENV_PW")
	if pw == "" {
		log.Fatal("BUNSHIN_ENV_PW environment variable is required")
	}

	os.MkdirAll(filepath.Join(dataPath, "stacks"), 0755)
	os.MkdirAll(filepath.Join(dataPath, "env"), 0755)

	envMgr := envmanager.New(dataPath, pw)
	stackMgr := stackmanager.New(dataPath, envMgr)
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatal("Moby SDK Connection Error:", err)
	}
	dockerCtrl := dockercontroller.New(cli)

	staticFS, err := fs.Sub(staticFiles, "frontend")
	if err != nil {
		log.Fatal("Failed to create static filesystem:", err)
	}

	http.HandleFunc("/api/stacks", handleListStacks(stackMgr))
	http.HandleFunc("/api/stack/get", handleGetStack(stackMgr))
	http.HandleFunc("/api/stack/save", handleSaveStack(stackMgr))
	http.HandleFunc("/api/stack/status", handleStatus(dockerCtrl))
	http.HandleFunc("/api/stack/action", handleAction(dockerCtrl, stackMgr, envMgr))
	http.HandleFunc("/api/stack/containers", handleContainers(dockerCtrl))
	http.HandleFunc("/ws/logs", dockerCtrl.HandleLogs)
	http.HandleFunc("/ws/shell", dockerCtrl.HandleShell)
	http.Handle("/", http.FileServer(http.FS(staticFS)))

	log.Println("Bunshin | Port: 8080 | Data: ", dataPath)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func handleListStacks(stackMgr *stackmanager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[API] Listing stacks")
		stacks := stackMgr.ListStacks()
		log.Printf("[API] Found %d stack(s)", len(stacks))
		json.NewEncoder(w).Encode(stacks)
	}
}

func handleGetStack(stackMgr *stackmanager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		log.Printf("[API] Loading stack '%s'", name)
		yml, envStr, err := stackMgr.GetStack(name)
		if err != nil {
			log.Printf("[API] Error reading stack '%s': %v", name, err)
		}
		if envStr != "" {
			log.Printf("[API] Loaded encrypted environment for stack '%s'", name)
		}
		json.NewEncoder(w).Encode(map[string]string{"yaml": yml, "env": envStr})
	}
}

func handleSaveStack(stackMgr *stackmanager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Name, YAML, Env string }
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("[API] Error decoding save request: %v", err)
			return
		}
		log.Printf("[API] Saving stack '%s'", req.Name)
		if err := stackMgr.SaveStack(req.Name, req.YAML, req.Env); err != nil {
			log.Printf("[API] Error saving stack '%s': %v", req.Name, err)
			return
		}
		log.Printf("[API] Successfully saved stack '%s'", req.Name)
	}
}

func handleStatus(dockerCtrl *dockercontroller.Controller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		status := dockerCtrl.GetStatus(name)
		fmt.Fprint(w, status)
	}
}

func handleAction(dockerCtrl *dockercontroller.Controller, stackMgr *stackmanager.Manager, envMgr *envmanager.Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		action := r.URL.Query().Get("action")
		ctx := context.Background()
		log.Printf("[ACTION] Stack '%s' - executing action: %s", name, action)
		if action == "stop" {
			if err := dockerCtrl.StopStack(ctx, name); err != nil {
				log.Printf("[STOP] Error stopping stack '%s': %v", name, err)
				w.WriteHeader(500)
				return
			}
		} else {
			project, err := stackMgr.LoadProject(ctx, name)
			if err != nil {
				log.Printf("[START] Error loading stack '%s': %v", name, err)
				w.WriteHeader(500)
				return
			}
			stackEnv := envMgr.GetEnvMap(name)
			isUpdate := action == "update"
			if err := dockerCtrl.StartStack(ctx, name, project, isUpdate, stackEnv); err != nil {
				log.Printf("[START] Error starting stack '%s': %v", name, err)
				w.WriteHeader(500)
				return
			}
			log.Printf("[ACTION] Stack '%s' action '%s' completed successfully", name, action)
		}
		w.WriteHeader(200)
	}
}

func handleContainers(dockerCtrl *dockercontroller.Controller) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		containers, err := dockerCtrl.ListContainers(name)
		if err != nil {
			log.Printf("[API] Error listing containers for stack '%s': %v", name, err)
			w.WriteHeader(500)
			return
		}
		json.NewEncoder(w).Encode(containers)
	}
}
