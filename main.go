package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v3"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
)

type PackageListResponse struct {
	PackageNames []string `json:"packageNames"`
}

type PackageDetailsResponse struct {
	Package struct {
		Versions map[string]struct {
			Source struct {
				URL string `json:"url"`
			} `json:"source"`
		} `json:"versions"`
	} `json:"package"`
}

type ShopwareExtension struct {
	Store map[string]interface{} `yaml:"store"` // Use interface{} to maintain flexibility
	Build map[string]interface{} `yaml:"build"`
}

func main() {
	packageListURL := "https://packagist.org/packages/list.json?type=shopware-platform-plugin"

	// Fetch the list of packages
	resp, err := http.Get(packageListURL)
	if err != nil {
		log.Fatalf("Failed to fetch package list: %v", err)
	}
	defer resp.Body.Close()

	var packageList PackageListResponse
	if err := json.NewDecoder(resp.Body).Decode(&packageList); err != nil {
		log.Fatalf("Failed to decode package list: %v", err)
	}

	packageData := make(map[string]*ShopwareExtension)
	// Iterate through each package to get the repository URL
	for _, packageName := range packageList.PackageNames {
		detailsURL := fmt.Sprintf("https://packagist.org/packages/%s.json", packageName)
		detailsResp, err := http.Get(detailsURL)
		if err != nil {
			log.Printf("Failed to fetch details for package %s: %v", packageName, err)
			continue
		}
		defer detailsResp.Body.Close()

		var packageDetails PackageDetailsResponse
		if err := json.NewDecoder(detailsResp.Body).Decode(&packageDetails); err != nil {
			log.Printf("Failed to decode details for package %s: %v", packageName, err)
			continue
		}

		for _, version := range packageDetails.Package.Versions {
			log.Printf("Package: %s, Repository: %s\n", packageName, version.Source.URL)
			if strings.Contains(version.Source.URL, "github.com") {
				extension := checkShopwareExtensionFile(version.Source.URL)
				if extension != nil {
					packageData[packageName] = extension
				}
			}
			break // Assuming you want one repository URL per package
		}
	}

	// Write the map to a JSON file
	file, err := os.Create("shopware_extensions.json")
	if err != nil {
		log.Fatalf("Unable to create JSON file: %v", err)
	}
	defer file.Close()

	jsonEncoder := json.NewEncoder(file)
	jsonEncoder.SetIndent("", "  ")
	if err := jsonEncoder.Encode(packageData); err != nil {
		log.Fatalf("Failed to write JSON to file: %v", err)
	}

	log.Println("Shopware extensions data written to shopware_extensions.json")
}

func checkShopwareExtensionFile(repositoryURL string) *ShopwareExtension {
	// Extract the owner and repo from the GitHub URL
	parts := strings.Split(repositoryURL, "/")
	if len(parts) < 5 {
		log.Printf("Invalid GitHub URL: %s", repositoryURL)
		return nil
	}

	owner := parts[3]
	repo := parts[4]
	// Ensure to remove '.git' if it's included in the URL
	repo = strings.TrimSuffix(repo, ".git")

	// Use the GitHub API to fetch the file details
	githubAPIURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/.shopware-extension.yml", owner, repo)
	req, _ := http.NewRequest("GET", githubAPIURL, nil)

	token, ok := os.LookupEnv("GITHUB_TOKEN")
	if ok {
		req.Header.Set("Authorization", "token "+token)
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Failed to fetch .shopware-extension.yml file for %s/%s: %v", owner, repo, err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		log.Printf(".shopware-extension.yml found in %s/%s\n", owner, repo)

		var contentResponse struct {
			Content string `json:"content"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&contentResponse); err != nil {
			log.Printf("Failed to decode .shopware-extension.yml content response: %v", err)
			return nil
		}

		yamlContent, err := decodeBase64(contentResponse.Content)
		if err != nil {
			log.Printf("Failed to decode base64 content: %v", err)
			return nil
		}

		log.Printf(yamlContent)

		var extension ShopwareExtension
		if err := yaml.Unmarshal([]byte(yamlContent), &extension); err != nil {
			log.Printf("Failed to parse YAML: %v", err)
			return nil
		}

		log.Printf(".shopware-extension.yml found and parsed in %s/%s\n", owner, repo)
		return &extension
	} else if resp.StatusCode == http.StatusNotFound {
		log.Printf(".shopware-extension.yml not found in %s/%s\n", owner, repo)
	} else {
		log.Printf("Failed to fetch .shopware-extension.yml for %s/%s: %v", owner, repo, err)
	}

	return nil
}

func decodeBase64(content string) (string, error) {
	decodedContent, err := io.ReadAll(base64.NewDecoder(base64.StdEncoding, strings.NewReader(content)))
	if err != nil {
		return "", err
	}
	return string(decodedContent), nil
}
