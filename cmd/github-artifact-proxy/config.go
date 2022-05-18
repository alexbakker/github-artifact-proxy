package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/go-github/v44/github"
	"gopkg.in/yaml.v3"
)

type Target struct {
	Token    *string `yaml:"token"`
	Owner    string  `yaml:"owner"`
	Repo     string  `yaml:"repo"`
	Filename string  `yaml:"filename"`
	Branch   *string `yaml:"branch"`
	Event    *string `yaml:"event"`
	Status   *string `yaml:"status"`

	lockChan           chan struct{}
	latestArtifact     *github.Artifact
	latestArtifactTime time.Time
}

type Webhook struct {
	Path   string `yaml:"path"`
	Secret string `yaml:"secret"`
}

type Config struct {
	Webhook *Webhook
	Tokens  map[string]string  `yaml:"tokens"`
	Targets map[string]*Target `yaml:"targets"`
}

func (t *Target) Lock(ctx context.Context) error {
	select {
	case t.lockChan <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *Target) Unlock() {
	<-t.lockChan
}

func LoadConfig(filename string) (*Config, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var config Config
	if err := yaml.NewDecoder(file).Decode(&config); err != nil {
		return nil, err
	}

	for id, target := range config.Targets {
		target.lockChan = make(chan struct{}, 1)

		if target.Token == nil {
			return nil, fmt.Errorf("target '%s' requires an API token", id)
		}

		if _, ok := config.Tokens[*target.Token]; !ok {
			return nil, fmt.Errorf("token with id '%s' not found in tokens list", *target.Token)
		}
	}

	return &config, err
}
