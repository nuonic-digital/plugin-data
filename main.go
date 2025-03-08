package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
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

type ShopwareExtensionMetadata struct {
	RepositoryUrl    string `json:"repositoryUrl"`
	Ref              string `json:"ref"`
	LatestCommitTime int    `json:"latestCommitTime"`
}

type ShopwareExtensionIndex struct {
	Extensions  map[string]*ShopwareExtensionMetadata `json:"extensions"`
	GeneratedAt int                                   `json:"generatedAt"`
}

type GitHubClient struct {
	HTTPClient *http.Client
	Token      string
}

func (c *GitHubClient) FetchFile(owner, repo, filePath string) (*http.Response, error) {
	githubAPIURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/contents/%s", owner, repo, filePath)
	req, _ := http.NewRequest("GET", githubAPIURL, nil)

	if c.Token != "" {
		req.Header.Set("Authorization", "token "+c.Token)
	}

	return c.HTTPClient.Do(req)
}

func (c *GitHubClient) FetchLatestCommitTime(owner, repo string) (int, error) {
	githubAPIURL := fmt.Sprintf("https://api.github.com/repos/%s/%s/commits?per_page=1", owner, repo)
	req, _ := http.NewRequest("GET", githubAPIURL, nil)

	if c.Token != "" {
		req.Header.Set("Authorization", "token "+c.Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("failed to fetch commits: status code %d", resp.StatusCode)
	}

	var commits []struct {
		Commit struct {
			Author struct {
				Date string `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&commits); err != nil {
		return 0, err
	}

	if len(commits) == 0 {
		return 0, fmt.Errorf("no commits found")
	}

	commitTime, err := time.Parse(time.RFC3339, commits[0].Commit.Author.Date)
	if err != nil {
		return 0, err
	}

	return int(commitTime.Unix()), nil
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

	packageData := make(map[string]*ShopwareExtensionMetadata)
	// Initialize the GitHub client
	githubClient := &GitHubClient{
		HTTPClient: &http.Client{},
		Token:      os.Getenv("GITHUB_TOKEN"),
	}
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
				extension, commitTime := checkShopwareExtensionFile(version.Source.URL, githubClient)
				if extension {
					packageData[packageName] = &ShopwareExtensionMetadata{
						RepositoryUrl:    version.Source.URL,
						Ref:              detailsURL,
						LatestCommitTime: commitTime,
					}
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
	if err := jsonEncoder.Encode(ShopwareExtensionIndex{
		Extensions:  packageData,
		GeneratedAt: int(time.Now().Unix()),
	}); err != nil {
		log.Fatalf("Failed to write JSON to file: %v", err)
	}

	log.Println("Shopware extensions data written to shopware_extensions.json")
}

func checkShopwareExtensionFile(repositoryURL string, client *GitHubClient) (bool, int) {
	parts := strings.Split(repositoryURL, "/")
	if len(parts) < 5 {
		log.Printf("Invalid GitHub URL: %s", repositoryURL)
		return false, 0
	}

	owner := parts[3]
	repo := strings.TrimSuffix(parts[4], ".git")

	resp, err := client.FetchFile(owner, repo, ".shopware-extension.yml")
	if err != nil {
		log.Printf("Failed to fetch .shopware-extension.yml file for %s/%s: %v", owner, repo, err)
		return false, 0
	}
	defer resp.Body.Close()

	exists := resp.StatusCode == http.StatusOK
	if !exists && resp.StatusCode != http.StatusNotFound {
		log.Printf("Failed to fetch .shopware-extension.yml for %s/%s: %v", owner, repo, err)
	}

	commitTime, err := client.FetchLatestCommitTime(owner, repo)
	if err != nil {
		log.Printf("Failed to fetch latest commit time for %s/%s: %v", owner, repo, err)
	}

	return exists, commitTime
}
