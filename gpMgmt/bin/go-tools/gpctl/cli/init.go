package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/greenplum-db/gpdb/gpservice/pkg/greenplum"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/greenplum-db/gp-common-go-libs/gplog"
	"github.com/greenplum-db/gpdb/gpservice/constants"
	"github.com/greenplum-db/gpdb/gpservice/idl"
	"github.com/greenplum-db/gpdb/gpservice/pkg/gpservice_config"
	"github.com/greenplum-db/gpdb/gpservice/pkg/gpservice_mgmt"
	"github.com/greenplum-db/gpdb/gpservice/pkg/utils"
)

type Locale struct {
	LcAll      string `mapstructure:"lc-all" json:"lc-all"`
	LcCollate  string `mapstructure:"lc-collate" json:"lc-collate"`
	LcCtype    string `mapstructure:"lc-ctype" json:"lc-ctype"`
	LcMessages string `mapstructure:"lc-messages" json:"lc-messages"`
	LcMonetary string `mapstructure:"lc-monetary" json:"lc-monetary"`
	LcNumeric  string `mapstructure:"lc-numeric" json:"lc-numeric"`
	LcTime     string `mapstructure:"lc-time" json:"lc-time"`
}

type Segment struct {
	Hostname      string `mapstructure:"hostname" json:"hostname"`
	Address       string `mapstructure:"address" json:"address"`
	Port          int    `mapstructure:"port" json:"port"`
	DataDirectory string `mapstructure:"data-directory" json:"data-directory" yaml:"data-directory" toml:"data-directory"`
}

type SegmentPair struct {
	Primary *Segment `mapstructure:"primary"`
	Mirror  *Segment `mapstructure:"mirror"`
}

type InitConfig struct {
	DbName            string            `mapstructure:"db-name"`
	Encoding          string            `mapstructure:"encoding"`
	HbaHostnames      bool              `mapstructure:"hba-hostnames"`
	DataChecksums     bool              `mapstructure:"data-checksums"`
	SuPassword        string            `mapstructure:"su-password"` //TODO set to default if not provided
	Locale            Locale            `mapstructure:"locale"`
	CommonConfig      map[string]string `mapstructure:"common-config"`
	CoordinatorConfig map[string]string `mapstructure:"coordinator-config"`
	SegmentConfig     map[string]string `mapstructure:"segment-config"`
	Coordinator       Segment           `mapstructure:"coordinator"`
	SegmentArray      []SegmentPair     `mapstructure:"segment-array"`

	//Expansion config parameters
	PrimaryBasePort        int      `mapstructure:"primary-base-port"`
	PrimaryDataDirectories []string `mapstructure:"primary-data-directories"`
	HostList               []string `mapstructure:"hostlist"`
	MirrorBasePort         int      `mapstructure:"mirror-base-port"`
	MirrorDataDirectories  []string `mapstructure:"mirror-data-directories"`
	MirroringType          string   `mapstructure:"mirroring-type"`
}

var (
	InitClusterService                   = InitClusterServiceFn
	LoadInputConfigToIdl                 = LoadInputConfigToIdlFn
	ValidateInputConfigAndSetDefaults    = ValidateInputConfigAndSetDefaultsFn
	CheckForDuplicatPortAndDataDirectory = CheckForDuplicatePortAndDataDirectoryFn
	ParseStreamResponse                  = ParseStreamResponseFn
	GetSystemLocale                      = GetSystemLocaleFn
	SetDefaultLocale                     = SetDefaultLocaleFn
	IsGpServicesEnabled                  = IsGpServicesEnabledFn
)

var (
	cliForceFlag   bool
	cliCleanFlag   bool
	CleanFilePath  string
	ContainsMirror bool
	HubClient      idl.HubClient
)

func initCmd() *cobra.Command {
	initCmd := &cobra.Command{
		Use:   "init <config-file>",
		Short: "Initializes a Greenplum Database system by using configuration parameters specified in a configuration file",
		RunE:  RunInitClusterCmd,
	}

	initCmd.Flags().BoolVar(&cliForceFlag, "force", false, "Create the database cluster by overwriting the data directories if they are not empty")
	initCmd.Flags().BoolVar(&cliCleanFlag, "clean", false, "Rollback the changes made due to a failed cluster initialization")

	return initCmd
}

// RunInitClusterCmd driving function gets called from cobra on gpctl init command
func RunInitClusterCmd(cmd *cobra.Command, args []string) error {

	clusterCleanupFile := filepath.Join(greenplum.GetDefaultHubLogDir(), constants.CleanFileName)
	// cli validation
	if cliCleanFlag {
		if len(args) == 1 {
			return fmt.Errorf("cannot provide config file with --clean")
		}

		if cliCleanFlag && cliForceFlag {
			return fmt.Errorf("cannot use --clean and --force together")
		}

		_, err := utils.System.Stat(clusterCleanupFile)
		if err != nil {
			return fmt.Errorf("cluster is clean, no cleanup file present")
		}

	} else {
		if len(args) == 0 {
			return fmt.Errorf("please provide config file for cluster initialization")
		}

		if len(args) > 1 {
			return fmt.Errorf("more arguments than expected")
		}
	}

	defer stopAndDeleteService(!IsGpserviceRunning, !IsConfigured)
	var hostnames []string
	var err error

	if !IsConfigured {
		if cliCleanFlag {
			hostnames, err = GetHostListFromCleanUpFile(clusterCleanupFile)
			if len(hostnames) == 0 || err != nil {
				return fmt.Errorf("failed to get hostnames from cleanup file : %s, err: %s", clusterCleanupFile, err)
			}
		} else {
			hostlist, err := GetHostlistFromConfig(args[0])
			if err != nil {
				return fmt.Errorf("failed to get hostlist from cluster config file : %s, err: %s", args[0], err)
			}
			// get unique hostnames from host list
			hostnames, err = GetHostnamesFromHosts(hostlist)
			if len(hostnames) == 0 || err != nil {
				return fmt.Errorf("failed to get hostnames: %s, err: %s", args[0], err)
			}
		}

		err = gpservice_mgmt.InitialiseGpService(ConfigFilePath, constants.DefaultHubPort, constants.DefaultAgentPort,
			hostnames, greenplum.GetDefaultHubLogDir(), constants.DefaultServiceName,
			os.Getenv("GPHOME"), "", "", "", true, true)

		if err != nil {
			return err
		}

		Conf, err = gpservice_config.Read(ConfigFilePath)
		if err != nil {
			return err
		}
	}

	if !IsConfigured || !IsGpserviceRunning {
		err = gpservice_mgmt.StartServices(Conf)
		if err != nil {
			return err
		}
	}

	if cliCleanFlag {
		return InitClean(false)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ctrl := NewStreamController()

	SetSignalHandler(ctrl)

	go func() {
		ticker := time.NewTicker(constants.CheckInterruptFrequency)
		defer ticker.Stop()

		for ; true; <-ticker.C {
			if TerminationRequested {
				cancel()
				return
			}
		}
	}()

	lockDir, err := utils.NewLockDir(cmd)
	if err != nil {
		return err
	}
	defer func() {
		err := lockDir.Remove()
		if err != nil {
			gplog.Error("failed to remove lock directory err %v", err)
		}

	}()

	err = InitClusterService(args[0], ctx, ctrl, cliForceFlag, verbose)
	if err != nil {
		return err
	}

	return nil
}

func IsGPServiceIsRunning(Conf *gpservice_config.Config) bool {
	client, err := gpservice_config.ConnectToHub(Conf)
	if err != nil {
		return false
	} else {
		_, err = client.ReportAgentHealth(context.Background(), &idl.ReportAgentHealthRequest{})
		if err != nil {
			return false
		}
	}
	return true
}

func stopAndDeleteService(toStop, toDelete bool) {
	//check if service is configured avoid in between exit
	if Conf == nil {
		gplog.Warn("gpservice not yet configured, nothing to be done.")
		return
	}
	if toDelete {
		err := gpservice_mgmt.DeleteServices(Conf, ConfigFilePath)
		if err != nil {
			gplog.Error("failed to stop and delete services: %v", err)
			return
		}
	} else if toStop {
		err := gpservice_mgmt.StopServices(Conf)
		if err != nil {
			gplog.Error("failed to stop services: %v", err)
			return
		}
	}
}

func InitClean(prompt bool) error {
	if prompt {
		input := utils.AskUserYesOrNo("Continue with the rollback?")
		if !input {
			gplog.Info("Exiting without rollback")
			gplog.Info("Please run gpctl init --clean to rollback")
			return nil

		}
	}

	client, err := gpservice_config.ConnectToHub(Conf)
	if err != nil {
		return err
	}

	_, err = client.CleanInitCluster(context.Background(), &idl.CleanInitClusterRequest{})
	if err != nil {
		return fmt.Errorf("clean cluster command failed: %w", err)
	}

	gplog.Info("Successfully cleaned up the changes")
	return nil
}

/*
InitClusterServiceFn does input config file validation followed by actual cluster creation
*/
func InitClusterServiceFn(inputConfigFile string, ctx context.Context, ctrl *StreamController, force, verbose bool) (err error) {
	defer func() {
		if err != nil {
			if TerminationRequested {
				err = &ErrorUserTermination{}
			}

			// Call the cleanup routine only if the cleanup file exists
			fileName := filepath.Join(Conf.LogDir, constants.CleanFileName)
			_, statErr := utils.System.Stat(fileName)

			// do not display the prompt in case of SIGTERM
			displayPrompt := !(TerminationRequested && SigtermReceived)

			if statErr == nil {
				gplog.Error("failed to initialize the cluster: %v", err)
				err = InitClean(displayPrompt)
			}
		}
	}()

	_, err = utils.System.Stat(inputConfigFile)
	if err != nil {
		return err
	}

	// Viper instance to read the input config
	cliHandler := viper.New()

	HubClient, err = gpservice_config.ConnectToHub(Conf)
	if err != nil {
		return err
	}

	// Create the request based on the input configuration file
	clusterReq, err := LoadInputConfigToIdl(ctx, inputConfigFile, cliHandler, force, verbose)
	if err != nil {
		return err
	}

	// Validate the input configuration
	if err := ValidateInputConfigAndSetDefaults(clusterReq, cliHandler); err != nil {
		return err
	}

	// Call RPC on Hub to create the cluster
	stream, err := HubClient.MakeCluster(ctx, clusterReq)
	if err != nil {
		return utils.FormatGrpcError(err)
	}

	err = ParseStreamResponse(stream, ctrl)
	if err != nil {
		return err
	}

	if TerminationRequested {
		gplog.Info("Not able to stop the current execution as it has been completed")
	}

	gplog.Info("Cluster initialized successfully")
	return nil
}

/*
LoadInputConfigToIdlFn reads config file and populates RPC IDL request structure
*/
func LoadInputConfigToIdlFn(ctx context.Context, inputConfigFile string, cliHandler *viper.Viper, force bool, verbose bool) (*idl.MakeClusterRequest, error) {
	cliHandler.SetConfigFile(inputConfigFile)

	cliHandler.SetDefault("common-config", make(map[string]string))
	cliHandler.SetDefault("coordinator-config", make(map[string]string))
	cliHandler.SetDefault("segment-config", make(map[string]string))
	cliHandler.SetDefault("data-checksums", true)

	if err := cliHandler.ReadInConfig(); err != nil {
		return &idl.MakeClusterRequest{}, fmt.Errorf("while reading config file: %w", err)
	}

	var config InitConfig
	if err := cliHandler.UnmarshalExact(&config); err != nil {
		return &idl.MakeClusterRequest{}, fmt.Errorf("while unmarshaling config file: %w", err)
	}

	if AnyExpansionConfigPresent(cliHandler) {
		// Validate expansion config
		err := ValidateExpansionConfigAndSetDefault(&config, cliHandler)
		if err != nil {
			return &idl.MakeClusterRequest{}, err
		}

		// Check for multi-home
		isMultiHome, NameAddressMap, AddressNameMap, err := IsMultiHome(ctx, config.HostList)
		if err != nil {
			gplog.Error("multihome detection failed, error: %v", err)
			return &idl.MakeClusterRequest{}, err
		}

		if isMultiHome {
			isValidMultiHomeConfig, err := ValidateMultiHomeConfig(config, NameAddressMap)
			if !isValidMultiHomeConfig {
				return &idl.MakeClusterRequest{}, err
			}
		}

		//Expand details to config for primary
		segmentPairArray := ExpandSegPairArray(config, isMultiHome, NameAddressMap, AddressNameMap)
		config.SegmentArray = segmentPairArray
		// TODO to print expanded configuration here for user reference and print to file if required
	}

	return CreateMakeClusterReq(&config, force, verbose), nil
}

// ValidateMultiHomeConfig performs validation checks for multi-home environment
func ValidateMultiHomeConfig(config InitConfig, addressMap map[string][]string) (bool, error) {
	// Check if all the hosts in the host-list have same number of addresses
	var addressLengthList []int
	index := 0
	for _, addressList := range addressMap {
		addressLengthList = append(addressLengthList, len(addressList))
		if index > 0 {
			if addressLengthList[index] != addressLengthList[index-1] {
				return false, fmt.Errorf("multi-home validation failed, all hosts should have same number of interfaces/aliases")
			}
		}
		index++
	}
	// the number of directories per host is in multiple of number of addresses per host
	if (addressLengthList[0] < len(config.PrimaryDataDirectories)) && len(config.PrimaryDataDirectories)%(addressLengthList[0]) != 0 {
		return false, fmt.Errorf("multi-host setup must have data-directories in multiple of number of addresses or more. "+
			"Number of data-directories:%d and number of addresses per host: %d", len(config.PrimaryDataDirectories), addressLengthList[0])
	}

	if ContainsMirror {
		// Check if the total number of actual hosts is enough for spread mirroring
		if config.MirroringType == constants.SpreadMirroring && !(len(addressMap) > len(config.MirrorDataDirectories)) {
			return false, fmt.Errorf("to enable spread mirroring, number of hosts should be more than number of primary segments per host. "+
				"Current number of unique hosts is: %d and number of primaries per host is: %d", len(addressMap), len(config.MirrorDataDirectories))
		}
	}

	return true, nil
}

// IsMultiHome checks if it is a multi-home environment
// Makes a call to resolve all addresses to hostname and returns a map of address vs hostnames
// In case of error, map will be empty, and returns false
func IsMultiHome(ctx context.Context, hostlist []string) (isMultiHome bool, NameAddress map[string][]string, AddressNameMap map[string]string, err error) {
	// get a list of hostnames against each address
	request := idl.GetAllHostNamesRequest{HostList: hostlist}
	reply, err := HubClient.GetAllHostNames(ctx, &request)
	if err != nil {

		return false, nil, nil, utils.LogAndReturnError(fmt.Errorf("failed names of the host against address: %v", err))
	}
	isMultiHome = false
	NameAddressMap := make(map[string][]string)
	// convert address-name map to name-address map
	for address, hostname := range reply.HostNameMap {
		NameAddressMap[hostname] = append(NameAddressMap[hostname], address)
	}
	// Sort the address-list
	for hostname := range NameAddressMap {
		slices.Sort(NameAddressMap[hostname])
	}

	if len(hostlist) > len(NameAddressMap) {
		isMultiHome = true
	}

	return isMultiHome, NameAddressMap, reply.HostNameMap, nil
}

func AnyExpansionConfigPresent(cliHandle *viper.Viper) bool {
	expansionKeys := []string{"hostlist", "primary-base-port", "primary-data-directories", "mirroring-type", "mirror-base-port", "mirror-data-directories"}
	for _, key := range expansionKeys {
		if cliHandle.IsSet(key) {
			return true
		}
	}
	return false
}

func AnyExpansionMirrorConfigPresent(cliHandle *viper.Viper) bool {
	expansionKeys := []string{"mirroring-type", "mirror-base-port", "mirror-data-directories"}
	for _, key := range expansionKeys {
		if cliHandle.IsSet(key) {
			return true
		}
	}

	return false
}

// ValidateStringArray checks if there are no empty values in the array
// Returns false if any empty string in the array
func ValidateStringArray(input []string) bool {
	for _, entry := range input {
		if strings.TrimSpace(entry) == "" {
			return false
		}
	}
	return true
}

func ValidateExpansionConfigAndSetDefault(config *InitConfig, cliHandle *viper.Viper) error {
	// If provided expansion config and primary/mirrors array is also defined
	if cliHandle.IsSet("segment-array") && len(config.SegmentArray) > 0 {
		return fmt.Errorf("cannot specify segments-array and primary-base-directories together")
	}

	// Check if mandatory primary expansion parameters are provided
	if !cliHandle.IsSet("primary-data-directories") || len(config.PrimaryDataDirectories) < 1 {
		return fmt.Errorf("primary-data-directories not specified. Please specify primary-data-directories to continue")
	}

	if !ValidateStringArray(config.PrimaryDataDirectories) {
		return fmt.Errorf("empty primary-data-directories entry provided, please provide valid directory")
	}

	if !cliHandle.IsSet("hostlist") || len(config.HostList) < 1 {
		return fmt.Errorf("hostlist not specified. Please specify hostlist to continue")
	}

	if !ValidateStringArray(config.HostList) {
		return fmt.Errorf("empty hostlist entry detected, Please provide valid hostlist")
	}

	if !cliHandle.IsSet("primary-base-port") {
		defaultPrimaryBasePort := config.Coordinator.Port + 2
		gplog.Warn("primary-base-port value not specified. Setting default to: %d", defaultPrimaryBasePort)
		config.PrimaryBasePort = defaultPrimaryBasePort
	}

	if cliHandle.IsSet("primary-base-port") && config.PrimaryBasePort < 1 {
		return fmt.Errorf("invalid primary-base-port value provided: %d", config.PrimaryBasePort)
	}

	if config.PrimaryBasePort == config.Coordinator.Port {
		return fmt.Errorf("coordinator port and primary-base-port value cannot be same. Please provide different values")
	}

	// Check if mandatory mirror expansion parameters are provided
	if AnyExpansionMirrorConfigPresent(cliHandle) {
		ContainsMirror = true
		if len(config.PrimaryDataDirectories) != len(config.MirrorDataDirectories) {
			return fmt.Errorf("number of primary-data-directories should be equal to number of mirror-data-directories")
		}

		if !ValidateStringArray(config.MirrorDataDirectories) {
			return fmt.Errorf("empty mirror-data-directories entry provided, please provide valid directory")
		}

		// validate mirror base port
		if !cliHandle.IsSet("mirror-base-port") {
			defaultMirrorBasePort := config.PrimaryBasePort + 1000
			gplog.Warn("mirror-base-port value not specified. Setting default to: %d", defaultMirrorBasePort)
			config.MirrorBasePort = defaultMirrorBasePort
		}

		if config.MirrorBasePort < 1 {
			return fmt.Errorf("invalid mirror-base-port value provided: %d", config.MirrorBasePort)
		}

		if config.MirrorBasePort == config.Coordinator.Port {
			return fmt.Errorf("coordinator port and mirror-base-port value cannot be same. Please provide different values")
		}

		if config.MirrorBasePort == config.PrimaryBasePort {
			return fmt.Errorf("primary-base-port and mirror-base-port value cannot be same. Please provide different values")
		}

		//TODO Check if the primary and mirror range overlaps

		if !cliHandle.IsSet("mirroring-type") || config.MirroringType == "" {
			// Default is group mirroring
			config.MirroringType = constants.GroupMirroring
			gplog.Warn("Mirroring type not specified. Setting default as 'group' mirroring")
		} else {
			config.MirroringType = strings.ToLower(config.MirroringType)

			if config.MirroringType != constants.SpreadMirroring && config.MirroringType != constants.GroupMirroring {
				return fmt.Errorf("invalid mirroring-Type: %s. Valid options are 'group' and 'spread'", config.MirroringType)
			}
		}

		// Check if mirroring type is spread mirroring, the number of hosts should be greater than the number of primaries
		// per host so that we can spread segments
		if strings.ToLower(config.MirroringType) == constants.SpreadMirroring && !(len(config.MirrorDataDirectories) < len(config.HostList)) {
			strErr := fmt.Sprintf("To enable spread mirroring, number of hosts should be more than number of primary segments per host. "+
				"Current number of hosts is: %d and number of primaries per host is:%d", len(config.HostList), len(config.MirrorDataDirectories))
			return fmt.Errorf(strErr)
		}
	} else {
		// Mirror-less configuration
		gplog.Warn("No mirror-data-directories provided. Will create mirror-less cluster")
		ContainsMirror = false
	}

	return nil
}

func ExpandNonMultiHomePrimaryList(segPairList *[]SegmentPair, PrimaryBasePort int, PrimaryDataDirectories []string, hostList []string, addressNameMap map[string]string) *[]SegmentPair {
	segNum := 0
	for _, hostAddress := range hostList {
		for segIdx, directory := range PrimaryDataDirectories {
			seg := Segment{
				Hostname:      addressNameMap[hostAddress],
				Address:       hostAddress,
				Port:          PrimaryBasePort + segIdx,
				DataDirectory: filepath.Join(directory, fmt.Sprintf("%s%d", constants.DefaultSegName, segNum)),
			}
			*segPairList = append(*segPairList, SegmentPair{Primary: &seg})
			segNum++
		}
	}
	return segPairList
}
func ExpandNonMultiHomeGroupMirrorList(segPairList *[]SegmentPair, mirrorBasePort int, mirrorDataDirectories []string, hostList []string, addressNameMap map[string]string) *[]SegmentPair {
	segNum := 0
	hostListLen := len(hostList)
	for hostIdx := range hostList {
		for segIdx, directory := range mirrorDataDirectories {
			hostAddress := hostList[(hostIdx+1)%hostListLen]
			seg := Segment{
				Hostname:      addressNameMap[hostAddress],
				Address:       hostAddress,
				Port:          mirrorBasePort + segIdx,
				DataDirectory: filepath.Join(directory, fmt.Sprintf("%s%d", constants.DefaultSegName, segNum)),
			}
			(*segPairList)[segNum].Mirror = &seg
			segNum++
		}
	}

	return segPairList
}

func ExpandNonMultiHomeSpreadMirroring(segPairList *[]SegmentPair, mirrorBasePort int, mirrorDataDirectories []string, hostList []string, addressNameMap map[string]string) *[]SegmentPair {
	segmentsPerHost := len(mirrorDataDirectories)
	hostListLen := len(hostList)
	segNum := 0
	for hostIndex := range hostList {
		mirrorHostIndex := (hostIndex + 1) % hostListLen
		for localSeg := 0; localSeg < segmentsPerHost; localSeg++ {
			hostAddress := hostList[mirrorHostIndex]
			seg := Segment{
				Hostname:      addressNameMap[hostAddress],
				Address:       hostAddress,
				Port:          mirrorBasePort + localSeg,
				DataDirectory: filepath.Join(mirrorDataDirectories[localSeg], fmt.Sprintf("%s%d", constants.DefaultSegName, segNum)),
			}
			(*segPairList)[segNum].Mirror = &seg
			segNum++
			mirrorHostIndex = (mirrorHostIndex + 1) % hostListLen
		}
	}

	return segPairList
}

func ExpandMultiHomePrimaryArray(segPairList *[]SegmentPair, primaryBasePort int, primaryDataDirectories []string, hostnameArray []string, nameAddressMap map[string][]string) *[]SegmentPair {
	segNum := 0
	for _, hostname := range hostnameArray {
		addressList := nameAddressMap[hostname]
		for idx, directory := range primaryDataDirectories {
			seg := Segment{
				Hostname:      hostname,
				Address:       addressList[idx%len(addressList)],
				Port:          primaryBasePort + idx,
				DataDirectory: filepath.Join(directory, fmt.Sprintf("%s%d", constants.DefaultSegName, segNum)),
			}
			*segPairList = append(*segPairList, SegmentPair{Primary: &seg})
			segNum++
		}
	}
	return segPairList
}

func ExpandMultiHomeGroupMirrorList(segPairList *[]SegmentPair, mirrorBasePort int, mirrorDataDirectories []string, hostnameArray []string, nameAddressMap map[string][]string) *[]SegmentPair {
	uniqueHostCount := len(hostnameArray)
	segNum := 0
	// Group mirroring
	for idx := 0; idx < uniqueHostCount; idx++ {
		hostname := hostnameArray[(idx+1)%uniqueHostCount]
		addressList := nameAddressMap[hostname]
		for segIdx, directory := range mirrorDataDirectories {
			seg := Segment{
				Hostname:      hostname,
				Address:       addressList[segIdx%len(addressList)],
				Port:          mirrorBasePort + segIdx,
				DataDirectory: filepath.Join(directory, fmt.Sprintf("%s%d", constants.DefaultSegName, segNum)),
			}
			(*segPairList)[segNum].Mirror = &seg
			segNum++
		}
	}

	return segPairList
}

func ExpandMultiHomeSpreadMirrorList(segPairList *[]SegmentPair, mirrorBasePort int, mirrorDataDirectories []string, hostnameArray []string, nameAddressMap map[string][]string) *[]SegmentPair {
	segNum := 0
	uniqueHostCount := len(hostnameArray)
	for hostnameIdx := range hostnameArray {
		for segIdx, directory := range mirrorDataDirectories {
			nxtHostIdx := (hostnameIdx + segIdx + 1) % uniqueHostCount
			// if the current hostname and mirror hostname is same, move to next
			if nxtHostIdx == hostnameIdx {
				nxtHostIdx = (nxtHostIdx + 1) % uniqueHostCount
			}
			nxtHostName := hostnameArray[nxtHostIdx]
			addressList := nameAddressMap[nxtHostName]
			addressCnt := len(addressList)
			seg := Segment{
				Address:       addressList[(hostnameIdx+segIdx)%addressCnt],
				Hostname:      nxtHostName,
				Port:          mirrorBasePort + segIdx,
				DataDirectory: filepath.Join(directory, fmt.Sprintf("%s%d", constants.DefaultSegName, segNum)),
			}
			(*segPairList)[segNum].Mirror = &seg
			segNum++
		}
	}

	return segPairList
}

/*
ExpandSegPairArray expands primary and mirror configuration from the given configuration
Returns an array of segmentPair to be updated in the MakeCluster request
*/
func ExpandSegPairArray(config InitConfig, multiHome bool, nameAddressMap map[string][]string, addressNameMap map[string]string) []SegmentPair {
	var segPairList []SegmentPair

	slices.Sort(config.HostList)
	if multiHome {
		// Build a list of unit hosts
		var hostnameArray []string
		for hostname := range nameAddressMap {
			hostnameArray = append(hostnameArray, hostname)
		}
		slices.Sort(hostnameArray)

		// Expand Primaries
		segPairList = *ExpandMultiHomePrimaryArray(&segPairList, config.PrimaryBasePort, config.PrimaryDataDirectories, hostnameArray, nameAddressMap)

		// Add mirrors to this expansion
		if ContainsMirror {
			if config.MirroringType == constants.GroupMirroring {
				segPairList = *ExpandMultiHomeGroupMirrorList(&segPairList, config.MirrorBasePort, config.MirrorDataDirectories, hostnameArray, nameAddressMap)
			} else {
				// Spread mirroring
				segPairList = *ExpandMultiHomeSpreadMirrorList(&segPairList, config.MirrorBasePort, config.MirrorDataDirectories, hostnameArray, nameAddressMap)
			}
		}
		return segPairList
	} else {
		// non-multi-home setup,
		// Create Primary segments
		segPairList = *ExpandNonMultiHomePrimaryList(&segPairList, config.PrimaryBasePort, config.PrimaryDataDirectories, config.HostList, addressNameMap)

		if ContainsMirror {
			if config.MirroringType == constants.GroupMirroring {
				// Perform group mirroring
				segPairList = *ExpandNonMultiHomeGroupMirrorList(&segPairList, config.MirrorBasePort, config.MirrorDataDirectories, config.HostList, addressNameMap)
			} else {
				// Perform spread mirroring
				segPairList = *ExpandNonMultiHomeSpreadMirroring(&segPairList, config.MirrorBasePort, config.MirrorDataDirectories, config.HostList, addressNameMap)
			}
		}
		//End of non-multi-home expansion
	}

	return segPairList
}

/*
CreateMakeClusterReq helper function to populate cluster request from the config
*/
func CreateMakeClusterReq(config *InitConfig, forceFlag bool, verbose bool) *idl.MakeClusterRequest {
	var segmentPairs []*idl.SegmentPair
	for _, pair := range config.SegmentArray {
		segmentPairs = append(segmentPairs, SegmentPairToIdl(&pair))
	}

	return &idl.MakeClusterRequest{
		GpArray: &idl.GpArray{
			Coordinator:  SegmentToIdl(&config.Coordinator),
			SegmentArray: segmentPairs,
		},
		ClusterParams: ClusterParamsToIdl(config),
		ForceFlag:     forceFlag,
		Verbose:       verbose,
	}
}

func SegmentToIdl(seg *Segment) *idl.Segment {
	if seg == nil {
		return nil
	}
	return &idl.Segment{
		Port:          int32(seg.Port),
		DataDirectory: seg.DataDirectory,
		HostName:      seg.Hostname,
		HostAddress:   seg.Address,
	}
}

func SegmentPairToIdl(pair *SegmentPair) *idl.SegmentPair {
	return &idl.SegmentPair{
		Primary: SegmentToIdl(pair.Primary),
		Mirror:  SegmentToIdl(pair.Mirror),
	}
}

func ClusterParamsToIdl(config *InitConfig) *idl.ClusterParams {
	return &idl.ClusterParams{
		CoordinatorConfig: config.CoordinatorConfig,
		SegmentConfig:     config.SegmentConfig,
		CommonConfig:      config.CommonConfig,
		Locale: &idl.Locale{
			LcAll:      config.Locale.LcAll,
			LcCollate:  config.Locale.LcCollate,
			LcCtype:    config.Locale.LcCtype,
			LcMessages: config.Locale.LcMessages,
			LcMonetory: config.Locale.LcMonetary,
			LcNumeric:  config.Locale.LcNumeric,
			LcTime:     config.Locale.LcTime,
		},
		HbaHostnames:  config.HbaHostnames,
		Encoding:      config.Encoding,
		SuPassword:    config.SuPassword,
		DbName:        config.DbName,
		DataChecksums: config.DataChecksums,
	}
}

/*
ValidateInputConfigAndSetDefaultsFn performs various validation checks on the configuration
*/
func ValidateInputConfigAndSetDefaultsFn(request *idl.MakeClusterRequest, cliHandler *viper.Viper) error {
	//Check if coordinator details are provided
	if !cliHandler.IsSet("coordinator") {
		return fmt.Errorf("no coordinator segment provided in input config file")
	}

	//Check if primary segment details are provided
	if !cliHandler.IsSet("segment-array") && !cliHandler.IsSet("primary-data-directories") {
		return fmt.Errorf("no primary segments are provided in input config file")
	}

	//Check if locale is provided, if not set it to system locale
	if !cliHandler.IsSet("locale") {
		gplog.Warn("locale is not provided, setting it to system locale")
		err := SetDefaultLocale(request.ClusterParams.Locale)
		if err != nil {
			return err
		}
	}

	numPrimary := len(request.GetPrimarySegments())
	numMirror := len(request.GetMirrorSegments())

	if numPrimary == 0 {
		return fmt.Errorf("no primary segments are provided in input config file")
	}

	if numPrimary != len(request.GpArray.SegmentArray) {
		return fmt.Errorf("invalid segment array, primary segments are missing in some segment objects")
	}

	if numMirror != 0 && numPrimary != numMirror {
		return fmt.Errorf("number of primary segments %d and number of mirror segments %d must be equal", numPrimary, numMirror)
	}

	// validate details of coordinator
	err := ValidateSegment(request.GpArray.Coordinator)
	if err != nil {
		return err
	}

	// validate the details of segments
	for _, seg := range append(request.GetPrimarySegments(), request.GetMirrorSegments()...) {
		err = ValidateSegment(seg)
		if err != nil {
			return err
		}
	}

	// check for conflicting port and data-dir on a host
	err = CheckForDuplicatPortAndDataDirectory(append(request.GetPrimarySegments(), request.GetMirrorSegments()...))
	if err != nil {
		return err
	}

	// check if gp services enabled on hosts
	err = IsGpServicesEnabled(request)
	if err != nil {
		return err
	}

	if request.ClusterParams.Encoding == "" {
		gplog.Info(fmt.Sprintf("Could not find encoding in cluster config, defaulting to %v", constants.DefaultEncoding))
		request.ClusterParams.Encoding = constants.DefaultEncoding
	}

	if request.ClusterParams.Encoding == "SQL_ASCII" {
		return fmt.Errorf("SQL_ASCII is no longer supported as a server encoding")
	}

	// Validate max_connections
	err = ValidateMaxConnections(request.ClusterParams)
	if err != nil {
		return err
	}

	// if shared_buffers not provided in config then set the COORDINATOR_SHARED_BUFFERS and QE_SHARED_BUFFERS to DEFAULT_BUFFERS (128000 kB)
	CheckAndSetDefaultConfigParams(request.ClusterParams, "shared_buffers", constants.DefaultBuffer)

	return nil
}

/*
ValidateSegment checks if valid values have been provided for the segment hostname, address, port and data-directory.
If hostname is not provided then the function returns an error.
If address is not provided then it is populated with the hostname value
*/
func ValidateSegment(segment *idl.Segment) error {
	if segment.HostName == "" {
		//TODO Call RPC to get the hostname from hostAddress and populate here as segment.HostName
		return fmt.Errorf("hostName has not been provided for the segment with port %v and data_directory %v", segment.Port, segment.DataDirectory)
	}

	if segment.HostAddress == "" {
		segment.HostAddress = segment.HostName
		gplog.Warn("hostAddress has not been provided, populating it with same as hostName %v for the segment with port %v and data_directory %v", segment.HostName, segment.Port, segment.DataDirectory)
	}

	if segment.Port <= 0 {
		return fmt.Errorf("invalid port has been provided for segment with hostname %v and data_directory %v", segment.HostName, segment.DataDirectory)
	}

	if segment.DataDirectory == "" {
		return fmt.Errorf("data_directory has not been provided for segment with hostname %v and port %v", segment.HostName, segment.Port)
	}
	return nil
}

/*
CheckForDuplicatePortAndDataDirectoryFn checks for duplicate data-directories and ports on host.
In case of data-directories, look for unique host-names.
For checking duplicate port, checking if address is unique. A host can use same the port for a different address.
*/
func CheckForDuplicatePortAndDataDirectoryFn(segs []*idl.Segment) error {
	hostToDataDirectory := make(map[string]map[string]bool)
	hostToPort := make(map[string]map[int32]bool)
	for _, seg := range segs {
		//Check for data-directory
		if _, ok := hostToDataDirectory[seg.HostName]; !ok {
			hostToDataDirectory[seg.HostName] = make(map[string]bool)
		}
		if _, ok := hostToDataDirectory[seg.HostName][seg.DataDirectory]; ok {
			return fmt.Errorf("duplicate data directory entry %v found for host %v", seg.DataDirectory, seg.HostAddress)
		}
		hostToDataDirectory[seg.HostName][seg.DataDirectory] = true

		// Check for port
		if _, ok := hostToPort[seg.HostAddress]; !ok {
			hostToPort[seg.HostAddress] = make(map[int32]bool)
		}
		if _, ok := hostToPort[seg.HostName][seg.Port]; ok {
			return fmt.Errorf("duplicate port entry %v found for host %v", seg.Port, seg.HostName)
		}
		hostToPort[seg.HostAddress][seg.Port] = true
	}
	return nil
}

/*
GetSystemLocaleFn returns system locales
*/
func GetSystemLocaleFn() ([]byte, error) {
	cmd := utils.System.ExecCommand("/usr/bin/locale")
	output, err := cmd.Output()

	if err != nil {
		return []byte(""), fmt.Errorf("failed to get locale on this system: %w", err)
	}

	return output, nil
}

/*
SetDefaultLocaleFn populates the locale struct with system locales
*/
func SetDefaultLocaleFn(locale *idl.Locale) error {
	systemLocale, err := GetSystemLocale()
	if err != nil {
		return err
	}
	v := viper.New()
	v.SetConfigType("properties")
	err = v.ReadConfig(bytes.NewBuffer(systemLocale))
	if err != nil {
		return err
	}

	locale.LcAll = strings.Trim(v.GetString("LC_ALL"), "\"")
	locale.LcCollate = strings.Trim(v.GetString("LC_COLLATE"), "\"")
	locale.LcCtype = strings.Trim(v.GetString("LC_CTYPE"), "\"")
	locale.LcMessages = strings.Trim(v.GetString("LC_MESSAGES"), "\"")
	locale.LcMonetory = strings.Trim(v.GetString("LC_MONETARY"), "\"")
	locale.LcNumeric = strings.Trim(v.GetString("LC_NUMERIC"), "\"")
	locale.LcTime = strings.Trim(v.GetString("LC_TIME"), "\"")

	return nil
}

/*
IsGpServicesEnabledFn returns error if any of the hosts from config does not have gp services enabled
*/
func IsGpServicesEnabledFn(req *idl.MakeClusterRequest) error {
	hostnames := []string{req.GpArray.Coordinator.HostName}
	for _, seg := range req.GetPrimarySegments() {
		hostnames = append(hostnames, seg.HostName)
	}

	// remove any duplicate entries
	slices.Sort(hostnames)
	hostnames = slices.Compact(hostnames)

	diff := utils.GetListDifference(hostnames, Conf.Hostnames)
	if len(diff) != 0 {
		return fmt.Errorf("following hostnames %s do not have gp services configured. Please configure the services", diff)
	}
	return nil
}

/*
ValidateMaxConnections sets the default value of max_connections if not provided in config. Also returns error if valid value is not provided
if max_connections not defined in CommonConfig set it to default value
if max_connections not defined in CoordinatorConfig set to CommonConfig value
if max_connections not defined in SegmentConfig set it to strconv.Atoi(clusterParams.CommonConfig["max_connections"])*constants.QeConnectFactor
*/
func ValidateMaxConnections(clusterParams *idl.ClusterParams) error {
	if _, ok := clusterParams.CommonConfig["max_connections"]; !ok {
		gplog.Info("max_connections not set, will set to default value %v", constants.DefaultQdMaxConnect)
		clusterParams.CommonConfig["max_connections"] = strconv.Itoa(constants.DefaultQdMaxConnect)
	}

	if _, ok := clusterParams.CoordinatorConfig["max_connections"]; !ok {
		// Check if common-config has max-connections defined
		gplog.Info("Coordinator max_connections not set, will set to value %v from CommonConfig", clusterParams.CommonConfig["max_connections"])
		clusterParams.CoordinatorConfig["max_connections"] = clusterParams.CommonConfig["max_connections"]
	}
	coordinatorMaxConnect, err := strconv.Atoi(clusterParams.CoordinatorConfig["max_connections"])
	if err != nil {
		return fmt.Errorf("invalid value %s for max_connections, must be an integer. error: %v",
			clusterParams.CoordinatorConfig["max_connections"], err)
	}

	if coordinatorMaxConnect < 1 {
		return fmt.Errorf("COORDINATOR max_connections value %d is too small. Should be more than 1. ", coordinatorMaxConnect)
	}

	// if max_connections not defined in SegmentConfig, set to commonConfigMaxConnections*QeConnectFactor
	if _, ok := clusterParams.SegmentConfig["max_connections"]; !ok {
		maxConnections, err := strconv.Atoi(clusterParams.CommonConfig["max_connections"])
		if err != nil {
			return fmt.Errorf("invalid value %s for max_connections, must be an integer. error: %v",
				clusterParams.CommonConfig["max_connections"], err)
		}
		segmentConfigMaxConnections := maxConnections * constants.QeConnectFactor
		gplog.Info("Segment max_connections not set, will set to value %v", segmentConfigMaxConnections)
		clusterParams.SegmentConfig["max_connections"] = strconv.Itoa(segmentConfigMaxConnections)
	}
	return nil
}

/*
CheckAndSetDefaultConfigParams sets the default value for parameters not defined in config
if configParam is not defined in CommonConfig , the value will be set to defaultValue provided
if configParam is not defined in CoordinatorConfig or SegmentConfig, the value will be set to same as configParam from CommonConfig
*/
func CheckAndSetDefaultConfigParams(clusterParams *idl.ClusterParams, configParam string, defaultValue string) {
	if _, ok := clusterParams.CommonConfig[configParam]; !ok {
		gplog.Info(fmt.Sprintf("%v is not set in CommonConfig, will set to default value %v", configParam, defaultValue))
		clusterParams.CommonConfig[configParam] = defaultValue
	}

	if _, ok := clusterParams.CoordinatorConfig[configParam]; !ok {
		// Check if common-config has configParam defined
		gplog.Info("Coordinator %v not set, will set to value %v from CommonConfig", configParam, clusterParams.CommonConfig[configParam])
		clusterParams.CoordinatorConfig[configParam] = clusterParams.CommonConfig[configParam]
	}
	if _, ok := clusterParams.SegmentConfig[configParam]; !ok {
		// Check if common-config has configParam defined
		gplog.Info("Segment %v not set, will set to value %v from CommonConfig", configParam, clusterParams.CommonConfig[configParam])
		clusterParams.SegmentConfig[configParam] = clusterParams.CommonConfig[configParam]
	}
}

/*
CheckGpServiceIsConfigured check if gpservice.conf file exists on the ConfigFilePath
if file exists validate that it has some content
return true if file exists and has some content else false
*/
func CheckGpServiceIsConfigured() bool {
	fileInfo, err := os.Stat(ConfigFilePath)
	if os.IsNotExist(err) {
		return false
	} else if err != nil {
		return false
	}

	// Check if the file is not empty
	if fileInfo.Size() == 0 {
		os.Remove(ConfigFilePath)
		return false
	}

	return true
}

// getHostnameUsingSSH executes the SSH command to run `hostname` on the remote host
func getHostnameUsingSSH(host string, wg *sync.WaitGroup, results chan<- string, errors chan<- error) {
	defer wg.Done()

	// Construct the SSH command
	cmd := exec.Command("ssh", host, "hostname")

	// Capture the output
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errors <- fmt.Errorf("failed to run command on host %s: %v, stderr: %s", host, err, stderr.String())
		return
	}

	hostname := strings.TrimSpace(stdout.String())
	results <- hostname
}

// GetHostnamesFromHosts takes a list of hosts, performs SSH to them in parallel, and runs the hostname command
// returns list of hostname
func GetHostnamesFromHosts(hosts []string) ([]string, error) {
	var wg sync.WaitGroup
	results := make(chan string, len(hosts))
	errs := make(chan error, len(hosts))

	for _, host := range hosts {
		wg.Add(1)
		go getHostnameUsingSSH(host, &wg, results, errs)
	}

	wg.Wait()
	close(results)
	close(errs)

	var hostnames []string
	for result := range results {
		hostnames = append(hostnames, result)
	}

	var err error
	for e := range errs {
		err = errors.Join(err, e)
	}

	return hostnames, err
}

func GetHostListFromCleanUpFile(clusterCleanupFile string) ([]string, error) {

	_, err := utils.System.Stat(clusterCleanupFile)
	if err != nil {
		return nil, fmt.Errorf("cluster is clean, no cleanup file present")
	}
	entries, err := utils.ReadEntriesFromFile(clusterCleanupFile)
	if err != nil {
		return nil, fmt.Errorf("failed reading entries from cleanup file")
	}
	hostnameMap := make(map[string]struct{})

	for _, entry := range entries {
		fields := strings.Fields(entry)
		if len(fields) == 2 {
			host := fields[0]
			hostnameMap[host] = struct{}{}
		} else {
			return nil, fmt.Errorf("invalid entries in map")
		}
	}

	uniqueHostnames := make([]string, 0, len(hostnameMap))
	for hostname := range hostnameMap {
		uniqueHostnames = append(uniqueHostnames, hostname)
	}
	return uniqueHostnames, nil
}

/*
GetHostlistFromConfig parse content of the hostlist from the config file and return
*/
func GetHostlistFromConfig(filePath string) ([]string, error) {

	v := viper.New()
	v.SetConfigFile(filePath)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("error reading config file: %w", err)
	}

	var config InitConfig
	if err := v.UnmarshalExact(&config); err != nil {
		return nil, fmt.Errorf("failed to parse config file:%v error: %v", filePath, err)
	}
	hostnameMap := make(map[string]struct{})

	// Add hostnames from hostlist
	for _, hostname := range config.HostList {
		hostnameMap[hostname] = struct{}{}
	}

	// Add coordinator hostname
	if config.Coordinator.Hostname != "" {
		hostnameMap[config.Coordinator.Hostname] = struct{}{}
	}

	// Add hostnames from segment-array
	for _, segment := range config.SegmentArray {
		if segment.Primary.Hostname != "" {
			hostnameMap[segment.Primary.Hostname] = struct{}{}
		}
		if segment.Mirror.Hostname != "" {
			hostnameMap[segment.Mirror.Hostname] = struct{}{}
		}
	}

	uniqueHostnames := make([]string, 0, len(hostnameMap))
	for hostname := range hostnameMap {
		uniqueHostnames = append(uniqueHostnames, hostname)
	}
	return uniqueHostnames, nil
}
