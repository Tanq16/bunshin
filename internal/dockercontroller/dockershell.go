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

func (c *Controller) HandleShell(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	containerID := r.URL.Query().Get("container")
	log.Printf("[SHELL] WebSocket connection requested for stack '%s', container '%s'", name, containerID)
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[SHELL] Error upgrading connection: %v", err)
		return
	}
	defer conn.Close()

	ctx := context.Background()
	f := filters.NewArgs()
	f.Add("label", "bunshin.stack="+name)
	containers, _ := c.cli.ContainerList(ctx, container.ListOptions{Filters: f})
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
	exec, err := c.cli.ContainerExecCreate(ctx, targetContainer.ID, container.ExecOptions{
		AttachStdin: true, AttachStdout: true, AttachStderr: true, Tty: true, Cmd: []string{"/bin/sh"},
	})
	if err != nil {
		log.Printf("[SHELL] Error creating exec session: %v", err)
		return
	}
	resp, err := c.cli.ContainerExecAttach(ctx, exec.ID, container.ExecStartOptions{Tty: true})
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
