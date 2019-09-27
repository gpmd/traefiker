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
	"strconv"
	"strings"
	"sync"
	"time"

	"path/filepath"

	"github.com/spf13/viper"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// AuthInfo stores a users' docker registry/hub info
var AuthInfo sync.Map

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

	log.Println("Connecting to docker...")

	cli, err := client.NewEnvClient()
	E(err)
	ctx := context.Background()
	d := Docker{cli: cli}

	old := map[string]string{}
	for _, s := range d.List() {
		if s.Image == conf["name"] {
			old[s.ID] = s.ID
		}
		log.Println(s.Image)
	}

	log.Println("Creating docker container...")

	name, err := BuildDockerImage(ctx, conf, d.cli)
	E(err)

	d.Run(ctx, name, "", labelconf, dockerconf)
	newid := ""
	for _, s := range d.List() {
		if _, ok := old[s.ID]; !ok {
			if s.Image == name {
				newid = s.ID
			}
		}
	}
	if newid != "" {
		for _, id := range old {
			log.Println("Stopping", id)
			d.StopContainer(ctx, id)
		}
	}
}

// Docker is base struct for the app
type Docker struct {
	cli  *client.Client
	list []types.Container
}

// E is a generic error handler
func E(err error) {
	if err != nil {
		panic(err)
	}
}

// Run starts the created container
func (d *Docker) Run(ctx context.Context, imagename, imageurl string, labels map[string]string, conf map[string][]string) string {

	if imageurl != "" {
		reader, err := d.cli.ImagePull(ctx, imageurl, types.ImagePullOptions{})
		if err != nil {
			panic(err)
		}
		io.Copy(os.Stdout, reader)
	}

	nets, err := d.cli.NetworkList(ctx, types.NetworkListOptions{})
	netid := ""
	for _, n := range nets {
		if n.Name == conf["networks"][0] {
			netid = n.ID
		}
	}

	mm := []mount.Mount{}

	for _, l := range conf["mounts"] {
		ll := strings.Split(l, ":")
		if len(ll) != 2 {
			log.Panicf("Mounts in config.yml (%v) have line %s where 'from:to' is not correct", conf["mounts"], l)
		}
		mm = append(mm, mount.Mount{
			Type:   mount.TypeBind,
			Source: ll[0],
			Target: ll[1],
		})
	}

	hostconfig := &container.HostConfig{
		NetworkMode:   container.NetworkMode(conf["networks"][0]),
		RestartPolicy: container.RestartPolicy{MaximumRetryCount: 0},
		Mounts:        mm,
	}

	if len(conf["ports"]) > 0 {
		//hostBinding := nat.PortBinding{
		// 	HostIP:   "0.0.0.0",
		// 	HostPort: "8000",
		// }
		//	containerPort, err := nat.NewPort("tcp", "80")
		// if err != nil {
		// 	panic("Unable to get the port")
		// }
		// hostconfig.PortBindings = nat.PortMap{containerPort: []nat.PortBinding{hostBinding}}
	}
	cont, err := d.cli.ContainerCreate(
		context.Background(),
		&container.Config{
			Image:  imagename,
			Labels: labels,
		},
		hostconfig,
		nil,
		imagename+"_"+strconv.FormatInt(time.Now().UTC().Unix(), 32))
	E(err)
	d.cli.ContainerStart(ctx, cont.ID, types.ContainerStartOptions{})
	d.cli.NetworkConnect(ctx,
		netid,
		cont.ID,
		&network.EndpointSettings{
			Links: conf["links"],
		},
	)
	fmt.Println("Links:", conf["links"])
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

// Kill running container by container ID
func (d *Docker) StopContainer(ctx context.Context, containerID string) {
	err := d.cli.ContainerStop(ctx, containerID, nil)
	E(err)
}

// APIClient is meli's client to interact with the docker daemon server
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
		log.Println("Walking in ", path)
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
			"Build",
			imgProg.Status,
			imgProg.Progress,
			imgProg.Stream)
	}
	if err := scanner.Err(); err != nil {
		fmt.Println(" :unable to log output for image", imageName, err)
	}

	imageBuildResponse.Body.Close()
	log.Println("Build successful")
	return imageName, nil
}
