package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/rah-0/rod"
	"github.com/rah-0/rod/lib/launcher"
	"github.com/rah-0/rod/lib/proto"
)

const managerURL = "http://127.0.0.1:7317"

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	product, err := exerciseManager(ctx, os.Getenv(launcher.ManagerTokenEnv))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(product)
}

func exerciseManager(ctx context.Context, authToken string) (product string, err error) {
	if authToken == "" {
		return "", fmt.Errorf("%s is empty", launcher.ManagerTokenEnv)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, managerURL, nil)
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		return "", fmt.Errorf("unauthenticated manager request returned %s", res.Status)
	}

	managed, err := launcher.NewManaged(ctx, managerURL, authToken)
	if err != nil {
		return "", err
	}
	client, err := managed.Context(ctx).Client()
	if err != nil {
		return "", err
	}

	browser := rod.New().Context(ctx).Client(client)
	if err := browser.Connect(); err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, browser.Close()) }()

	version, err := browser.Version()
	if err != nil {
		return "", err
	}
	if version.Product == "" {
		return "", fmt.Errorf("browser returned an empty product version")
	}

	page, err := browser.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		return "", err
	}
	if err := page.SetDocumentContent(`<main id="compatibility">latest Chromium</main>`); err != nil {
		return "", err
	}
	element, err := page.Element("#compatibility")
	if err != nil {
		return "", err
	}
	text, err := element.Text()
	if err != nil {
		return "", err
	}
	if text != "latest Chromium" {
		return "", fmt.Errorf("browser rendered %q", text)
	}

	return version.Product, nil
}
