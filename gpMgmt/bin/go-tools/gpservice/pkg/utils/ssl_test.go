package utils_test

import (
	"os"
	"os/exec"
	"testing"

	"github.com/greenplum-db/gpdb/gpservice/constants"
	"github.com/greenplum-db/gpdb/gpservice/pkg/utils"
)

func TestLoadServerCredentials(t *testing.T) {
	out, err := exec.Command(constants.ShellPath, "-c", "../../generate_test_tls_certificates.sh `hostname`").CombinedOutput()
	if err != nil {
		t.Fatalf("Cannot generate test certificates: %v, stdErr:%s", err, string(out))
	}

	t.Run("successfully parses good certificate files", func(t *testing.T) {
		creds := &utils.GpCredentials{
			CACertPath:     "./certificates/ca-cert.pem",
			ServerCertPath: "./certificates/server-cert.pem",
			ServerKeyPath:  "./certificates/server-key.pem",
			TlsEnabled:     true,
		}
		_, err := creds.LoadServerCredentials()
		if err != nil {
			t.Errorf("unexpected error %v", err)
		}
		// TODO: What's a good way to check a "good" certificate?
	})
	t.Run("fails to parse a bad certificate file", func(t *testing.T) {
		creds := &utils.GpCredentials{
			CACertPath:     "./certificates/ca-cert.pem",
			ServerCertPath: "./certificates/server-cert.pem",
			ServerKeyPath:  "./certificates/server-key.pem",
			TlsEnabled:     true,
		}
		creds.ServerCertPath = "/dev/null"
		_, err := creds.LoadServerCredentials()
		if err == nil {
			t.Fatalf("expected TLS error, did not receive one")
		}
		if err.Error() != "could not load server credentials: tls: failed to find any PEM data in certificate input" {
			t.Errorf("expected TLS error, got %v", err)
		}
	})

	err = os.RemoveAll("./certificates")
	if err != nil {
		t.Fatalf("Cannot remove test certificates: %v", err)
	}
}

func TestLoadClientCredentials(t *testing.T) {
	out, err := exec.Command(constants.ShellPath, "-c", "../../generate_test_tls_certificates.sh `hostname`").CombinedOutput()
	if err != nil {
		t.Fatalf("Cannot generate test certificates: %v, stderror:%v", err, string(out))
	}

	t.Run("successfully parses good certificate files", func(t *testing.T) {
		creds := &utils.GpCredentials{
			CACertPath:     "./certificates/ca-cert.pem",
			ServerCertPath: "./certificates/server-cert.pem",
			ServerKeyPath:  "./certificates/server-key.pem",
			TlsEnabled:     true,
		}
		_, err := creds.LoadClientCredentials()
		if err != nil {
			t.Errorf("unexpected error %v", err)
		}
		// TODO: What's a good way to check a "good" certificate?
	})
	t.Run("fails to parse a bad certificate file", func(t *testing.T) {
		creds := &utils.GpCredentials{
			CACertPath:     "./certificates/ca-cert.pem",
			ServerCertPath: "./certificates/server-cert.pem",
			ServerKeyPath:  "./certificates/server-key.pem",
			TlsEnabled:     true,
		}
		creds.CACertPath = "/dev/null"
		_, err := creds.LoadClientCredentials()
		if err == nil {
			t.Fatalf("expected TLS error, did not receive one")
		}
		if err.Error() != "failed to add server CA's certificate" {
			t.Errorf("expected TLS error, got %v", err)
		}
	})

	err = os.RemoveAll("./certificates")
	if err != nil {
		t.Fatalf("Cannot remove test certificates: %v", err)
	}
}
