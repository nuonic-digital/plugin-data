package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
)

type PackageListResponse struct {
	PackageNames []string `json:"packageNames"`
}

type PackageVersion struct {
	Source struct {
		URL string `json:"url"`
	} `json:"source"`
	License *[]string `json:"license"`
	Time    string    `json:"time"`
}

type PackageDetails struct {
	Data PackageDetailsResponse
	URL  string
}
type PackageDetailsResponse struct {
	Package struct {
		Versions map[string]PackageVersion `json:"versions"`
	} `json:"package"`
}

func (d *PackageDetailsResponse) GetLatestVersion() *PackageVersion {
	var latestVersion *PackageVersion
	var latestTime time.Time

	for _, versionData := range d.Package.Versions {
		parsedTime, err := time.Parse(time.RFC3339, versionData.Time)
		if err != nil {
			log.Printf("Failed to parse time for package version: %s, error: %v", versionData.Time, err)
			continue
		}

		if latestVersion == nil || parsedTime.After(latestTime) {
			latestTime = parsedTime
			latestVersion = &versionData
		}
	}
	return latestVersion
}

type ShopwareExtensionMetadata struct {
	RepositoryUrl            string `json:"repositoryUrl"`
	Ref                      string `json:"ref"`
	LatestCommitTime         int    `json:"latestCommitTime"`
	AdditionalMetadataExists bool   `json:"additionalMetadataExists"`
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

func (c *GitHubClient) GetLatestCommitTime(repositoryURL string) (bool, *int) {
	parts := strings.Split(repositoryURL, "/")
	if len(parts) < 5 {
		log.Printf("Invalid GitHub URL: %s", repositoryURL)
		return false, nil
	}

	owner := parts[3]
	repo := strings.TrimSuffix(parts[4], ".git")

	resp, err := c.FetchFile(owner, repo, ".shopware-extension.yml")
	if err != nil {
		log.Printf("Failed to fetch .shopware-extension.yml file for %s/%s: %v", owner, repo, err)
		return false, nil
	}
	defer resp.Body.Close()

	exists := resp.StatusCode == http.StatusOK
	if !exists && resp.StatusCode != http.StatusNotFound {
		log.Printf("Failed to fetch .shopware-extension.yml for %s/%s: %v", owner, repo, err)
	}

	commitTime, err := c.FetchLatestCommitTime(owner, repo)
	if err != nil {
		log.Printf("Failed to fetch latest commit time for %s/%s: %v", owner, repo, err)
		return false, nil
	}

	return exists, &commitTime
}

func FetchPackageDetails(packageName string) (*PackageDetails, error) {
	detailsURL := fmt.Sprintf("https://packagist.org/packages/%s.json", packageName)
	detailsResp, err := http.Get(detailsURL)
	if err != nil {
		return nil, errors.New(fmt.Sprintf("Failed to fetch details for package %s: %v", packageName, err))
	}
	defer detailsResp.Body.Close()

	var packageDetails PackageDetailsResponse
	if err := json.NewDecoder(detailsResp.Body).Decode(&packageDetails); err != nil {
		return nil, errors.New(fmt.Sprintf("Failed to decode details for package %s: %v", packageName, err))
	}

	return &PackageDetails{
		Data: packageDetails,
		URL:  detailsURL,
	}, nil
}

func filter[T any](ss []T, test func(T) bool) (ret []T) {
	for _, s := range ss {
		if test(s) {
			ret = append(ret, s)
		}
	}
	return
}

var githubClient *GitHubClient

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

	var packageNameBlacklist = []string{"tinect/platform-html-minify"}
	packageList.PackageNames = filter(packageList.PackageNames, func(s string) bool {
		return !slices.Contains(packageNameBlacklist, s)
	})

	// Initialize the GitHub client
	githubClient = &GitHubClient{
		HTTPClient: &http.Client{},
		Token:      os.Getenv("GITHUB_TOKEN"),
	}

	var wg sync.WaitGroup

	workerCount := 10
	jobCount := len(packageList.PackageNames)

	jobs := make(chan Job, jobCount)
	results := make(chan Result, jobCount)

	wg.Add(workerCount)
	for w := 1; w <= workerCount; w++ {
		go worker(jobs, results, &wg)
	}

	var resultsWg sync.WaitGroup
	resultsWg.Add(1)
	go collectResults(results, &resultsWg)

	for _, packageName := range packageList.PackageNames {
		jobs <- Job{
			PackageName: packageName,
		}
	}
	close(jobs)
	wg.Wait()
	close(results)
	resultsWg.Wait()
}

type Job struct {
	PackageName string
}

type Result struct {
	Data *PackageData
}

func collectResults(results chan Result, wg *sync.WaitGroup) {
	defer wg.Done()

	packageData := make(map[string]*ShopwareExtensionMetadata)
	for result := range results {
		if result.Data != nil {
			packageData[result.Data.PackageName] = result.Data.Metadata
		}
	}

	// Write the map to a JSON file
	writeShopwareExtensionJson(packageData)
	writeShopwareExtensionListingHtml(packageData)
}

func writeShopwareExtensionListingHtml(packageData map[string]*ShopwareExtensionMetadata) {
	file, err := os.Create("index.html")
	if err != nil {
		log.Fatalf("Unable to create index.html file: %v", err)
	}
	defer file.Close()

	tmpl, err := template.New("index_html").Parse(`
	<!DOCTYPE html>
	<html lang="en">
	<head>
		<meta charset="UTF-8">
		<meta name="viewport" content="width=device-width, initial-scale=1.0">
		<title>Shopware Extensions</title>
	</head>
	<body>
		<h1>Shopware Extensions</h1>
		<p>Generated at: {{ .GeneratedAt }}</p>
		<table>
			<thead>
				<tr>
					<th>Package</th>
					<th>Repository URL</th>
					<th>Latest Commit Time</th>
				</tr>
 			</thead>
			<tbody>
				{{ range $packageName, $metadata := .Extensions }}
					<tr>
						<td>{{ $packageName }}</td>
						<td><a href="{{ $metadata.RepositoryUrl }}">{{ $metadata.RepositoryUrl }}</a></td>
						<td>{{ $metadata.LatestCommitTime }}</td>
					</tr>
				{{ end }}
			</tbody>
		</table>
	</body>
`)

	if tmpl == nil {
		log.Fatalf("Failed to parse HTML template: %v", err)
	}

	if err = tmpl.Execute(file, ShopwareExtensionIndex{
		Extensions:  packageData,
		GeneratedAt: int(time.Now().Unix()),
	}); err != nil {
		log.Fatalf("Failed to write HTML to file: %v", err)
	}

	log.Println("Shopware extensions data written to index.html")
}

func writeShopwareExtensionJson(packageData map[string]*ShopwareExtensionMetadata) {
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

func worker(jobs <-chan Job, results chan<- Result, wg *sync.WaitGroup) {
	defer wg.Done()
	for job := range jobs {
		results <- Result{
			Data: ProcessPackage(job.PackageName),
		}
	}
}

type PackageData struct {
	PackageName string
	Metadata    *ShopwareExtensionMetadata
}

func ProcessPackage(packageName string) *PackageData {
	packageDetails, err := FetchPackageDetails(packageName)
	if err != nil {
		log.Printf("%v", err)
		return nil
	}

	latestVersion := packageDetails.Data.GetLatestVersion()
	if latestVersion == nil {
		return nil
	}

	log.Printf("Package: %s, Repository: %s\n", packageName, latestVersion.Source.URL)
	if !strings.Contains(latestVersion.Source.URL, "github.com") {
		return nil
	}

	if latestVersion.License == nil || slices.Contains(*latestVersion.License, "proprietary") {
		log.Printf("Skipping proprietary package: %s", packageName)
		return nil
	}

	exists, commitTime := githubClient.GetLatestCommitTime(latestVersion.Source.URL)
	if commitTime == nil {
		return nil
	}

	return &PackageData{
		PackageName: packageName,
		Metadata: &ShopwareExtensionMetadata{
			RepositoryUrl:            latestVersion.Source.URL,
			Ref:                      packageDetails.URL,
			LatestCommitTime:         *commitTime,
			AdditionalMetadataExists: exists,
		},
	}
}
