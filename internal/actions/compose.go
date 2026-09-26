package actions

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

func mapValue(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func scalar(v string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v} }

// unsetEnv, returned by setComposeEnv's edit, removes the variable.
const unsetEnv = "\x00unset"

// setComposeEnv changes one environment variable of a service in a
// docker-compose file, keeping everything else. edit gets the current
// value ("" when unset) and returns the new one. When service is empty or
// not found and the file has a single service, that service is used.
func setComposeEnv(compose, service, key string, edit func(old string) string) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(compose), &doc); err != nil {
		return "", fmt.Errorf("docker-compose 配置格式不对：%v", err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return "", errors.New("docker-compose 配置格式不对")
	}
	services := mapValue(doc.Content[0], "services")
	if services == nil || services.Kind != yaml.MappingNode {
		return "", errors.New("docker-compose 配置里没有 services")
	}
	var svc *yaml.Node
	if service != "" {
		svc = mapValue(services, service)
	}
	if svc == nil {
		if len(services.Content) != 2 {
			return "", fmt.Errorf("docker-compose 配置里找不到服务 %s", service)
		}
		svc = services.Content[1]
	}
	if svc.Kind != yaml.MappingNode {
		return "", errors.New("docker-compose 配置格式不对")
	}
	env := mapValue(svc, "environment")
	if env == nil {
		env = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		svc.Content = append(svc.Content, scalar("environment"), env)
	} else if env.Kind == yaml.ScalarNode && env.Tag == "!!null" {
		*env = yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	}
	switch env.Kind {
	case yaml.MappingNode:
		found := false
		for i := 0; i+1 < len(env.Content); i += 2 {
			if env.Content[i].Value == key {
				found = true
				if nv := edit(env.Content[i+1].Value); nv == unsetEnv {
					env.Content = append(env.Content[:i], env.Content[i+2:]...)
				} else {
					*env.Content[i+1] = *scalar(nv)
				}
				break
			}
		}
		if nv := ""; !found {
			if nv = edit(""); nv != unsetEnv {
				env.Content = append(env.Content, scalar(key), scalar(nv))
			}
		}
	case yaml.SequenceNode:
		found := false
		kept := env.Content[:0]
		for _, item := range env.Content {
			if k, v, _ := strings.Cut(item.Value, "="); k == key {
				found = true
				nv := edit(v)
				if nv == unsetEnv {
					continue
				}
				*item = *scalar(key + "=" + nv)
			}
			kept = append(kept, item)
		}
		env.Content = kept
		if nv := ""; !found {
			if nv = edit(""); nv != unsetEnv {
				env.Content = append(env.Content, scalar(key+"="+nv))
			}
		}
	default:
		return "", errors.New("docker-compose 配置里 environment 的格式不对")
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}
