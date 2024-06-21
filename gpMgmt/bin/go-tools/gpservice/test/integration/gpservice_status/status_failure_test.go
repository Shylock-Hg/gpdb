package status

import (
	"os"
	"strings"
	"testing"

	"github.com/greenplum-db/gpdb/gpservice/test/integration/testutils"
)

func TestStatusFailures(t *testing.T) {
	t.Run("checking service status without configuration file will fail", func(t *testing.T) {
		testutils.InitService(*hostfile, testutils.CertificateParams)
		_, _ = testutils.RunGpserviceStart()
		_ = testutils.CopyFile(testutils.DefaultConfigurationFile, "/tmp/config.conf")
		_ = os.RemoveAll(testutils.DefaultConfigurationFile)

		expectedOut := "could not open service config file"

		result, err := testutils.RunGpserviceStatus()
		if err == nil {
			t.Errorf("\nExpected error Got: %#v", err)
		}
		if result.ExitCode != testutils.ExitCode1 {
			t.Errorf("\nExpected: %#v \nGot: %v", testutils.ExitCode1, result.ExitCode)
		}
		if !strings.Contains(result.OutputMsg, expectedOut) {
			t.Errorf("\nExpected string: %#v \nNot found in: %#v", expectedOut, result.OutputMsg)
		}

		_, _ = testutils.RunGpserviceStop("--config-file", "/tmp/config.conf")
	})

	t.Run("checking status of services after stopping hub will fail", func(t *testing.T) {
		testutils.InitService(*hostfile, testutils.CertificateParams)
		_, _ = testutils.RunGpserviceStop("--hub")

		expectedOut := []string{
			"Hub", "not running", "0",
			"service must be running to get the agent status",
		}

		result, err := testutils.RunGpserviceStatus()
		if err == nil {
			t.Errorf("\nExpected error Got: %#v", err)
		}
		if result.ExitCode != testutils.ExitCode1 {
			t.Errorf("\nExpected: %#v \nGot: %v", testutils.ExitCode1, result.ExitCode)
		}
		for _, item := range expectedOut {
			if !strings.Contains(result.OutputMsg, item) {
				t.Errorf("\nExpected string: %#v \nNot found in: %#v", item, result.OutputMsg)
			}
		}
	})

	t.Run("checking status of services without certificates", func(t *testing.T) {
		testutils.InitService(*hostfile, testutils.CertificateParams)
		_, _ = testutils.RunGpserviceStart()
		_ = testutils.CpCfgWithoutCertificates(configCopy)

		cliParams := []string{
			"--config-file", configCopy,
		}
		expectedOut := "error while loading server certificate"

		result, err := testutils.RunGpserviceStatus(cliParams...)
		if err == nil {
			t.Errorf("\nExpected error Got: %#v", err)
		}
		if result.ExitCode != testutils.ExitCode1 {
			t.Errorf("\nExpected: %#v \nGot: %v", testutils.ExitCode1, result.ExitCode)
		}
		if !strings.Contains(result.OutputMsg, expectedOut) {
			t.Errorf("\nExpected string: %#v \nNot found in: %#v", expectedOut, result.OutputMsg)
		}

		_, _ = testutils.RunGpserviceStop()
	})

	t.Run("checking service status with no value for --config-file will fail", func(t *testing.T) {
		testutils.InitService(*hostfile, testutils.CertificateParams)
		_, _ = testutils.RunGpserviceStart()

		cliParams := []string{
			"--config-file",
		}
		expectedOut := "flag needs an argument: --config-file"

		result, err := testutils.RunGpserviceStatus(cliParams...)
		if err == nil {
			t.Errorf("\nExpected error Got: %#v", err)
		}
		if result.ExitCode != testutils.ExitCode1 {
			t.Errorf("\nExpected: %#v \nGot: %v", testutils.ExitCode1, result.ExitCode)
		}
		if !strings.Contains(result.OutputMsg, expectedOut) {
			t.Errorf("\nExpected string: %#v \nNot found in: %#v", expectedOut, result.OutputMsg)
		}

		_, _ = testutils.RunGpserviceStop()
	})

	t.Run("checking service status with non-existing file for --config-file will fail", func(t *testing.T) {
		testutils.InitService(*hostfile, testutils.CertificateParams)
		_, _ = testutils.RunGpserviceStart()

		cliParams := []string{
			"--config-file", "file",
		}
		expectedOut := "no such file or directory"

		result, err := testutils.RunGpserviceStatus(cliParams...)
		if err == nil {
			t.Errorf("\nExpected error Got: %#v", err)
		}
		if result.ExitCode != testutils.ExitCode1 {
			t.Errorf("\nExpected: %#v \nGot: %v", testutils.ExitCode1, result.ExitCode)
		}
		if !strings.Contains(result.OutputMsg, expectedOut) {
			t.Errorf("\nExpected string: %#v \nNot found in: %#v", expectedOut, result.OutputMsg)
		}

		_, _ = testutils.RunGpserviceStop()
	})

	t.Run("checking service status with empty string for --config-file will fail", func(t *testing.T) {
		testutils.InitService(*hostfile, testutils.CertificateParams)
		_, _ = testutils.RunGpserviceStart()

		cliParams := []string{
			"--config-file", "",
		}
		expectedOut := "no such file or directory"

		result, err := testutils.RunGpserviceStatus(cliParams...)
		if err == nil {
			t.Errorf("\nExpected error Got: %#v", err)
		}
		if result.ExitCode != testutils.ExitCode1 {
			t.Errorf("\nExpected: %#v \nGot: %v", testutils.ExitCode1, result.ExitCode)
		}
		if !strings.Contains(result.OutputMsg, expectedOut) {
			t.Errorf("\nExpected string: %#v \nNot found in: %#v", expectedOut, result.OutputMsg)
		}

		_, _ = testutils.RunGpserviceStop()
	})

	t.Run("checking status of services command with invalid param shows help", func(t *testing.T) {
		testutils.InitService(*hostfile, testutils.CertificateParams)
		cliParams := []string{
			"invalid",
		}

		expectedOut := append([]string{
			"Error: unknown command \"invalid\" for \"gpservice status\"",
		}, testutils.CommonHelpText...)

		result, err := testutils.RunGpserviceStatus(cliParams...)
		if err == nil {
			t.Errorf("\nExpected error Got: %#v", err)
		}
		if result.ExitCode != testutils.ExitCode1 {
			t.Errorf("\nExpected: %#v \nGot: %v", testutils.ExitCode1, result.ExitCode)
		}
		for _, item := range expectedOut {
			if !strings.Contains(result.OutputMsg, item) {
				t.Errorf("\nExpected string: %#v \nNot found in: %#v", item, result.OutputMsg)
			}
		}
	})
}
