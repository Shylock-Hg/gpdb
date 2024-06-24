package testutils

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/greenplum-db/gpdb/gpservice/constants"
	"github.com/greenplum-db/gpdb/gpservice/internal/platform"
	"github.com/greenplum-db/gpdb/gpservice/pkg/gpservice_config"
	"github.com/greenplum-db/gpdb/gpservice/pkg/utils"
)

type Command struct {
	host   string
	cmdStr string
	args   []string
}

type CmdResult struct {
	OutputMsg string
	ExitCode  int
}

const ExitCode1 = 1

func ConfigureAndStartServices(hostfile string) error {
	result, err := RunGPServiceInit(true, "--hostfile", hostfile)
	if err != nil {
		return fmt.Errorf("failed to configure the services: %v, %v", result.OutputMsg, err)
	}

	result, err = RunGpserviceStart()
	if err != nil {
		return fmt.Errorf("failed to start the services: %v, %v", result.OutputMsg, err)
	}

	startTime := time.Now()
	timeout := 10 * time.Second

	for {
		result, err := RunGpserviceStatus()
		if err == nil {
			return nil
		}

		if time.Since(startTime) >= timeout {
			return fmt.Errorf("failed to configure services after %v seconds: %v, %v", timeout, result.OutputMsg, err)
		}

		time.Sleep(500 * time.Millisecond)
	}
}

func RunGPServiceInit(useCert bool, params ...string) (CmdResult, error) {
	var args []string

	if useCert {
		args = append([]string{"init"}, CertificateParams...)
		args = append(args, params...)
	} else {
		args = append([]string{"init"}, params...)
	}

	genCmd := Command{
		cmdStr: constants.DefaultServiceName,
		args:   args,
	}
	return runCmd(genCmd)
}

func RunGpserviceStart(params ...string) (CmdResult, error) {
	params = append([]string{"start"}, params...)
	genCmd := Command{
		cmdStr: constants.DefaultServiceName,
		args:   params,
	}
	return runCmd(genCmd)
}

func RunGpserviceStop(params ...string) (CmdResult, error) {
	params = append([]string{"stop"}, params...)
	genCmd := Command{
		cmdStr: constants.DefaultServiceName,
		args:   params,
	}
	return runCmd(genCmd)
}

func RunGpserviceStatus(params ...string) (CmdResult, error) {
	params = append([]string{"status"}, params...)
	genCmd := Command{
		cmdStr: constants.DefaultServiceName,
		args:   params,
	}
	return runCmd(genCmd)
}

func RunGpServiceDelete(params ...string) (CmdResult, error) {
	params = append([]string{"delete"}, params...)
	genCmd := Command{
		cmdStr: constants.DefaultServiceName,
		args:   params,
	}
	return runCmd(genCmd)
}

func RunInitCluster(params ...string) (CmdResult, error) {
	params = append([]string{"init"}, params...)
	genCmd := Command{
		cmdStr: constants.DefaultGpCtlName,
		args:   params,
	}
	return runCmd(genCmd)
}

func DisableandDeleteHubServiceFile(p platform.Platform, serviceName string) {
	serviceDir, serviceExt, serviceCmd := GetServiceDetails(p)
	hubServiceFile := filepath.Join(serviceDir, fmt.Sprintf("%s.%s", serviceName, serviceExt))
	fmt.Printf(hubServiceFile)
	UnloadSvcFile(serviceCmd, hubServiceFile)
	_ = os.RemoveAll(hubServiceFile)
}

func DisableandDeleteAgentServiceFile(p platform.Platform, serviceName string) {
	serviceDir, serviceExt, serviceCmd := GetServiceDetails(p)
	agentServiceFile := filepath.Join(serviceDir, fmt.Sprintf("%s.%s", serviceName, serviceExt))
	fmt.Printf(agentServiceFile)
	UnloadSvcFile(serviceCmd, agentServiceFile)
	_ = os.RemoveAll(agentServiceFile)
}

func RunGpStatus(params ...string) (CmdResult, error) {
	allParams := append([]string{"gpstate"}, params...)

	genCmd := Command{
		cmdStr: allParams[0],
		args:   allParams[1:],
	}

	return runCmd(genCmd)
}

func RunGpCheckCat(params ...string) (CmdResult, error) {
	allParams := append([]string{"gpcheckcat"}, params...)

	genCmd := Command{
		cmdStr: allParams[0],
		args:   allParams[1:],
	}

	return runCmd(genCmd)
}

func RunGpStop(params ...string) (CmdResult, error) {
	allParams := append([]string{"gpstop", "-a"}, params...)

	genCmd := Command{
		cmdStr: allParams[0],
		args:   allParams[1:],
	}

	return runCmd(genCmd)
}

func RunGpRecoverSeg(params ...string) (CmdResult, error) {
	allParams := append([]string{"gprecoverseg", "-a"}, params...)

	genCmd := Command{
		cmdStr: allParams[0],
		args:   allParams[1:],
	}

	return runCmd(genCmd)
}

func RunGpStopSegment(dataDir, hostname string) error {
	cmdStr := fmt.Sprintf("source %s/greenplum_path.sh && pg_ctl stop -m fast -D %s -w -t 120", os.Getenv("GPHOME"), dataDir)
	cmd := exec.Command("ssh", hostname, cmdStr)

	err := cmd.Run()
	if err != nil {
		return fmt.Errorf("failed to stop segment via SSH: %v", err)
	}
	return nil
}

func RunGpStart(params ...string) (CmdResult, error) {
	allParams := append([]string{"gpstart", "-a"}, params...)

	genCmd := Command{
		cmdStr: allParams[0],
		args:   allParams[1:],
	}

	return runCmd(genCmd)
}

func DeleteCluster() (CmdResult, error) {
	genCmd := Command{
		cmdStr: "bash",
		args:   []string{"-c", "echo -e 'y\ny' | gpdeletesystem"},
	}
	return runCmd(genCmd)
}

func ParseConfig(configFile string) gpservice_config.Config {
	gpConfig := gpservice_config.Config{}
	gpConfig.Credentials = &utils.GpCredentials{}
	config, _ := os.Open(configFile)
	defer config.Close()
	byteValue, _ := io.ReadAll(config)

	_ = json.Unmarshal(byteValue, &gpConfig)

	return gpConfig
}

func CleanupFilesOnHub(files ...string) {
	for _, f := range files {
		_ = os.RemoveAll(f)
	}
}

func CleanupFilesOnAgents(file string, hosts []string) {
	cmdStr := fmt.Sprintf("/bin/bash -c 'rm -rf %s && echo $?'", file)
	for _, host := range hosts {
		cmd := exec.Command("ssh", host, cmdStr)
		_, _ = cmd.CombinedOutput()
	}
}

func CpCfgWithoutCertificates(name string) error {
	cfg := ParseConfig(DefaultConfigurationFile)
	cfg.Credentials = &utils.GpCredentials{}
	content, _ := json.Marshal(cfg)
	return os.WriteFile(name, content, 0777)
}

func extractPID(outMessage string) string {
	pidRegex1 := regexp.MustCompile(`"PID"\s*=\s*(\d+);`)
	pidRegex2 := regexp.MustCompile(`MainPID=(\d+)`)
	pidRegex3 := regexp.MustCompile(`\b(\d+)\s+\w+\b`)
	if match := pidRegex1.FindStringSubmatch(outMessage); len(match) >= 2 {
		return match[1]
	} else if match = pidRegex2.FindStringSubmatch(outMessage); len(match) >= 2 {
		return match[1]
	}

	for _, line := range strings.Split(outMessage, "\n") {
		if strings.Contains(line, "(LISTEN)") && pidRegex3.MatchString(line) {
			match := pidRegex3.FindStringSubmatch(line)
			if len(match) > 1 {
				return match[1]
			}
		}
	}

	return "0"
}

func runCmd(cmd Command) (CmdResult, error) {
	var cmdObj *exec.Cmd
	if cmd.host == "" || cmd.host == DefaultHost {
		cmdObj = exec.Command(cmd.cmdStr, cmd.args...)
	} else {
		subCmd := exec.Command(cmd.cmdStr, cmd.args...)
		cmdObj = exec.Command("ssh", cmd.host, subCmd.String())
	}

	out, err := cmdObj.CombinedOutput()
	result := CmdResult{
		OutputMsg: string(out),
		ExitCode:  cmdObj.ProcessState.ExitCode(),
	}

	return result, err
}

func runCmdWithUserInput(cmd Command, userinput string) (CmdResult, error) {
	var cmdObj *exec.Cmd
	if cmd.host == "" || cmd.host == DefaultHost {
		cmdObj = exec.Command(cmd.cmdStr, cmd.args...)
	} else {
		subCmd := exec.Command(cmd.cmdStr, cmd.args...)
		cmdObj = exec.Command("ssh", cmd.host, subCmd.String())
	}

	stdin, _ := cmdObj.StdinPipe()
	go func() {
		defer stdin.Close()
		_, err := io.WriteString(stdin, userinput)
		if err != nil {
			return
		}
	}()

	out, err := cmdObj.CombinedOutput()
	result := CmdResult{
		OutputMsg: string(out),
		ExitCode:  cmdObj.ProcessState.ExitCode(),
	}

	return result, err
}

func RunInitClusterwithUserInput(userinput string, params ...string) (CmdResult, error) {
	params = append([]string{"init"}, params...)
	genCmd := Command{
		cmdStr: constants.DefaultGpCtlName,
		args:   params,
	}
	return runCmdWithUserInput(genCmd, userinput)
}

func GetServiceDetails(p platform.Platform) (string, string, string) {
	serviceDir := p.(platform.GpPlatform).ServiceDir
	serviceExt := p.(platform.GpPlatform).ServiceExt
	serviceCmd := p.(platform.GpPlatform).ServiceCmd

	return serviceDir, serviceExt, serviceCmd
}

func UnloadSvcFile(cmd string, file string) {
	genCmd := Command{
		cmdStr: cmd,
	}
	if cmd == "launchctl" {
		genCmd.args = []string{"unload", file}

	} else {
		genCmd.args = []string{"--user", "stop", file}
	}
	_, _ = runCmd(genCmd)
}

func DisableandDeleteServiceFiles(p platform.Platform) {
	serviceDir, serviceExt, serviceCmd := GetServiceDetails(p)
	filesToUnload := GetSvcFiles(serviceDir, serviceExt)
	for _, filePath := range filesToUnload {
		fullPath := filepath.Join(serviceDir, filepath.Base(filePath))
		UnloadSvcFile(serviceCmd, fullPath)
		_ = os.RemoveAll(filePath)
	}
}

func GetSvcFiles(svcDir string, svcExtention string) []string {
	pattern := filepath.Join(svcDir, fmt.Sprintf("*.%s", svcExtention))
	fileList, _ := filepath.Glob(pattern)
	return fileList
}

func InitService(hostfile string, params []string) {
	_, _ = RunGPServiceInit(false, append(
		[]string{
			"--hostfile", hostfile,
		},
		params...)...)
	time.Sleep(5 * time.Second)
}

func CopyFile(src, dest string) error {
	input, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	err = os.WriteFile(dest, input, 0644)
	if err != nil {
		return err
	}
	return nil
}

func GetSvcStatusOnHost(p platform.GpPlatform, serviceName string, host string) (CmdResult, error) {
	args := []string{p.UserArg, p.StatusArg, serviceName}

	if p.OS == "darwin" {
		args = args[1:]
	}
	genCmd := Command{
		cmdStr: p.ServiceCmd,
		args:   args,
	}
	genCmd.host = host

	return runCmd(genCmd)
}

func GetListeningProcess(port int, host string) string {
	var output string
	if host == DefaultHost || host == "" {
		cmd := exec.Command("lsof", fmt.Sprintf("-i:%d", port))
		out, _ := cmd.CombinedOutput()
		output = string(out)
	} else {
		genCmd := Command{
			cmdStr: fmt.Sprintf("lsof -i:%d", port),
			host:   host,
		}
		result, _ := runCmd(genCmd)
		output = result.OutputMsg
	}

	return extractPID(output)
}

func ExtractStatusData(data string) map[string]map[string]string {
	lines := strings.Split(data, "\n")
	dataMap := make(map[string]map[string]string)

	for i := 1; i < len(lines); i++ {
		fields := strings.Fields(lines[i])
		if len(fields) >= 4 {
			role := fields[0] // agent or hub
			host := fields[1]
			pid := fields[3]

			// Create a map for the role if it doesn't exist
			if _, ok := dataMap[role]; !ok {
				dataMap[role] = make(map[string]string)
			}

			// Add host and pid to the role's map
			dataMap[role][host] = pid
		}
	}

	return dataMap
}

func StructToString(s interface{}) string {
	return structToString(reflect.ValueOf(s), 0)
}

func structToString(value reflect.Value, indentLevel int) string {
	if value.Kind() != reflect.Struct {
		return ""
	}

	var result string
	result += fmt.Sprintf("%s {", value.Type().Name())

	for i := 0; i < value.NumField(); i++ {
		field := value.Field(i)
		fieldName := value.Type().Field(i).Name

		if field.Kind() == reflect.Struct {
			// Recursively handle nested structs
			nestedString := structToString(field, indentLevel+1)
			result += fmt.Sprintf("\n%s%s: %s", strings.Repeat("    ", indentLevel+1), fieldName, nestedString)
		} else {
			result += fmt.Sprintf("%s: %v", fieldName, field.Interface())
		}

		if i < value.NumField()-1 {
			result += ", "
		}
	}

	result += "}"
	return result
}

func GetHostListFromFile(hostfile string) []string {
	content, _ := os.ReadFile(hostfile)

	return strings.Fields(string(content))
}

func GetTempFile(t *testing.T, name string) string {
	dir := t.TempDir()
	return filepath.Join(dir, name)
}

func CheckifClusterisRunning(hostlist []string) (CmdResult, error) {

	var result CmdResult
	var err error
	cmdStr := "/bin/bash -c 'ps -ef | grep postgres | wc -l'"

	for _, hostname := range hostlist {
		genCmd := Command{
			cmdStr: cmdStr,
			host:   hostname,
		}
		result, err := runCmd(genCmd)

		if result.ExitCode != 0 && strings.TrimSpace(result.OutputMsg) != "2" {
			return result, err
		}

	}

	return result, err
}

func CheckifdataDirIsEmpty(hostmap map[string][]string) (CmdResult, error) {

	var result CmdResult
	var err error
	for hostname, datadirs := range hostmap {
		for _, dir := range datadirs {
			cmdStr := fmt.Sprintf("/bin/bash -c 'ls -l %s | wc -l'", dir)
			genCmd := Command{
				cmdStr: cmdStr,
				host:   hostname,
			}
			result, err = runCmd(genCmd)
			if result.ExitCode != 0 && strings.TrimSpace(result.OutputMsg) != "1" {
				return result, err
			}
		}
	}
	return result, err
}

func CheckServiceFilesNotExist(t *testing.T, hosts []string) {
	t.Helper()
	var (
		defaultServiceDir string
		serviceExt        string
	)
	p := platform.GetPlatform()
	defaultServiceDir, serviceExt, _ = GetServiceDetails(p)

	agentFile := fmt.Sprintf("%s/%s_%s.%s", defaultServiceDir, constants.DefaultServiceName, "agent", serviceExt)
	hubFile := fmt.Sprintf("%s/%s_%s.%s", defaultServiceDir, constants.DefaultServiceName, "hub", serviceExt)

	//check if file exists on agents
	cmdStr := fmt.Sprintf("/bin/bash -c 'test -e %s && echo 1'", agentFile)
	for _, host := range hosts {
		cmd := exec.Command("ssh", host, cmdStr)
		out, _ := cmd.CombinedOutput()
		if strings.TrimSpace(string(out)) != "1" {
			t.Errorf("File %s found on %s", agentFile, host)
		}
	}
	//check if services files exists on hub
	if _, err := os.Stat(hubFile); err == nil {
		t.Errorf("File %s found", hubFile)
	}

}

func CheckServiceNotRunning(t *testing.T, hosts []string) {
	t.Helper()
	p := platform.GetPlatform()

	for _, svc := range []string{"gpservice_hub", "gpservice_agent"} {
		hostList := hosts[:1]
		if svc == "gpservice_agent" {
			hostList = hosts
		}
		for _, host := range hostList {
			status, _ := GetSvcStatusOnHost(p.(platform.GpPlatform), svc, host)
			VerifySvcNotRunning(t, status.OutputMsg)
		}
	}
}
