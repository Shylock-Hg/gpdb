package gpservice_config

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"

	"github.com/greenplum-db/gp-common-go-libs/gplog"
	"github.com/greenplum-db/gpdb/gpservice/idl"
	"github.com/greenplum-db/gpdb/gpservice/idl/mock_idl"
	"github.com/greenplum-db/gpdb/gpservice/pkg/greenplum"
	"github.com/greenplum-db/gpdb/gpservice/pkg/utils"
	"google.golang.org/grpc"
)

var (
	ConnectToHub           = connectToHubFunc
	copyConfigFileToAgents = copyConfigFileToAgentsFunc
)

type Config struct {
	HubPort       int      `json:"hubPort"`
	AgentPort     int      `json:"agentPort"`
	Hostnames     []string `json:"hostnames"`
	LogDir        string   `json:"hubLogDir"`
	ServiceName   string   `json:"serviceName"`
	GpHome        string   `json:"gphome"`
	DefaultConfig bool     `json:"defaultConfig"`

	Credentials utils.Credentials
}

func (conf *Config) Write(filepath string) error {
	file, err := utils.System.OpenFile(filepath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("could not create service config file %s: %w", filepath, err)
	}
	defer file.Close()

	contents, err := json.MarshalIndent(conf, "", "")
	if err != nil {
		return fmt.Errorf("could not create service config file %s: %w", filepath, err)
	}

	_, err = file.Write(contents)
	if err != nil {
		return fmt.Errorf("could not write to service config file %s: %w", filepath, err)
	}

	err = copyConfigFileToAgents(conf.Hostnames, filepath, conf.GpHome)
	if err != nil {
		return err
	}

	return nil
}

func (conf *Config) Remove(configFilepath string) error {
	gpsshCmd := &greenplum.GpSSH{
		Hostnames: conf.Hostnames,
		Command:   fmt.Sprintf("rm %s", configFilepath),
	}
	_, err := utils.RunGpSourcedCommand(gpsshCmd, conf.GpHome)
	if err != nil {
		return fmt.Errorf("failed to delete service configuration file %s: %w", configFilepath, err)
	}
	gplog.Info("Successfully deleted service configuration file %s", configFilepath)
	return nil
}

func Create(filepath string, hubPort, agentPort int, hostnames []string, logdir, serviceName, gphome string, creds utils.Credentials, defaultConfig bool) error {
	conf := &Config{
		HubPort:       hubPort,
		AgentPort:     agentPort,
		Hostnames:     hostnames,
		LogDir:        logdir,
		ServiceName:   serviceName,
		GpHome:        gphome,
		Credentials:   creds,
		DefaultConfig: defaultConfig,
	}

	return conf.Write(filepath)
}

func Read(filepath string) (*Config, error) {
	config := &Config{}
	config.Credentials = &utils.GpCredentials{}

	contents, err := utils.System.ReadFile(filepath)
	if err != nil {
		return nil, fmt.Errorf("could not open service config file %s: %w", filepath, err)
	}

	err = json.Unmarshal(contents, &config)
	if err != nil {
		return nil, fmt.Errorf("could not parse service config file %s: %w", filepath, err)
	}

	return config, nil
}

func copyConfigFileToAgentsFunc(hostnames []string, filepath, gpHome string) error {
	gpsyncCmd := &greenplum.GpSync{
		Hostnames:   hostnames,
		Source:      filepath,
		Destination: filepath,
	}

	out, err := utils.RunGpSourcedCommand(gpsyncCmd, gpHome)
	if err != nil {
		return fmt.Errorf("could not copy %s to segment hosts: %s, %w", filepath, out, err)
	}

	return nil
}

func connectToHubFunc(conf *Config) (idl.HubClient, error) {
	errPrefix := fmt.Sprintf("could not connect to hub on port %d", conf.HubPort)
	var opts []grpc.DialOption
	credentials, err := conf.Credentials.LoadClientCredentials()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errPrefix, err)
	}

	opts = append(opts, grpc.WithTransportCredentials(credentials))

	address := net.JoinHostPort("localhost", strconv.Itoa(conf.HubPort))
	conn, err := grpc.NewClient(address, opts...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", errPrefix, err)
	}

	return idl.NewHubClient(conn), nil
}

func SetConnectToHub(hubClient *mock_idl.MockHubClient) {
	ConnectToHub = func(conf *Config) (idl.HubClient, error) {
		return hubClient, nil
	}
}

func ResetConfigFunctions() {
	ConnectToHub = connectToHubFunc
	copyConfigFileToAgents = copyConfigFileToAgentsFunc
}

func SetCopyConfigFileToAgents() {
	copyConfigFileToAgents = func(hostnames []string, filepath, gpHome string) error {
		return nil
	}
}