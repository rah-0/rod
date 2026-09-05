package docker

import (
	"context"
	"crypto/rand"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/rah-0/rod/lib/launcher"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	chromiumImage       = "docker.io/chromedp/headless-shell:latest"
	managerBinaryInTest = "/usr/local/bin/rod-manager"
	clientBinaryInTest  = "/usr/local/bin/rod-manager-client"
)

func TestManagerImage(t *testing.T) {
	// This test owns one container and registers its cleanup below.
	t.Setenv("TESTCONTAINERS_RYUK_DISABLED", "true")

	if testing.Short() {
		t.Skip("skipping container integration test")
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		t.Skipf("%s does not publish a linux/%s image", chromiumImage, runtime.GOARCH)
	}
	testcontainers.SkipIfProviderIsNotHealthy(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	authToken := rand.Text() + rand.Text()

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	managerBinary := buildBinary(t, ctx, repoRoot, "./lib/launcher/rod-manager", "rod-manager")
	clientBinary := buildBinary(t, ctx, ".", "./fixtures/manager-client", "rod-manager-client")

	managerContainer, err := testcontainers.Run(
		ctx,
		chromiumImage,
		testcontainers.WithAlwaysPull(),
		testcontainers.WithImagePlatform("linux/"+runtime.GOARCH),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath:      managerBinary,
			ContainerFilePath: managerBinaryInTest,
			FileMode:          0o755,
		}, testcontainers.ContainerFile{
			HostFilePath:      clientBinary,
			ContainerFilePath: clientBinaryInTest,
			FileMode:          0o755,
		}),
		testcontainers.WithEntrypoint(managerBinaryInTest),
		testcontainers.WithEnv(map[string]string{launcher.ManagerTokenEnv: authToken}),
		testcontainers.WithCmd("-rod=bin=/headless-shell/headless-shell"),
		testcontainers.WithWaitStrategy(wait.ForLog("[rod-manager] listening on:")),
	)
	testcontainers.CleanupContainer(t, managerContainer)
	if err != nil {
		t.Fatal(err)
	}

	exitCode, outputReader, err := managerContainer.Exec(
		ctx,
		[]string{clientBinaryInTest},
		tcexec.Multiplexed(),
	)
	if err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(outputReader)
	if err != nil {
		t.Fatal(err)
	}
	if exitCode != 0 {
		t.Fatalf("container client exited with code %d: %s", exitCode, output)
	}
	product := strings.TrimSpace(string(output))
	if product == "" {
		t.Fatal("browser returned an empty product version")
	}
	t.Logf("tested %s from %s", product, chromiumImage)
}

func buildBinary(t *testing.T, ctx context.Context, dir, pkg, name string) string {
	t.Helper()

	binary := filepath.Join(t.TempDir(), name)
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-o", binary, pkg)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH="+runtime.GOARCH,
		"GOWORK=off",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", name, err, output)
	}

	return binary
}
