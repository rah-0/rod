package main

import (
	"flag"
	"os"
	"testing"

	"github.com/rah-0/rod/lib/launcher"
)

func TestSecureManagerConfiguration(t *testing.T) {
	addressFlag := flag.Lookup("address")
	if addressFlag == nil || addressFlag.DefValue != defaultManagerAddress {
		t.Fatalf("default address = %v, want %s", addressFlag, defaultManagerAddress)
	}
	if flag.Lookup("allow-all") != nil {
		t.Fatal("the unsafe allow-all flag is registered")
	}
	remoteFlag := flag.Lookup("allow-plaintext-remote")
	if remoteFlag == nil || remoteFlag.DefValue != "false" {
		t.Fatal("plaintext remote access is not opt-in")
	}

	for _, address := range []string{"127.0.0.1:7317", "localhost:7317", "[::1]:7317"} {
		loopback, err := configuredAddressIsLoopback(address)
		if err != nil || !loopback {
			t.Fatalf("loopback address %q was rejected: %v", address, err)
		}
	}
	for _, address := range []string{":7317", "0.0.0.0:7317", "[::]:7317", "192.0.2.1:7317"} {
		loopback, err := configuredAddressIsLoopback(address)
		if err != nil || loopback {
			t.Fatalf("non-loopback address %q was accepted: %v", address, err)
		}
	}
	if _, err := configuredAddressIsLoopback("invalid"); err == nil {
		t.Fatal("invalid address was accepted")
	}

	t.Setenv(launcher.ManagerTokenEnv, "")
	if _, ok := takeManagerToken(); ok {
		t.Fatal("an empty manager token was accepted")
	}
	if _, ok := os.LookupEnv(launcher.ManagerTokenEnv); ok {
		t.Fatal("empty manager token remained in the process environment")
	}

	const token = "test-manager-token"
	t.Setenv(launcher.ManagerTokenEnv, token)
	got, ok := takeManagerToken()
	if !ok || got != token {
		t.Fatal("configured manager token was not loaded")
	}
	if _, ok := os.LookupEnv(launcher.ManagerTokenEnv); ok {
		t.Fatal("manager token remained in the process environment")
	}
}
