package stackmanager

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/docker/client"
)

type Manager struct {
	dataPath string
	envMgr   EnvManager
}

type EnvManager interface {
	GetEnvMap(name string) map[string]string
	ReadEnv(name string) (string, error)
	WriteEnv(name string, envContent string) error
}

func New(dataPath string, envMgr EnvManager) *Manager {
	return &Manager{
		dataPath: dataPath,
		envMgr:   envMgr,
	}
}

func (m *Manager) ListStacks() []string {
	files, _ := os.ReadDir(filepath.Join(m.dataPath, "stacks"))
	stacks := []string{}
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".yml") {
			stacks = append(stacks, strings.TrimSuffix(f.Name(), ".yml"))
		}
	}
	return stacks
}

func (m *Manager) GetStack(name string) (string, string, error) {
	yml, err := os.ReadFile(filepath.Join(m.dataPath, "stacks", name+".yml"))
	if err != nil {
		return "", "", err
	}
	var envStr string
	envStr, _ = m.envMgr.ReadEnv(name)
	return string(yml), envStr, nil
}

func (m *Manager) SaveStack(name string, yaml string, env string) error {
	if err := os.WriteFile(filepath.Join(m.dataPath, "stacks", name+".yml"), []byte(yaml), 0644); err != nil {
		return err
	}
	return m.envMgr.WriteEnv(name, env)
}

func (m *Manager) LoadProject(ctx context.Context, name string) (*types.Project, error) {
	ymlData, err := os.ReadFile(filepath.Join(m.dataPath, "stacks", name+".yml"))
	if err != nil {
		return nil, err
	}
	envMap := m.envMgr.GetEnvMap(name)
	project, err := loader.LoadWithContext(ctx, types.ConfigDetails{
		WorkingDir: ".",
		ConfigFiles: []types.ConfigFile{
			{Filename: name + ".yml", Content: ymlData},
		},
		Environment: envMap,
	}, func(opts *loader.Options) {
		opts.SetProjectName(name, true)
	})
	if err != nil {
		return nil, err
	}
	return project, nil
}

func FindService(project *types.Project, serviceName string) *types.ServiceConfig {
	for i := range project.Services {
		if project.Services[i].Name == serviceName {
			svc := project.Services[i]
			return &svc
		}
	}
	return nil
}

func SortServicesByDependencies(services types.Services) []types.ServiceConfig {
	visited := make(map[string]bool)
	recStack := make(map[string]bool)
	result := make([]types.ServiceConfig, 0, len(services))
	var visit func(name string) bool
	visit = func(name string) bool {
		if recStack[name] {
			log.Printf("[WARN] Circular dependency detected involving service '%s'", name)
			return false
		}
		if visited[name] {
			return true
		}
		svc, exists := services[name]
		if !exists {
			return true
		}
		visited[name] = true
		recStack[name] = true
		for depName := range svc.DependsOn {
			visit(depName)
		}
		recStack[name] = false
		result = append(result, svc)
		return true
	}
	for name := range services {
		visit(name)
	}
	if len(result) > 0 {
		serviceNames := make([]string, 0, len(result))
		for _, svc := range result {
			serviceNames = append(serviceNames, svc.Name)
		}
		log.Printf("[SORT] Services sorted by dependencies: %v", serviceNames)
	}
	return result
}

func WaitForDependencies(ctx context.Context, cli *client.Client, project *types.Project, stackName string, svc types.ServiceConfig) error {
	if len(svc.DependsOn) == 0 {
		return nil
	}
	log.Printf("[WAIT] Service '%s' waiting for dependencies", svc.Name)

	for depName, condition := range svc.DependsOn {
		depService := FindService(project, depName)
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
