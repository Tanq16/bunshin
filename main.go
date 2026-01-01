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
	"slices"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/gorilla/websocket"
	"golang.org/x/crypto/pbkdf2"
)

//go:embed frontend/*
var staticFiles embed.FS

var (
	upgrader = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
	dataPath string
	envKey   []byte
)

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

	// Create a sub-filesystem starting at the "frontend" directory
	staticFS, err := fs.Sub(staticFiles, "frontend")
	if err != nil {
		log.Fatal("Failed to create static filesystem:", err)
	}

	http.HandleFunc("/api/stacks", handleListStacks)
	http.HandleFunc("/api/stack/get", handleGetStack)
	http.HandleFunc("/api/stack/save", handleSaveStack)
	http.HandleFunc("/api/stack/status", handleStatus(cli))
	http.HandleFunc("/api/stack/action", handleAction(cli))
	http.HandleFunc("/api/stack/containers", handleContainers(cli))
	http.HandleFunc("/ws/logs", handleLogs(cli))
	http.HandleFunc("/ws/shell", handleShell(cli))

	// Serve frontend files from the root path
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

func parseEnvFile(envContent string) map[string]string {
	envMap := make(map[string]string)
	lines := strings.SplitSeq(envContent, "\n")
	for line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
				value = value[1 : len(value)-1]
			}
			envMap[key] = value
		}
	}
	return envMap
}

// --- Network Helpers ---

func resolveNetworkName(ctx context.Context, cli *client.Client, networkName string) (string, error) {
	specialModes := []string{"host", "bridge", "none"}
	if slices.Contains(specialModes, networkName) {
		return networkName, nil
	}
	if strings.HasPrefix(networkName, "container:") {
		return networkName, nil
	}

	actualNetworkName := networkName
	if after, ok := strings.CutPrefix(networkName, "service:"); ok {
		containerName := after
		log.Printf("[NETWORK] Resolving network for service/container: %s", containerName)
		// Try to find the container and get its network
		f := filters.NewArgs()
		f.Add("name", containerName)
		containers, err := cli.ContainerList(ctx, container.ListOptions{Filters: f, All: true})
		if err == nil && len(containers) > 0 {
			containerJSON, err := cli.ContainerInspect(ctx, containers[0].ID)
			if err == nil && len(containerJSON.NetworkSettings.Networks) > 0 {
				for netName := range containerJSON.NetworkSettings.Networks {
					actualNetworkName = netName
					log.Printf("[NETWORK] Found network '%s' from container '%s'", actualNetworkName, containerName)
					break
				}
			}
		}
		if actualNetworkName == networkName {
			actualNetworkName = containerName
			log.Printf("[NETWORK] Trying network name '%s' directly", actualNetworkName)
		}
	}

	networks, err := cli.NetworkList(ctx, network.ListOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to list networks: %w", err)
	}
	for _, net := range networks {
		if net.Name == actualNetworkName || net.ID == actualNetworkName {
			log.Printf("[NETWORK] Network '%s' found (ID: %s)", actualNetworkName, net.ID[:12])
			return net.Name, nil
		}
	}

	return "", fmt.Errorf("network '%s' not found. Available networks: %v", actualNetworkName, getNetworkNames(networks))
}

func getNetworkNames(networks []network.Summary) []string {
	names := make([]string, 0, len(networks))
	for _, net := range networks {
		names = append(names, net.Name)
	}
	return names
}

// --- API Handlers ---

func handleListStacks(w http.ResponseWriter, r *http.Request) {
	log.Printf("[API] Listing stacks from %s/stacks", dataPath)
	files, _ := os.ReadDir(filepath.Join(dataPath, "stacks"))
	stacks := []string{}
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".yml") {
			stacks = append(stacks, strings.TrimSuffix(f.Name(), ".yml"))
		}
	}
	log.Printf("[API] Found %d stack(s)", len(stacks))
	json.NewEncoder(w).Encode(stacks)
}

func handleGetStack(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	log.Printf("[API] Loading stack '%s'", name)
	yml, err := os.ReadFile(filepath.Join(dataPath, "stacks", name+".yml"))
	if err != nil {
		log.Printf("[API] Error reading stack '%s': %v", name, err)
	}
	var envStr string
	if enc, err := os.ReadFile(filepath.Join(dataPath, "env", name+".env")); err == nil {
		dec, _ := decrypt(enc)
		envStr = string(dec)
		log.Printf("[API] Loaded encrypted environment for stack '%s'", name)
	}
	json.NewEncoder(w).Encode(map[string]string{"yaml": string(yml), "env": envStr})
}

func handleSaveStack(w http.ResponseWriter, r *http.Request) {
	var req struct{ Name, YAML, Env string }
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Printf("[API] Error decoding save request: %v", err)
		return
	}
	log.Printf("[API] Saving stack '%s'", req.Name)
	if err := os.WriteFile(filepath.Join(dataPath, "stacks", req.Name+".yml"), []byte(req.YAML), 0644); err != nil {
		log.Printf("[API] Error writing stack file '%s': %v", req.Name, err)
		return
	}
	enc, _ := encrypt([]byte(req.Env))
	if err := os.WriteFile(filepath.Join(dataPath, "env", req.Name+".env"), enc, 0644); err != nil {
		log.Printf("[API] Error writing env file '%s': %v", req.Name, err)
		return
	}
	log.Printf("[API] Successfully saved stack '%s'", req.Name)
}

func handleStatus(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		f := filters.NewArgs()
		f.Add("label", "bunshin.stack="+name)
		containers, _ := cli.ContainerList(context.Background(), container.ListOptions{Filters: f})
		status := "Stopped"
		if len(containers) > 0 {
			status = "Operational"
		}
		fmt.Fprint(w, status)
	}
}

func getContainerInstanceNumber(ctx context.Context, cli *client.Client, stackName, serviceName string) int {
	f := filters.NewArgs()
	f.Add("label", "bunshin.stack="+stackName)
	f.Add("label", "bunshin.service="+serviceName)
	containers, _ := cli.ContainerList(ctx, container.ListOptions{Filters: f, All: true})
	return len(containers) + 1
}

func findService(project *types.Project, serviceName string) *types.ServiceConfig {
	for i := range project.Services {
		if project.Services[i].Name == serviceName {
			svc := project.Services[i]
			return &svc
		}
	}
	return nil
}

func waitForDependencies(ctx context.Context, cli *client.Client, project *types.Project, stackName string, svc types.ServiceConfig) error {
	if len(svc.DependsOn) == 0 {
		return nil
	}

	log.Printf("[WAIT] Service '%s' waiting for dependencies", svc.Name)

	for depName, condition := range svc.DependsOn {
		depService := findService(project, depName)
		if depService == nil {
			return fmt.Errorf("dependency '%s' not found in project", depName)
		}

		targetContainerName := depService.ContainerName
		if targetContainerName == "" {
			targetContainerName = fmt.Sprintf("%s_%s_1", stackName, depName)
		}

		timeout := time.After(60 * time.Second)
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		ready := false
		for !ready {
			select {
			case <-timeout:
				return fmt.Errorf("timeout waiting for dependency '%s'", depName)
			case <-ctx.Done():
				return ctx.Err()
			case <-ticker.C:
				inspect, err := cli.ContainerInspect(ctx, targetContainerName)
				if err != nil {
					continue
				}

				switch condition.Condition {
				case "service_healthy":
					if inspect.State.Health != nil && inspect.State.Health.Status == "healthy" {
						ready = true
					}
				case "service_completed_successfully":
					if inspect.State.Status == "exited" && inspect.State.ExitCode == 0 {
						ready = true
					}
				default:
					if inspect.State.Status == "running" {
						ready = true
					}
				}
			}
		}
		log.Printf("[WAIT] Dependency '%s' is ready", depName)
	}
	return nil
}

func handleAction(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		action := r.URL.Query().Get("action")
		ctx := context.Background()

		log.Printf("[ACTION] Stack '%s' - executing action: %s", name, action)

		f := filters.NewArgs()
		f.Add("label", "bunshin.stack="+name)

		if action == "stop" {
			log.Printf("[STOP] Stopping stack '%s'", name)
			containers, _ := cli.ContainerList(ctx, container.ListOptions{Filters: f, All: true})
			log.Printf("[STOP] Found %d container(s) to stop for stack '%s'", len(containers), name)
			for _, c := range containers {
				log.Printf("[STOP] Stopping container '%s' (ID: %s)", c.Names[0], c.ID[:12])
				if err := cli.ContainerStop(ctx, c.ID, container.StopOptions{}); err != nil {
					log.Printf("[STOP] Error stopping container '%s': %v", c.Names[0], err)
				}
				log.Printf("[STOP] Removing container '%s'", c.Names[0])
				if err := cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
					log.Printf("[STOP] Error removing container '%s': %v", c.Names[0], err)
				} else {
					log.Printf("[STOP] Successfully removed container '%s'", c.Names[0])
				}
			}
			log.Printf("[STOP] Stack '%s' stopped successfully", name)
		} else {
			ymlData, err := os.ReadFile(filepath.Join(dataPath, "stacks", name+".yml"))
			if err != nil {
				log.Printf("[START] Error reading stack file '%s': %v", name, err)
				w.WriteHeader(500)
				return
			}

			envMap := make(map[string]string)
			if enc, err := os.ReadFile(filepath.Join(dataPath, "env", name+".env")); err == nil {
				dec, err := decrypt(enc)
				if err != nil {
					log.Printf("[START] Error decrypting env file for stack '%s': %v", name, err)
				} else {
					envMap = parseEnvFile(string(dec))
					log.Printf("[START] Loaded %d environment variable(s) from .env file for stack '%s'", len(envMap), name)
				}
			} else {
				log.Printf("[START] No .env file found for stack '%s' (this is okay)", name)
			}

			project, err := loader.LoadWithContext(ctx, types.ConfigDetails{
				WorkingDir: ".",
				ConfigFiles: []types.ConfigFile{
					{Filename: name + ".yml", Content: ymlData},
				},
				Environment: envMap,
			})
			if err != nil {
				log.Printf("[START] Error parsing stack '%s': %v", name, err)
				w.WriteHeader(500)
				return
			}

			log.Printf("[START] Starting stack '%s' with %d service(s)", name, len(project.Services))

			for _, svc := range project.Services {
				log.Printf("[SERVICE] Processing service '%s' from stack '%s'", svc.Name, name)
				log.Printf("[SERVICE] Image: %s", svc.Image)

				if action == "update" {
					log.Printf("[UPDATE] Pulling latest image for '%s'", svc.Image)
					out, err := cli.ImagePull(ctx, svc.Image, image.PullOptions{})
					if err != nil {
						log.Printf("[UPDATE] Error pulling image '%s': %v", svc.Image, err)
					} else {
						io.Copy(io.Discard, out)
						out.Close()
						log.Printf("[UPDATE] Successfully pulled image '%s'", svc.Image)
					}
				}

				if err := waitForDependencies(ctx, cli, project, name, svc); err != nil {
					log.Printf("[ERROR] Dependency check failed for '%s': %v", svc.Name, err)
					continue
				}

				cName := svc.ContainerName
				if cName == "" {
					instanceNum := getContainerInstanceNumber(ctx, cli, name, svc.Name)
					cName = fmt.Sprintf("%s_%s_%d", name, svc.Name, instanceNum)
				}
				log.Printf("[SERVICE] Container name: %s", cName)

				exposedPorts := nat.PortSet{}
				portBindings := nat.PortMap{}
				for _, p := range svc.Ports {
					protocol := p.Protocol
					if protocol == "" {
						protocol = "tcp"
					}
					port := nat.Port(fmt.Sprintf("%d/%s", p.Target, protocol))
					exposedPorts[port] = struct{}{}
					if p.Published != "" {
						hostIP := p.HostIP
						if hostIP == "" {
							hostIP = "0.0.0.0"
						}
						portBindings[port] = []nat.PortBinding{{HostIP: hostIP, HostPort: p.Published}}
						log.Printf("[SERVICE] Mapping port %s:%d", p.Published, p.Target)
					}
				}

				binds := []string{}
				for _, v := range svc.Volumes {
					if v.Type == "bind" || v.Type == "" {
						bind := fmt.Sprintf("%s:%s", v.Source, v.Target)
						if v.ReadOnly {
							bind += ":ro"
						}
						binds = append(binds, bind)
					}
				}
				if len(binds) > 0 {
					log.Printf("[SERVICE] Mounting %d volume(s)", len(binds))
				}

				networkMode := ""
				var networkName string
				var networkingConfig *network.NetworkingConfig

				if svc.NetworkMode != "" {
					networkMode = svc.NetworkMode
					specialModes := []string{"host", "bridge", "none"}
					isSpecialMode := slices.Contains(specialModes, networkMode)
					if after, ok := strings.CutPrefix(networkMode, "service:"); ok {
						depService := findService(project, after)
						if depService != nil && depService.ContainerName != "" {
							networkMode = "container:" + depService.ContainerName
						} else {
							networkMode = fmt.Sprintf("container:%s_%s_1", name, after)
						}
						log.Printf("[SERVICE] Resolved network_mode to: %s", networkMode)
					} else if !isSpecialMode && !strings.HasPrefix(networkMode, "container:") {
						networkName = networkMode
						networkMode = ""
						log.Printf("[SERVICE] Using named network: %s", networkName)
					} else {
						log.Printf("[SERVICE] Using network_mode: %s", networkMode)
					}
				} else if len(svc.Networks) > 0 {
					for netName := range svc.Networks {
						networkName = netName
						break
					}
					log.Printf("[SERVICE] Using network: %s", networkName)
				} else {
					log.Printf("[SERVICE] Using default bridge network")
				}

				if networkName != "" {
					resolvedNetwork, err := resolveNetworkName(ctx, cli, networkName)
					if err != nil {
						log.Printf("[ERROR] Failed to resolve network '%s' for container '%s': %v", networkName, cName, err)
						log.Printf("[ERROR] Skipping container '%s' due to network error", cName)
						continue
					}
					networkingConfig = &network.NetworkingConfig{
						EndpointsConfig: map[string]*network.EndpointSettings{
							resolvedNetwork: {},
						},
					}
					log.Printf("[SERVICE] Configured to connect to network '%s'", resolvedNetwork)
				}

				envList := []string{}
				for k, v := range svc.Environment {
					if v != nil {
						envList = append(envList, fmt.Sprintf("%s=%s", k, *v))
					}
				}
				if len(envList) > 0 {
					log.Printf("[SERVICE] Setting %d environment variable(s)", len(envList))
				}

				var cmdSlice []string
				if len(svc.Command) > 0 {
					cmdSlice = []string(svc.Command)
				}

				config := &container.Config{
					Image:        svc.Image,
					Cmd:          cmdSlice,
					Env:          envList,
					ExposedPorts: exposedPorts,
					Labels:       map[string]string{"bunshin.stack": name, "bunshin.service": svc.Name, "bunshin.managed": "true"},
				}

				restartPolicy := container.RestartPolicy{}
				if svc.Restart != "" {
					restartPolicy.Name = container.RestartPolicyMode(svc.Restart)
				}

				hostConfig := &container.HostConfig{
					Binds:         binds,
					PortBindings:  portBindings,
					RestartPolicy: restartPolicy,
					CapAdd:        svc.CapAdd,
				}

				if networkMode != "" {
					hostConfig.NetworkMode = container.NetworkMode(networkMode)
				}

				if len(svc.CapAdd) > 0 {
					log.Printf("[SERVICE] Adding capabilities: %v", svc.CapAdd)
				}

				log.Printf("[SERVICE] Removing existing container '%s' if present", cName)
				cli.ContainerRemove(ctx, cName, container.RemoveOptions{Force: true})

				log.Printf("[SERVICE] Creating container '%s'", cName)
				resp, err := cli.ContainerCreate(ctx, config, hostConfig, networkingConfig, nil, cName)
				if err != nil {
					log.Printf("[ERROR] Failed to create container '%s': %v", cName, err)
					continue
				}
				log.Printf("[SERVICE] Starting container '%s' (ID: %s)", cName, resp.ID[:12])
				if err := cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
					log.Printf("[ERROR] Failed to start container '%s': %v", cName, err)
				} else {
					log.Printf("[SERVICE] Successfully started container '%s'", cName)
				}
			}

			if action == "update" {
				log.Printf("[UPDATE] Pruning dangling images for stack '%s'", name)
				pf := filters.NewArgs()
				pf.Add("dangling", "true")
				pruneReport, err := cli.ImagesPrune(ctx, pf)
				if err != nil {
					log.Printf("[UPDATE] Error pruning images: %v", err)
				} else {
					log.Printf("[UPDATE] Reclaimed %d bytes from dangling images", pruneReport.SpaceReclaimed)
				}
			}

			log.Printf("[ACTION] Stack '%s' action '%s' completed successfully", name, action)
		}
		w.WriteHeader(200)
	}
}

func handleContainers(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		f := filters.NewArgs()
		f.Add("label", "bunshin.stack="+name)
		containers, _ := cli.ContainerList(context.Background(), container.ListOptions{Filters: f})

		type ContainerInfo struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		containerList := []ContainerInfo{}
		for _, c := range containers {
			containerName := strings.TrimPrefix(c.Names[0], "/")
			containerList = append(containerList, ContainerInfo{
				ID:   c.ID,
				Name: containerName,
			})
		}
		json.NewEncoder(w).Encode(containerList)
	}
}

func handleLogs(cli *client.Client) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		containerID := r.URL.Query().Get("container")
		log.Printf("[LOGS] WebSocket connection requested for stack '%s', container '%s'", name, containerID)

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[LOGS] Error upgrading connection: %v", err)
			return
		}
		defer conn.Close()

		f := filters.NewArgs()
		f.Add("label", "bunshin.stack="+name)
		containers, _ := cli.ContainerList(context.Background(), container.ListOptions{Filters: f})
		if len(containers) == 0 {
			log.Printf("[LOGS] No containers found for stack '%s'", name)
			return
		}

		var targetContainer *container.Summary
		if containerID != "" {
			for i := range containers {
				if containers[i].ID == containerID || strings.HasPrefix(containers[i].ID, containerID) {
					targetContainer = &containers[i]
					break
				}
			}
		}
		if targetContainer == nil {
			targetContainer = &containers[0]
		}

		containerName := strings.TrimPrefix(targetContainer.Names[0], "/")
		log.Printf("[LOGS] Streaming logs for container '%s' (ID: %s)", containerName, targetContainer.ID[:12])
		logs, err := cli.ContainerLogs(context.Background(), targetContainer.ID, container.LogsOptions{ShowStdout: true, ShowStderr: true, Follow: true, Tail: "200"})
		if err != nil {
			log.Printf("[LOGS] Error getting container logs: %v", err)
			return
		}
		defer logs.Close()

		hdr := make([]byte, 8)
		for {
			if _, err := logs.Read(hdr); err != nil {
				log.Printf("[LOGS] Log stream ended for stack '%s': %v", name, err)
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
		name := r.URL.Query().Get("name")
		containerID := r.URL.Query().Get("container")
		log.Printf("[SHELL] WebSocket connection requested for stack '%s', container '%s'", name, containerID)

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("[SHELL] Error upgrading connection: %v", err)
			return
		}
		defer conn.Close()

		f := filters.NewArgs()
		f.Add("label", "bunshin.stack="+name)
		containers, _ := cli.ContainerList(context.Background(), container.ListOptions{Filters: f})
		if len(containers) == 0 {
			log.Printf("[SHELL] No containers found for stack '%s'", name)
			return
		}

		var targetContainer *container.Summary
		if containerID != "" {
			for i := range containers {
				if containers[i].ID == containerID || strings.HasPrefix(containers[i].ID, containerID) {
					targetContainer = &containers[i]
					break
				}
			}
		}
		if targetContainer == nil {
			targetContainer = &containers[0]
		}

		containerName := strings.TrimPrefix(targetContainer.Names[0], "/")
		log.Printf("[SHELL] Opening shell in container '%s' (ID: %s)", containerName, targetContainer.ID[:12])
		exec, err := cli.ContainerExecCreate(context.Background(), targetContainer.ID, container.ExecOptions{
			AttachStdin: true, AttachStdout: true, AttachStderr: true, Tty: true, Cmd: []string{"/bin/sh"},
		})
		if err != nil {
			log.Printf("[SHELL] Error creating exec session: %v", err)
			return
		}
		resp, err := cli.ContainerExecAttach(context.Background(), exec.ID, container.ExecStartOptions{Tty: true})
		if err != nil {
			log.Printf("[SHELL] Error attaching to exec session: %v", err)
			return
		}
		defer resp.Close()

		log.Printf("[SHELL] Shell session established for stack '%s'", name)

		go func() {
			for {
				_, msg, err := conn.ReadMessage()
				if err != nil {
					log.Printf("[SHELL] WebSocket read error: %v", err)
					return
				}
				resp.Conn.Write(msg)
			}
		}()

		buf := make([]byte, 4096)
		for {
			n, err := resp.Reader.Read(buf)
			if err != nil {
				log.Printf("[SHELL] Shell session ended for stack '%s': %v", name, err)
				return
			}
			conn.WriteMessage(websocket.TextMessage, buf[:n])
		}
	}
}
