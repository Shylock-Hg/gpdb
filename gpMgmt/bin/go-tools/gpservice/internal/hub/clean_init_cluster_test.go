package hub_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/greenplum-db/gpdb/gpservice/testutils"

	"github.com/golang/mock/gomock"
	"github.com/greenplum-db/gp-common-go-libs/testhelper"
	"github.com/greenplum-db/gpdb/gpservice/constants"
	"github.com/greenplum-db/gpdb/gpservice/idl"
	"github.com/greenplum-db/gpdb/gpservice/idl/mock_idl"
	"github.com/greenplum-db/gpdb/gpservice/internal/hub"
	"github.com/greenplum-db/gpdb/gpservice/pkg/utils"
)

func CreateCleanupFile(dirPath string) error {

	_, err := utils.System.Stat(dirPath)

	//Create file if it doesnt exist
	if err != nil {
		if err := os.MkdirAll(dirPath, 0777); err != nil {
			err = os.Chmod(dirPath, 0777)
			return err
		}
	}
	_, err = os.Create(constants.CleanFileName)
	if err != nil {
		return err
	}
	return nil

}

func TestCleanInitCluster(t *testing.T) {
	testhelper.SetupTestLogger()
	hubServer := hub.New(testutils.CreateDummyServiceConfig(t))

	t.Run("entries file does not exist", func(t *testing.T) {

		req := &idl.CleanInitClusterRequest{}

		_, err := hubServer.CleanInitCluster(context.Background(), req)

		// Check error
		expectedErrPrefix := "Cluster is clean"
		if err != nil {
			if !strings.HasPrefix(err.Error(), expectedErrPrefix) {
				t.Fatalf("got %s, want %s", err.Error(), expectedErrPrefix)
			}
		}

	})

	t.Run("ReadEntriesFromFile fails", func(t *testing.T) {

		fileName := filepath.Join(hubServer.LogDir, constants.CleanFileName)
		err := CreateCleanupFile(hubServer.LogDir)
		if err != nil {
			t.Fatalf("Error creating dummy dir")
		}
		defer os.RemoveAll(fileName)

		req := &idl.CleanInitClusterRequest{}

		//Add entries to file
		lines := []string{}
		entry1 := fmt.Sprintf("%s %s", "sdw1", "/gpseg0")
		entry2 := fmt.Sprintf("%s %s", "sdw2", "/gpseg1")
		entry3 := fmt.Sprintf("%s %s", "sdw3", "/gpseg2")
		lines = append(lines, entry1, entry2, entry3)

		err = utils.CreateAppendLinesToFile(fileName, lines)
		if err != nil {
			t.Fatalf("error appending lines to file")
		}

		err = os.Chmod(fileName, 0200)
		if err != nil {
			t.Fatalf("error changing file permissions")
		}
		_, err = hubServer.CleanInitCluster(context.Background(), req)

		// Check error
		expectedErrPrefix := "init clean cluster failed err: open /tmp/logDir/ClusterInitCLeanup.txt: permission denied"
		if err != nil {
			if !strings.HasPrefix(err.Error(), expectedErrPrefix) {
				t.Fatalf("got %s, want %s", err.Error(), expectedErrPrefix)
			}
		}

	})

	t.Run("CreateHostDataDirMap fails", func(t *testing.T) {

		fileName := filepath.Join(hubServer.LogDir, constants.CleanFileName)
		err := CreateCleanupFile(hubServer.LogDir)
		if err != nil {
			t.Fatalf("Error creating dummy dir")
		}
		defer os.RemoveAll(fileName)

		req := &idl.CleanInitClusterRequest{}

		//Add invalid entries to file
		lines := []string{}
		entry1 := fmt.Sprintf("%s %s %s", "sdw1", "/gpseg0", "7000")
		entry2 := fmt.Sprintf("%s %s %s", "sdw2", "/gpseg1", "7001")
		entry3 := fmt.Sprintf("%s %s %s", "sdw3", "/gpseg2", "7002")
		lines = append(lines, entry1, entry2, entry3)

		err = utils.CreateAppendLinesToFile(fileName, lines)
		if err != nil {
			t.Fatalf("Error appending lines to file")
		}

		_, err = hubServer.CleanInitCluster(context.Background(), req)

		// Check error
		expectedErrPrefix := "invalid entries in cleanup file"
		if err != nil {
			if !strings.HasPrefix(err.Error(), expectedErrPrefix) {
				t.Fatalf("got %s, want %s", err.Error(), expectedErrPrefix)
			}
		}

	})

	t.Run("CleanInitCluster command fails", func(t *testing.T) {
		fileName := filepath.Join(hubServer.LogDir, constants.CleanFileName)
		err := CreateCleanupFile(hubServer.LogDir)
		if err != nil {
			t.Fatalf("Error creating dummy dir err: %v", err)
		}
		defer os.RemoveAll(fileName)

		req := &idl.CleanInitClusterRequest{}
		lines := []string{}
		entry1 := fmt.Sprintf("%s %s", "sdw1", "/gpseg0")
		entry2 := fmt.Sprintf("%s %s", "sdw2", "/gpseg1")
		entry3 := fmt.Sprintf("%s %s", "sdw3", "/gpseg2")
		lines = append(lines, entry1, entry2, entry3)

		err = utils.CreateAppendLinesToFile(fileName, lines)
		if err != nil {
			t.Fatalf("Error appending lines to file err: %v", err)
		}
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		sdw1 := mock_idl.NewMockAgentClient(ctrl)

		//Simulate removeDirectory error
		expectedErr := errors.New("test error")
		sdw1.EXPECT().RemoveDirectory(
			gomock.Any(),
			gomock.Any(),
		).Return(nil, expectedErr)

		agentConns := []*hub.Connection{
			{AgentClient: sdw1, Hostname: "sdw1"},
		}
		hubServer.Conns = agentConns
		_, err = hubServer.CleanInitCluster(context.Background(), req)

		if err == nil {
			t.Fatalf("unexpected error")
		}

		if !errors.Is(err, expectedErr) {
			t.Fatalf("got %v, want %v", err, expectedErr)
		}
	})

	t.Run("CleanInitCluster command succeeds", func(t *testing.T) {
		fileName := filepath.Join(hubServer.LogDir, constants.CleanFileName)
		err := CreateCleanupFile(hubServer.LogDir)
		if err != nil {
			t.Fatalf("Error creating dummy dir %v", err)
		}
		defer os.RemoveAll(fileName)

		req := &idl.CleanInitClusterRequest{}
		lines := []string{}
		entry1 := fmt.Sprintf("%s %s", "sdw1", "/gpseg0")
		entry2 := fmt.Sprintf("%s %s", "sdw2", "/gpseg1")
		entry3 := fmt.Sprintf("%s %s", "sdw3", "/gpseg2")
		lines = append(lines, entry1, entry2, entry3)

		err = utils.CreateAppendLinesToFile(fileName, lines)
		if err != nil {
			t.Fatalf("Error appending lines to file")
		}
		ctrl := gomock.NewController(t)
		defer ctrl.Finish()
		sdw1 := mock_idl.NewMockAgentClient(ctrl)

		sdw1.EXPECT().RemoveDirectory(
			gomock.Any(),
			&idl.RemoveDirectoryRequest{
				DataDirectory: "/gpseg0",
			},
		).Return(&idl.RemoveDirectoryReply{}, nil)

		sdw2 := mock_idl.NewMockAgentClient(ctrl)

		sdw2.EXPECT().RemoveDirectory(
			gomock.Any(),
			&idl.RemoveDirectoryRequest{
				DataDirectory: "/gpseg1",
			},
		).Return(&idl.RemoveDirectoryReply{}, nil)

		sdw3 := mock_idl.NewMockAgentClient(ctrl)

		sdw3.EXPECT().RemoveDirectory(
			gomock.Any(),
			&idl.RemoveDirectoryRequest{
				DataDirectory: "/gpseg2",
			},
		).Return(&idl.RemoveDirectoryReply{}, nil)

		agentConns := []*hub.Connection{
			{AgentClient: sdw1, Hostname: "sdw1"},
			{AgentClient: sdw2, Hostname: "sdw2"},
			{AgentClient: sdw3, Hostname: "sdw3"},
		}
		hubServer.Conns = agentConns
		_, err = hubServer.CleanInitCluster(context.Background(), req)
		// Check error
		if err != nil {
			t.Fatalf("unexpected error err: %v", err)
		}
	})

}
