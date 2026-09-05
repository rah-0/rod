// A server to help launch browser remotely
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rah-0/rod/lib/defaults"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/utils"
)

const defaultManagerAddress = "127.0.0.1:7317"

var (
	addr                 = flag.String("address", defaultManagerAddress, "the address to listen to")
	quiet                = flag.Bool("quiet", false, "silence the log")
	allowPlaintextRemote = flag.Bool("allow-plaintext-remote", false, "allow a non-loopback listener without TLS")
)

func main() {
	defaults.Load()
	flag.Parse()

	authToken, hasAuthToken := takeManagerToken()
	if !hasAuthToken {
		log.Fatal("[rod-manager] ROD_MANAGER_TOKEN must be set")
	}
	loopback, err := configuredAddressIsLoopback(*addr)
	if err != nil {
		utils.E(err)
	}
	if !loopback && !*allowPlaintextRemote {
		log.Fatal("[rod-manager] non-loopback address requires -allow-plaintext-remote")
	}

	m := launcher.NewManager(authToken)

	if !*quiet {
		m.Logger = log.New(os.Stdout, "", 0)
	}

	l, err := net.Listen("tcp", *addr)
	if err != nil {
		utils.E(err)
	}

	if !*quiet {
		fmt.Println("[rod-manager] listening on:", l.Addr().String())
	}

	srv := &http.Server{
		Handler:           m,
		ReadHeaderTimeout: 10 * time.Second,
	}
	utils.E(srv.Serve(l))
}

func takeManagerToken() (string, bool) {
	authToken := os.Getenv(launcher.ManagerTokenEnv)
	utils.E(os.Unsetenv(launcher.ManagerTokenEnv))
	return authToken, authToken != ""
}

func configuredAddressIsLoopback(address string) (bool, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false, err
	}
	if strings.EqualFold(host, "localhost") {
		return true, nil
	}
	return net.ParseIP(host).IsLoopback(), nil
}
