package utils

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

var (
	System              = InitializeSystemFunctions()
	ExecuteAndGetUlimit = ExecuteAndGetUlimitFn
)

type SystemFunctions struct {
	CurrentUser        func() (*user.User, error)
	InterfaceAddrs     func() ([]net.Addr, error)
	Open               func(name string) (*os.File, error)
	OpenFile           func(name string, flag int, perm os.FileMode) (*os.File, error)
	Create             func(name string) (*os.File, error)
	WriteFile          func(name string, data []byte, perm fs.FileMode) error
	ExecCommand        func(name string, arg ...string) *exec.Cmd
	ExecCommandContext func(ctx context.Context, name string, arg ...string) *exec.Cmd
	Getuid             func() int
	Stat               func(name string) (os.FileInfo, error)
	Getgid             func() int
	Remove             func(name string) error
	RemoveAll          func(path string) error
	ReadFile           func(name string) ([]byte, error)
	GetHostName        func() (name string, err error)
	OSExit             func(code int)
}

func InitializeSystemFunctions() *SystemFunctions {
	return &SystemFunctions{
		CurrentUser:        user.Current,
		InterfaceAddrs:     net.InterfaceAddrs,
		Open:               os.Open,
		OpenFile:           os.OpenFile,
		Create:             os.Create,
		WriteFile:          os.WriteFile,
		ExecCommand:        exec.Command,
		ExecCommandContext: exec.CommandContext,
		Getuid:             os.Geteuid,
		Stat:               os.Stat,
		Getgid:             os.Getgid,
		Remove:             os.Remove,
		RemoveAll:          os.RemoveAll,
		ReadFile:           os.ReadFile,
		GetHostName:        os.Hostname,
		OSExit:             os.Exit,
	}
}

func ResetSystemFunctions() {
	System = InitializeSystemFunctions()
}

/*
WriteLinesToFile creates a new file with the given contents.
If a file with the name already exists, overwrites the file with new contents.
Takes lines to be written as input and updates to the file by adding \n's.
*/
func WriteLinesToFile(filename string, lines []string) error {
	file, err := System.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(strings.Join(lines, "\n"))
	if err != nil {
		return err
	}

	return nil
}

/*
AppendLinesToFile appends the lines to an existing file.
*/
func AppendLinesToFile(filename string, lines []string) error {
	file, err := System.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString("\n" + strings.Join(lines, "\n"))
	if err != nil {
		return err
	}

	return nil
}

/*
 * Create file if it does not exists.
 * If file exists then append the lines to the existing file.
 */
func CreateAppendLinesToFile(filename string, lines []string) error {
	_, err := System.Stat(filename)

	//Create file if it doesnt exist
	if err != nil {
		_, err := System.Create(filename)
		if err != nil {
			return err
		}
	}

	file, err := System.OpenFile(filename, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.WriteString(strings.Join(lines, "\n"))
	if err != nil {
		return err
	}

	// Add /n to the end of the file
	_, err = file.WriteString("\n")
	if err != nil {
		return err
	}

	return nil
}

func GetHostAddrsNoLoopback() ([]string, error) {
	var addrs []string
	ipAddresses, err := System.InterfaceAddrs()
	if err != nil {
		return nil, err
	}

	for _, ip := range ipAddresses {
		if ipnet, ok := ip.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			addrs = append(addrs, ip.String())
		}
	}

	return addrs, nil
}

/*
GetListDifference returns all the elements present in listA but not listB
*/
func GetListDifference(listA, listB []string) []string {
	var result []string
	m := make(map[string]bool)

	for _, item := range listB {
		m[item] = true
	}

	for _, item := range listA {
		if _, ok := m[item]; !ok {
			result = append(result, item)
		}
	}

	return result
}

func ExecuteAndGetUlimitFn() (int, error) {
	out, err := System.ExecCommand("ulimit", "-n").CombinedOutput()
	if err != nil {
		return -1, fmt.Errorf("error fetching open file limit values:%v", err)
	}

	ulimitVal, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return -1, fmt.Errorf("could not convert the ulimit value: %v", err)
	}
	return ulimitVal, nil
}

/*
GetAllAddresses returns list of all IP addresses for all interfaces for the host
Adds 0.0.0 and "::" to the list
IPV6 addresses are appended with %interface-name to make them routable
*/
func GetAllAddresses() (ipList []string, err error) {
	ipList = []string{"", "0.0.0.0", "::"}
	// Get a list of network interfaces and their addresses
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("error getting list of interfaces to check port in use:%v", err)
	}
	// Get addresses for each interface
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {

			return nil, fmt.Errorf("error getting list of addresses for interface %s. Error:%v", iface.Name, err)
		}
		for _, addr := range addrs {
			switch v := addr.(type) {
			case *net.IPNet:
				ip := v.IP
				if ip != nil {
					if ip.To4() != nil {
						// if it's an IPv4 address
						ipList = append(ipList, ip.String())
					} else {
						// it's an IPv6 address, append interface name to make it routable
						ipList = append(ipList, fmt.Sprintf("%s%%%s", ip.String(), iface.Name))
					}
				}
			}

		}
	}
	return ipList, nil
}

/*
CheckIfPortFree returns error if port is not available otherwise returns nil
*/
func CheckIfPortFree(ip string, port string) (bool, error) {
	if ip == "" {
		// to detect if socket created without specifying hostname
		// listen fails to detect when the netcat ran without hostname
		_, err := net.Dial("tcp", net.JoinHostPort(ip, port))
		if err != nil {
			return true, nil
		} else {
			return false, fmt.Errorf("able to dial on port: %s, port is already open", port)
		}
	}
	listener, err := net.Listen("tcp", net.JoinHostPort(ip, port))
	if err != nil {
		return false, err
	}
	defer listener.Close()
	return true, nil
}

/* Function to read entries from a givem file and return the list of strings
 */
func ReadEntriesFromFile(filename string) ([]string, error) {
	// Open the file for reading
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var entries []string

	// Read the file line by line
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		entries = append(entries, line)
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

/*
 * Remove the contents of the directory
 */
func RemoveDirContents(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil && err != io.EOF {
		return err
	}
	for _, name := range names {
		err = os.RemoveAll(filepath.Join(dir, name))
		if err != nil {
			return err
		}
	}
	return nil
}
