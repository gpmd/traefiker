package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/viper"

	"github.com/docker/docker/client"
)

// AuthInfo stores a users' docker registry/hub info
var AuthInfo sync.Map

var running = map[string]string{}

// E is a generic error handler
func E(err error) {
	if err != nil {
		panic(err)
	}
}

func main() {

	log.Println("Reading configuration...")
	viper.SetConfigName("config")
	viper.AddConfigPath(".")    // optionally look for config in the working directory
	err := viper.ReadInConfig() // Find and read the config file
	if err != nil {             // Handle errors reading the config file
		panic(fmt.Errorf("fatal error config file: %v", err))
	}
	conf := viper.GetStringMapString("traefiker")
	dockerconf := viper.GetStringMapStringSlice("docker")
	if conf["network"] != "" && len(dockerconf["networks"]) == 0 {
		dockerconf["networks"] = []string{conf["network"]}
	}
	labelconf := viper.GetStringMapString("labels")
	// hotfix for entryPoints
	for k, v := range labelconf {
		if strings.Contains(k, "entrypoints") {
			k2 := strings.Replace(k, "entrypoints", "entryPoints", -1)
			delete(labelconf, k)
			labelconf[k2] = v
		}
	}

	log.Println("Connecting to docker...")

	cli, err := client.NewEnvClient()
	E(err)
	ctx := context.Background()
	d := Docker{cli: cli}

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "server":
			server(ctx, d, conf)
		case "traefik":
			traefik(ctx, d, dockerconf)
		}
		return
	}

	old := map[string]string{}
	for _, s := range d.List() {
		if s.Image == conf["name"] || strings.HasPrefix(s.Names[0], "/"+conf["name"]+"_") {
			old[s.ID] = s.ID
		}
		log.Println(s.Image)
		running[s.Image] = s.Names[0]
	}

	log.Println("Creating docker container...")

	name, err := BuildDockerImage(ctx, conf, d.cli)
	E(err)

	d.Run(ctx, name, "", labelconf, dockerconf)

	time.Sleep(2 * time.Second)

	newid := ""
	for _, s := range d.List() {
		if _, ok := old[s.ID]; !ok {
			if s.Image == name {
				newid = s.ID
			}
		}
	}

	if newid == "" {
		log.Println("Unsuccessful build, container is not running")
		os.Exit(-1)
	}

	for _, id := range old {
		log.Println("Stopping", id)
		d.StopContainer(ctx, id)
	}

}
