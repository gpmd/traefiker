package main

import (
	"context"
	"flag"
	"io/ioutil"
	"log"
	"os"
	"strings"

	"github.com/gpmd/filehelper"
	"github.com/shoobyban/slog"
)

type arrayFlags []string

func (i *arrayFlags) String() string {
	return strings.Join(*i, ", ")
}

func (i *arrayFlags) Set(value string) error {
	log.Printf("here\n")
	*i = append(*i, value)
	return nil
}

func traefik(ctx context.Context, d Docker, dockerconf map[string][]string) {
	fs := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	var ports arrayFlags
	fs.Var(&ports, "port", "port the service will listen on, can have multiple entries like -p 80 -p 8081")
	var tlsports arrayFlags
	fs.Var(&tlsports, "tlsport", "https port the service will listen on, can have multiple entries like -p 443 -p 8443")
	var redir string
	fs.StringVar(&redir, "tlsredir", "", "tls redirect if necessary, format: 80:443")
	err := fs.Parse(os.Args[2:])
	if err != nil {
		panic(err)
	}

	t, err := filehelper.ProcessTemplateFile("traefik.toml.template", dockerconf)
	if err != nil {
		panic(err)
	}
	os.MkdirAll("server", 0755)
	err = ioutil.WriteFile("server/traefik.toml", t, 0755)
	if err != nil {
		panic(err)
	}
	var id string
	for _, s := range d.List() {
		if s.Image == "traefik:latest" || strings.HasPrefix(s.Names[0], "/traefik_") {
			slog.Infof("names %v", s.Names)
			id = s.ID
			break
		}
	}
	if id != "" {
		d.StopContainer(ctx, id)
	}
	dockerconf["command"] = []string{
		"traefik",
		"--providers.docker=true",
		"--global.checknewversion=false",
		"--global.sendanonymoususage=false",
		"--log.level=DEBUG",
		"--api=true",
		"--api.insecure=true",
	}
	dockerconf["networks"] = append(dockerconf["networks"], "traefik")
	dockerconf["mounts"] = append(dockerconf["mounts"], "/var/run/docker.sock:/var/run/docker.sock")
	dockerconf["ports"] = append(dockerconf["ports"], "8080:8080")
	for _, p := range ports {
		dockerconf["ports"] = append(dockerconf["ports"], p+":"+p)
		dockerconf["command"] = append(dockerconf["command"], "--entrypoints.port"+p+".address=:"+p)
	}
	for _, p := range tlsports {
		dockerconf["ports"] = append(dockerconf["ports"], p+":"+p)
		dockerconf["command"] = append(dockerconf["command"], "--entrypoints.port"+p+".address=:"+p)
		dockerconf["command"] = append(dockerconf["command"], "--entrypoints.port"+p+".https=true")
	}
	if acme {
		dockerconf["command"] = append(dockerconf["command"], "--certificatesresolvers.myresolver.acme.email=your-email@example.com")
		--certificatesresolvers.myresolver.acme.storage=acme.json
		--certificatesresolvers.myresolver.acme.tlschallenge=true")
	}
	if redir != "" {
		parts := strings.Split(redir, ":")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			panic("invalid redir format")
		}
		found := false
		for _, p := range ports {
			if p == parts[0] {
				found = true
			}
		}
		if !found {
			panic("redir's source port is not defined with --port")
		}
		found = false
		for _, p := range tlsports {
			if p == parts[1] {
				found = true
			}
		}
		if !found {
			panic("redir's target port is not defined with --tlsport")
		}
		dockerconf["command"] = append(dockerconf["command"], "--entrypoints.web.port"+parts[0]+".redirections.entrypoint.to=port"+parts[1])
		dockerconf["command"] = append(dockerconf["command"], "--entrypoints.web.port"+parts[0]+".redirections.entrypoint.scheme=https")
	}
	slog.Infof("conf: %v", dockerconf)
	d.Run(ctx, "traefik:latest", "docker.io/library/traefik:latest", map[string]string{}, dockerconf)
}
