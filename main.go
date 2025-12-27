package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/goccy/go-yaml"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/pbkdf2"
)

//go:embed static/*
var staticFiles embed.FS

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	dataPath string
	envKey   []byte
)

// ComposeSchema reflects a "dumb" mapping.
// It doesn't orchestrate networks/volumes, just uses what's provided.
type ComposeSchema struct {
	Services map[string]struct {
		Image       string   `yaml:"image"`
		Container   string   `yaml:"container_name"`
		Environment []string `yaml:"environment"`
		Volumes     []string `yaml:"volumes"`
		Ports       []string `yaml:"ports"`
		Networks    []string `yaml:"networks"`
		NetworkMode string   `yaml:"network_mode"`
	} `yaml:"services"`
}

func main() {
	dataPath = "./data"
	for i, arg := range os.Args {
		if arg == "--data" && i+1 < len(os.Args) {
			dataPath = os.Args[i+1]
		}
	}

	pw := os.Getenv("BUNSHIN_ENV_PW")
	if pw == "" {
		log.Fatal("BUNSHIN_ENV_PW environment variable is required")
	}
	// PBKDF2 Key Derivation
	envKey = pbkdf2.Key([]byte(pw), []byte("bunshin-v1-salt"), 4096, 32, sha256.New)

	os.MkdirAll(filepath.Join(dataPath, "stacks"), 0755)
	os.MkdirAll(filepath.Join(dataPath, "env"), 0755)

	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		log.Fatal("Moby SDK Connection Error:", err)
	}

	// Create a sub-filesystem starting at the "static" directory
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatal("Failed to create static filesystem:", err)
	}

	http.HandleFunc("/api/stacks", handleListStacks)
	http.HandleFunc("/api/stack/get", handleGetStack)
	http.HandleFunc("/api/stack/save", handleSaveStack)
	http.HandleFunc("/api/stack/status", handleStatus(cli))
	http.HandleFunc("/api/stack/action", handleAction(cli))
	http.HandleFunc("/ws/logs", handleLogs(cli))
	http.HandleFunc("/ws/shell", handleShell(cli))

	// Serve static files from the root path
	http.Handle("/", http.FileServer(http.FS(staticFS)))

	log.Println("Bunshin v0.1.0 | Port :8080 | Data:", dataPath)
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// --- AES-GCM Crypto ---

func encrypt(data []byte) ([]byte, error) {
	block, _ := aes.NewCipher(envKey)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	return gcm.Seal(nonce, nonce, data, nil), nil
}

func decrypt(data []byte) ([]byte, error) {
	block, _ := aes.NewCipher(envKey)
	gcm, _ := cipher.NewGCM(block)
	ns := gcm.NonceSize()
	if len(data) < ns {
		return nil, fmt.Errorf("ciphertext too short")
	}
	return gcm.Open(nil, data[:ns], data[ns:], nil)
}

// --- API Handlers ---

func handleListStacks(w http.ResponseWriter, r *http.Request) {
	files, _ := os.ReadDir(filepath.Join(dataPath, "stacks"))
	stacks := []string{}
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".yml") {
			stacks = append(stacks, strings.TrimSuffix(f.Name(), ".yml"))
		}
	}
	json.NewEncoder(w).Encode(stacks)
}

func handleGetStack(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	yml, _ := os.ReadFile(filepath.Join(dataPath, "stacks", name+".yml"))
	var envStr string
	if enc, err := os.ReadFile(filepath.Join(dataPath, "env", name+".env")); err == nil {
		dec, _ := decrypt(enc)
		envStr = string(dec)
	}
	json.NewEncoder(w).Encode(map[string]string{"yaml": string(yml), "env": envStr})
}

func handleSaveStack(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, YAML, Env string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return
	}
	os.WriteFile(filepath.Join(dataPath, "stacks", req.Name+".yml"), []byte(req.YAML), 0644)
	enc, _ := encrypt([]byte(req.Env))
	os.WriteFile(filepath.Join(dataPath, "env", req.Name+".env"), enc, 0644)
}

func handleStatus(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		f := filters.NewArgs()
		f.Add("label", "bunshin.stack="+name)
		containers, _ := cli.ContainerList(context.Background(), container.ListOptions{Filters: f})
		if len(containers) > 0 {
			fmt.Fprint(w, "Operational")
		} else {
			fmt.Fprint(w, "Stopped")
		}
	}
}

func handleAction(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		action := r.URL.Query().Get("action")
		ctx := context.Background()

		f := filters.NewArgs()
		f.Add("label", "bunshin.stack="+name)

		if action == "stop" {
			containers, _ := cli.ContainerList(ctx, container.ListOptions{Filters: f, All: true})
			for _, c := range containers {
				cli.ContainerStop(ctx, c.ID, container.StopOptions{})
				cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true})
			}
		} else {
			ymlData, _ := os.ReadFile(filepath.Join(dataPath, "stacks", name+".yml"))
			var comp ComposeSchema
			yaml.Unmarshal(ymlData, &comp)

			for svcName, svc := range comp.Services {
				if action == "update" {
					out, err := cli.ImagePull(ctx, svc.Image, image.PullOptions{})
					if err == nil {
						io.Copy(io.Discard, out)
						out.Close()
					}
				}

				cName := svc.Container
				if cName == "" {
					cName = name + "_" + svcName
				}

				// Manual Port Mapping
				portMap := nat.PortMap{}
				exposedPorts := nat.PortSet{}
				for _, p := range svc.Ports {
					parts := strings.Split(p, ":")
					if len(parts) == 2 {
						port := nat.Port(parts[1] + "/tcp")
						exposedPorts[port] = struct{}{}
						portMap[port] = []nat.PortBinding{{HostIP: "0.0.0.0", HostPort: parts[0]}}
					}
				}

				config := &container.Config{
					Image:        svc.Image,
					Env:          svc.Environment,
					Labels:       map[string]string{"bunshin.stack": name, "bunshin.managed": "true"},
					ExposedPorts: exposedPorts,
				}

				hostConfig := &container.HostConfig{
					Binds:         svc.Volumes,
					PortBindings:  portMap,
					NetworkMode:   container.NetworkMode(svc.NetworkMode),
					RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
				}

				// Dumb network handling: use the first specified network if no mode is set
				if hostConfig.NetworkMode == "" && len(svc.Networks) > 0 {
					hostConfig.NetworkMode = container.NetworkMode(svc.Networks[0])
				}

				// Always attempt removal before start to ensure fresh state
				cli.ContainerRemove(ctx, cName, container.RemoveOptions{Force: true})
				resp, err := cli.ContainerCreate(ctx, config, hostConfig, nil, nil, cName)
				if err != nil {
					log.Printf("Create error for %s: %v", cName, err)
					continue
				}
				cli.ContainerStart(ctx, resp.ID, container.StartOptions{})
			}

			if action == "update" {
				pf := filters.NewArgs()
				pf.Add("dangling", "true")
				cli.ImagesPrune(ctx, pf)
			}
		}
		w.WriteHeader(200)
	}
}

func handleLogs(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, _ := upgrader.Upgrade(w, r, nil)
		defer conn.Close()
		name := r.URL.Query().Get("name")
		f := filters.NewArgs()
		f.Add("label", "bunshin.stack="+name)
		containers, _ := cli.ContainerList(context.Background(), container.ListOptions{Filters: f})
		if len(containers) == 0 {
			return
		}

		logs, err := cli.ContainerLogs(context.Background(), containers[0].ID, container.LogsOptions{ShowStdout: true, ShowStderr: true, Follow: true, Tail: "200"})
		if err != nil {
			return
		}
		defer logs.Close()

		hdr := make([]byte, 8)
		for {
			if _, err := logs.Read(hdr); err != nil {
				break
			}
			size := uint32(hdr[4])<<24 | uint32(hdr[5])<<16 | uint32(hdr[6])<<8 | uint32(hdr[7])
			payload := make([]byte, size)
			logs.Read(payload)
			conn.WriteMessage(websocket.TextMessage, payload)
		}
	}
}

func handleShell(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, _ := upgrader.Upgrade(w, r, nil)
		defer conn.Close()
		name := r.URL.Query().Get("name")
		f := filters.NewArgs()
		f.Add("label", "bunshin.stack="+name)
		containers, _ := cli.ContainerList(context.Background(), container.ListOptions{Filters: f})
		if len(containers) == 0 {
			return
		}

		exec, err := cli.ContainerExecCreate(context.Background(), containers[0].ID, container.ExecOptions{
			AttachStdin: true, AttachStdout: true, AttachStderr: true, Tty: true, Cmd: []string{"/bin/sh"},
		})
		if err != nil {
			return
		}
		resp, err := cli.ContainerExecAttach(context.Background(), exec.ID, container.ExecStartOptions{Tty: true})
		if err != nil {
			return
		}
		defer resp.Close()

		go func() {
			for {
				_, msg, err := conn.ReadMessage()
				if err != nil {
					return
				}
				resp.Conn.Write(msg)
			}
		}()

		buf := make([]byte, 4096)
		for {
			n, err := resp.Reader.Read(buf)
			if err != nil {
				return
			}
			conn.WriteMessage(websocket.TextMessage, buf[:n])
		}
	}
}
