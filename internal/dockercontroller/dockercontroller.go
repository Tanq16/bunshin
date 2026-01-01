package dockercontroller

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/gorilla/websocket"
	"github.com/tanq16/bunshin/internal/stackmanager"
)

type Controller struct {
	cli *client.Client
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func New(cli *client.Client) *Controller {
	return &Controller{cli: cli}
}

func (c *Controller) ResolveNetworkName(ctx context.Context, networkName string) (string, error) {
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
		f := filters.NewArgs()
		f.Add("name", containerName)
		containers, err := c.cli.ContainerList(ctx, container.ListOptions{Filters: f, All: true})
		if err == nil && len(containers) > 0 {
			containerJSON, err := c.cli.ContainerInspect(ctx, containers[0].ID)
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
	networks, err := c.cli.NetworkList(ctx, network.ListOptions{})
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

func (c *Controller) GetStatus(name string) string {
	ctx := context.Background()
	f := filters.NewArgs()
	f.Add("label", "bunshin.stack="+name)
	containers, _ := c.cli.ContainerList(ctx, container.ListOptions{Filters: f})
	status := "Stopped"
	if len(containers) > 0 {
		status = "Operational"
	}
	return status
}

func (c *Controller) GetContainerInstanceNumber(ctx context.Context, stackName, serviceName string) int {
	f := filters.NewArgs()
	f.Add("label", "bunshin.stack="+stackName)
	f.Add("label", "bunshin.service="+serviceName)
	containers, _ := c.cli.ContainerList(ctx, container.ListOptions{Filters: f, All: true})
	return len(containers) + 1
}

func (c *Controller) StopStack(ctx context.Context, name string) error {
	log.Printf("[STOP] Stopping stack '%s'", name)
	f := filters.NewArgs()
	f.Add("label", "bunshin.stack="+name)
	containers, _ := c.cli.ContainerList(ctx, container.ListOptions{Filters: f, All: true})
	log.Printf("[STOP] Found %d container(s) to stop for stack '%s'", len(containers), name)
	for _, ctr := range containers {
		log.Printf("[STOP] Stopping container '%s' (ID: %s)", ctr.Names[0], ctr.ID[:12])
		if err := c.cli.ContainerStop(ctx, ctr.ID, container.StopOptions{}); err != nil {
			log.Printf("[STOP] Error stopping container '%s': %v", ctr.Names[0], err)
		}
		log.Printf("[STOP] Removing container '%s'", ctr.Names[0])
		if err := c.cli.ContainerRemove(ctx, ctr.ID, container.RemoveOptions{Force: true}); err != nil {
			log.Printf("[STOP] Error removing container '%s': %v", ctr.Names[0], err)
		} else {
			log.Printf("[STOP] Successfully removed container '%s'", ctr.Names[0])
		}
	}
	log.Printf("[STOP] Stack '%s' stopped successfully", name)
	return nil
}

func (c *Controller) StartStack(ctx context.Context, name string, project *types.Project, isUpdate bool) error {
	log.Printf("[START] Starting stack '%s' with %d service(s)", name, len(project.Services))
	for _, svc := range project.Services {
		log.Printf("[SERVICE] Processing service '%s' from stack '%s'", svc.Name, name)
		log.Printf("[SERVICE] Image: %s", svc.Image)
		if isUpdate {
			log.Printf("[UPDATE] Pulling latest image for '%s'", svc.Image)
			out, err := c.cli.ImagePull(ctx, svc.Image, image.PullOptions{})
			if err != nil {
				log.Printf("[UPDATE] Error pulling image '%s': %v", svc.Image, err)
			} else {
				io.Copy(io.Discard, out)
				out.Close()
				log.Printf("[UPDATE] Successfully pulled image '%s'", svc.Image)
			}
		}

		if err := stackmanager.WaitForDependencies(ctx, c.cli, project, name, svc); err != nil {
			log.Printf("[ERROR] Dependency check failed for '%s': %v", svc.Name, err)
			continue
		}
		cName := svc.ContainerName
		if cName == "" {
			instanceNum := c.GetContainerInstanceNumber(ctx, name, svc.Name)
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
				depService := stackmanager.FindService(project, after)
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
			resolvedNetwork, err := c.ResolveNetworkName(ctx, networkName)
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
		c.cli.ContainerRemove(ctx, cName, container.RemoveOptions{Force: true})

		log.Printf("[SERVICE] Creating container '%s'", cName)
		resp, err := c.cli.ContainerCreate(ctx, config, hostConfig, networkingConfig, nil, cName)
		if err != nil {
			log.Printf("[ERROR] Failed to create container '%s': %v", cName, err)
			continue
		}
		log.Printf("[SERVICE] Starting container '%s' (ID: %s)", cName, resp.ID[:12])
		if err := c.cli.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
			log.Printf("[ERROR] Failed to start container '%s': %v", cName, err)
		} else {
			log.Printf("[SERVICE] Successfully started container '%s'", cName)
		}
	}
	if isUpdate {
		log.Printf("[UPDATE] Pruning dangling images for stack '%s'", name)
		pf := filters.NewArgs()
		pf.Add("dangling", "true")
		pruneReport, err := c.cli.ImagesPrune(ctx, pf)
		if err != nil {
			log.Printf("[UPDATE] Error pruning images: %v", err)
		} else {
			log.Printf("[UPDATE] Reclaimed %d bytes from dangling images", pruneReport.SpaceReclaimed)
		}
	}
	return nil
}

func (c *Controller) ListContainers(name string) ([]ContainerInfo, error) {
	ctx := context.Background()
	f := filters.NewArgs()
	f.Add("label", "bunshin.stack="+name)
	containers, _ := c.cli.ContainerList(ctx, container.ListOptions{Filters: f})
	containerList := []ContainerInfo{}
	for _, container := range containers {
		containerName := strings.TrimPrefix(container.Names[0], "/")
		containerList = append(containerList, ContainerInfo{
			ID:   container.ID,
			Name: containerName,
		})
	}
	return containerList, nil
}

type ContainerInfo struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
