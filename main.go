package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/client"
	"github.com/spf13/viper"
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
	log.Println("Version 0.1.2")
	dockerconf := map[string][]string{}
	conf := map[string]string{}
	labelconf := map[string]string{}

	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	var mode string
	fs.StringVar(&mode, "mode", "docker", "Mode: docker (default), static")
	err := fs.Parse(os.Args[1:])
	if err != nil {
		panic(err)
	}
	//	log.Printf("Flags: %s %v", mode, fs)
	env := os.Environ()
	for _, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			log.Printf("Env: %s", e)
		}
	}

	var d Docker
	ctx := context.Background()

	switch mode {
	case "docker":
		cli, err := client.NewEnvClient()
		E(err)
		d = Docker{mode: ModeDocker, cli: cli}
	case "static":
		d = Docker{mode: ModeStatic}
	}

	if len(fs.Args()) > 0 {
		switch fs.Args()[0] {
		case "start":
			traefik(ctx, d, dockerconf)
		default:
			log.Printf("can't process %v", fs.Args())
		}
		return
	}
	// deployment command (with docker) is default (legacy systems)
	log.Println("Reading configuration...")
	viper.SetConfigName("config")
	viper.AddConfigPath(".")   // optionally look for config in the working directory
	err = viper.ReadInConfig() // Find and read the config file
	if err != nil {            // Handle errors reading the config file
		panic(fmt.Errorf("fatal error config file: %v", err))
	}
	conf = viper.GetStringMapString("traefiker")
	dockerconf = viper.GetStringMapStringSlice("docker")
	if conf["network"] != "" && len(dockerconf["networks"]) == 0 {
		dockerconf["networks"] = []string{conf["network"]}
	}
	labelconf = viper.GetStringMapString("labels")
	//	hotfix for entryPoints

	for k, v := range labelconf {
		if strings.Contains(k, "entrypoints") {
			k2 := strings.Replace(k, "entrypoints", "entryPoints", -1)
			delete(labelconf, k)
			labelconf[k2] = v
		}
	}

	log.Println("Connecting to docker...")

	old := map[string]string{}
	for _, s := range d.List() {
		if s.Image == conf["name"] || strings.HasPrefix(s.Names[0], "/"+conf["name"]+"_") {
			old[s.ID] = s.ID
		}
		log.Println(s.Image)
		running[s.Image] = s.Names[0]
	}

	log.Println("Creating docker container...")

	name, err := d.BuildDockerImage(ctx, conf)
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
