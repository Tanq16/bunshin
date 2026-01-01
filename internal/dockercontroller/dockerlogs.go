package dockercontroller

import (
	"context"
	"log"
	"net/http"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/gorilla/websocket"
)

func (c *Controller) HandleLogs(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	containerID := r.URL.Query().Get("container")
	log.Printf("[LOGS] WebSocket connection requested for stack '%s', container '%s'", name, containerID)
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[LOGS] Error upgrading connection: %v", err)
		return
	}
	defer conn.Close()

	ctx := context.Background()
	f := filters.NewArgs()
	f.Add("label", "bunshin.stack="+name)
	containers, _ := c.cli.ContainerList(ctx, container.ListOptions{Filters: f})
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
	logs, err := c.cli.ContainerLogs(ctx, targetContainer.ID, container.LogsOptions{ShowStdout: true, ShowStderr: true, Follow: true, Tail: "200"})
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
