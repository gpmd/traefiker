package main

import (
	"context"
	"io/ioutil"
	"os"
	"strings"

	"github.com/gpmd/filehelper"
	"github.com/shoobyban/slog"
)

func traefik(ctx context.Context, d Docker, dockerconf map[string][]string) {
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
	os.Chdir("server")
	d.Run(ctx, "traefik:latest", "docker.io/library/traefik:latest", map[string]string{}, dockerconf)
}
