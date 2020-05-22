package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/strslice"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/gpmd/filehelper"
)

// Docker is base struct for the app
type Docker struct {
	cli  *client.Client
	list []types.Container
}

// Run starts the created container
func (d *Docker) Run(ctx context.Context, image, imageurl string, labels map[string]string, conf map[string][]string) string {

	if imageurl != "" {
		reader, err := d.cli.ImagePull(ctx, imageurl, types.ImagePullOptions{})
		if err != nil {
			panic(err)
		}
		io.Copy(os.Stdout, reader)
	}

	imagename := image
	if strings.Contains(imagename, ":") {
		parts := strings.Split(imagename, ":")
		imagename = parts[0]
	}

	// nets, err := d.cli.NetworkList(ctx, types.NetworkListOptions{})
	// netid := ""
	// for _, n := range nets {
	// 	if n.Name == conf["networks"][0] {
	// 		netid = n.ID
	// 	}
	// }

	mm := []mount.Mount{}

	wd, err := os.Getwd()
	if err != nil {
		panic(err)
	}
	for _, l := range conf["mounts"] {
		ll := strings.Split(l, ":")
		if len(ll) != 2 {
			log.Panicf("Mounts in config.yml (%v) have line %s where 'from:to' is not correct", conf["mounts"], l)
		}
		if strings.HasPrefix(ll[0], "./") {
			ll[0] = wd + strings.TrimPrefix(ll[0], ".")
		}
		mm = append(mm, mount.Mount{
			Type:   mount.TypeBind,
			Source: ll[0],
			Target: ll[1],
		})
	}

	hostconfig := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{MaximumRetryCount: 0},
		Mounts:        mm,
	}

	var nc *network.NetworkingConfig

	type emptyStruct struct{}

	portsMap := make(map[nat.Port]struct{})
	m := make(map[nat.Port][]nat.PortBinding)

	if len(conf["ports"]) > 0 {
		for _, v := range conf["ports"] {
			parts := strings.Split(v, ":")
			hostBinding := nat.PortBinding{
				HostIP:   "0.0.0.0",
				HostPort: parts[0],
			}
			containerPort, err := nat.NewPort("tcp", parts[1])
			if err != nil {
				panic("Unable to get the port")
			}
			portsMap[containerPort] = emptyStruct{}
			m[containerPort] = []nat.PortBinding{hostBinding}
		}
		hostconfig.PortBindings = m
	}

	links := []string{}
	for _, l := range conf["links"] {
		l2, err := filehelper.Template(l, running)
		if err != nil {
			log.Fatalf("Error in parsing link templates: %v", err)
		}
		links = append(links, l2)
	}

	if len(conf["networks"]) > 0 {
		hostconfig.NetworkMode = container.NetworkMode(conf["networks"][0])
		nc = &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				conf["networks"][0]: &network.EndpointSettings{
					Links:   links,
					Aliases: []string{imagename},
				},
			},
		}
	}
	cfg := container.Config{
		Hostname:     imagename + ".docker.localhost",
		Image:        image,
		Labels:       labels,
		ExposedPorts: portsMap,
	}
	if len(conf["command"]) > 0 {
		cfg.Cmd = strslice.StrSlice(conf["command"])
	}
	cont, err := d.cli.ContainerCreate(
		context.Background(),
		&cfg,
		hostconfig,
		nc,
		imagename+"_"+strconv.FormatInt(time.Now().UTC().Unix(), 32))
	E(err)
	err = d.cli.ContainerStart(ctx, cont.ID, types.ContainerStartOptions{})
	E(err)
	return cont.ID
}

// List lists all containers
func (d *Docker) List() []types.Container {
	containers, err := d.cli.ContainerList(context.Background(), types.ContainerListOptions{})
	E(err)
	d.list = []types.Container{}
	for _, container := range containers {
		d.list = append(d.list, container)
	}
	return d.list
}

// StopContainer stops container by container ID
func (d *Docker) StopContainer(ctx context.Context, containerID string) {
	err := d.cli.ContainerStop(ctx, containerID, nil)
	E(err)
}

// APIClient is meli's client interface to interact with the docker daemon server
type APIClient interface {
	// we implement this interface so that we can be able to mock it in tests
	// https://medium.com/@zach_4342/dependency-injection-in-golang-e587c69478a8
	ImagePull(ctx context.Context, ref string, options types.ImagePullOptions) (io.ReadCloser, error)
	ImageBuild(ctx context.Context, buildContext io.Reader, options types.ImageBuildOptions) (types.ImageBuildResponse, error)
	ContainerCreate(ctx context.Context, config *container.Config, hostConfig *container.HostConfig, networkingConfig *network.NetworkingConfig, containerName string) (container.ContainerCreateCreatedBody, error)
	ContainerStart(ctx context.Context, containerID string, options types.ContainerStartOptions) error
	ContainerLogs(ctx context.Context, container string, options types.ContainerLogsOptions) (io.ReadCloser, error)
	NetworkList(ctx context.Context, options types.NetworkListOptions) ([]types.NetworkResource, error)
	NetworkCreate(ctx context.Context, name string, options types.NetworkCreate) (types.NetworkCreateResponse, error)
	NetworkConnect(ctx context.Context, networkID, containerID string, config *network.EndpointSettings) error
	ContainerList(ctx context.Context, options types.ContainerListOptions) ([]types.Container, error)
	ContainerRemove(ctx context.Context, containerID string, options types.ContainerRemoveOptions) error
}

type imageProgress struct {
	Status         string `json:"status,omitempty"`
	Stream         string `json:"stream,omitempty"`
	Progress       string `json:"progress,omitempty"`
	ProgressDetail string `json:"progressDetail,omitempty"`
}

// this is taken from io.util
var blackHolePool = sync.Pool{
	New: func() interface{} {
		// TODO: change this size accordingly
		// we could find the size of the file we want to tar
		// then pass that in as the size. That way we will
		// always create a right sized slice and not have to incure cost of slice regrowth(if any)
		b := make([]byte, 512)
		return &b
	},
}

// this is taken from io.util
func poolReadFrom(r io.Reader) (n int64, err error) {
	bufp := blackHolePool.Get().(*[]byte)
	// reset the buffer since it may contain data from a previous round
	// see issues/118
	for i := range *bufp {
		(*bufp)[i] = 0

	}
	readSize := 0
	for {
		readSize, err = r.Read(*bufp)
		n += int64(readSize)
		if err != nil {
			blackHolePool.Put(bufp)
			if err == io.EOF {
				return n, nil
			}
			return
		}
	}
}

func walkFnClosure(src string, tw *tar.Writer, buf *bytes.Buffer) filepath.WalkFunc {
	return func(path string, info os.FileInfo, err error) error {
		// log.Println("Walking in ", path)
		if err != nil {
			// todo: maybe we should return nil
			return err
		}

		tarHeader, err := tar.FileInfoHeader(info, info.Name())
		if err != nil {
			return err
		}
		// update the name to correctly reflect the desired destination when untaring
		// https://medium.com/@skdomino/taring-untaring-files-in-go-6b07cf56bc07
		tarHeader.Name = strings.TrimPrefix(strings.Replace(path, src, "", -1), string(filepath.Separator))
		if src == "." {
			// see: issues/74
			tarHeader.Name = strings.TrimPrefix(path, string(filepath.Separator))
		}

		err = tw.WriteHeader(tarHeader)
		if err != nil {
			return err
		}
		// return on directories since there will be no content to tar
		if info.Mode().IsDir() {
			return nil
		}
		// return on non-regular files since there will be no content to tar
		if !info.Mode().IsRegular() {
			// non regular files are like symlinks etc; https://golang.org/src/os/types.go?h=ModeSymlink#L49
			return nil
		}

		// open files for taring
		f, err := os.Open(path)
		if err != nil {
			log.Println("Error while tar creation for file", path, err)
			return err
		}
		defer f.Close()

		tr := io.TeeReader(f, tw)
		_, err = poolReadFrom(tr)
		if err != nil {
			return err
		}

		return nil
	}
}

// BuildDockerImage builds a docker container using `conf`
func BuildDockerImage(ctx context.Context, conf map[string]string, cli APIClient) (string, error) {
	dockerFilePath := "./Dockerfile"

	dockerFileReader, err := os.Open(dockerFilePath)
	if err != nil {
		return "", fmt.Errorf("unable to open Dockerfile %v: %v", dockerFilePath, err)
	}
	readDockerFile, err := ioutil.ReadAll(dockerFileReader)
	if err != nil {
		return "", fmt.Errorf("unable to read dockerfile %v: %v", dockerFilePath, err)
	}

	imageName := conf["name"]

	splitDockerfile := strings.Split(string(readDockerFile), " ")
	splitImageName := strings.Split(splitDockerfile[1], "\n")
	imgFromDockerfile := splitImageName[0]

	AuthConfigs := make(map[string]types.AuthConfig)

	result, ok := AuthInfo.Load("dockerhub")
	if strings.Contains(imgFromDockerfile, "quay") {
		result, ok = AuthInfo.Load("quay")
	}
	if ok {

		authInfo := result.(map[string]string)
		registryURL := authInfo["registryURL"]
		username := authInfo["username"]
		password := authInfo["password"]

		AuthConfigs[registryURL] = types.AuthConfig{Username: username, Password: password}
	} else {
		log.Println("Can't read auth info, skipping")
	}

	buf := new(bytes.Buffer)
	tw := tar.NewWriter(buf)
	defer tw.Close()
	/*
		Context is either a path to a directory containing a Dockerfile, or a url to a git repository.
		When the value supplied is a relative path, it is interpreted as relative to the location of the Compose file.
		This directory is also the build context that is sent to the Docker daemon.
		- https://docs.docker.com/compose/compose-file/#context
	*/
	UserProvidedContextPath := "."
	err = filepath.Walk(UserProvidedContextPath, walkFnClosure(UserProvidedContextPath, tw, buf))
	if err != nil {
		return "", fmt.Errorf("unable to walk user provided context path %v: %v", UserProvidedContextPath, err)
	}
	log.Println("Len:", len(buf.Bytes()))
	dockerFileTarReader := bytes.NewReader(buf.Bytes())
	log.Println("building...")
	imageBuildResponse, err := cli.ImageBuild(
		ctx,
		dockerFileTarReader,
		types.ImageBuildOptions{
			//PullParent:     true,
			//Squash:     true, currently only supported in experimental mode
			Tags:           []string{imageName},
			Remove:         true, //remove intermediary containers after build
			NoCache:        true,
			SuppressOutput: false,
			Dockerfile:     "./Dockerfile",
			Context:        dockerFileTarReader,
			AuthConfigs:    AuthConfigs})
	if err != nil {
		return "", fmt.Errorf("unable to build docker image '%v' for service '%v': %v", imageName, os.Args[1], err)
	}

	var imgProg imageProgress
	scanner := bufio.NewScanner(imageBuildResponse.Body)
	for scanner.Scan() {
		_ = json.Unmarshal(scanner.Bytes(), &imgProg)
		log.Println(
			"Build\033[33m",
			strings.TrimRight(imgProg.Status, "\n"),
			strings.TrimRight(imgProg.Progress, "\n"),
			strings.TrimRight(imgProg.Stream, "\n"),
			"\033[0m",
		)
	}
	if err := scanner.Err(); err != nil {
		log.Println(" :unable to log output for image", imageName, err)
		return "", fmt.Errorf("unable to build due logging error %v", err)
	}

	imageBuildResponse.Body.Close()
	return imageName, nil
}
