// Package github provides a client for interacting with the GitHub API,
// including fetching authenticated user information and contribution data.
package github

import (
	"fmt"
	"strings"
	"time"

	"github.com/github/gh-skyline/internal/errors"
	"github.com/github/gh-skyline/internal/types"
)

// APIClient interface defines the methods we need from the client
type APIClient interface {
	Do(query string, variables map[string]interface{}, response interface{}) error
}

// Client holds the API client
type Client struct {
	api APIClient
}

// NewClient creates a new GitHub client
func NewClient(apiClient APIClient) *Client {
	return &Client{api: apiClient}
}

// GetAuthenticatedUser fetches the authenticated user's login name from GitHub.
func (c *Client) GetAuthenticatedUser() (string, error) {
	// GraphQL query to fetch the authenticated user's login.
	query := `
    query {
        viewer {
            login
        }
    }`

	var response struct {
		Viewer struct {
			Login string `json:"login"`
		} `json:"viewer"`
	}

	// Execute the GraphQL query.
	err := c.api.Do(query, nil, &response)
	if err != nil {
		return "", errors.New(errors.NetworkError, "failed to fetch authenticated user", err)
	}

	if response.Viewer.Login == "" {
		return "", errors.New(errors.ValidationError, "received empty username from GitHub API", nil)
	}

	return response.Viewer.Login, nil
}

// FetchContributions retrieves the contribution data for a given username and year from GitHub.
func (c *Client) FetchContributions(username string, year int) (*types.ContributionsResponse, error) {
	if username == "" {
		return nil, errors.New(errors.ValidationError, "username cannot be empty", nil)
	}

	if year < 2008 {
		return nil, errors.New(errors.ValidationError, "year cannot be before GitHub's launch (2008)", nil)
	}

	startDate := fmt.Sprintf("%d-01-01T00:00:00Z", year)
	endDate := fmt.Sprintf("%d-12-31T23:59:59Z", year)

	// GraphQL query to fetch the user's contributions within the specified date range.
	query := `
    query ContributionGraph($username: String!, $from: DateTime!, $to: DateTime!) {
        user(login: $username) {
            login
            contributionsCollection(from: $from, to: $to) {
                contributionCalendar {
                    totalContributions
                    weeks {
                        contributionDays {
                            contributionCount
                            date
                        }
                    }
                }
            }
        }
    }`

	variables := map[string]interface{}{
		"username": username,
		"from":     startDate,
		"to":       endDate,
	}

	var response types.ContributionsResponse

	// Execute the GraphQL query.
	err := c.api.Do(query, variables, &response)
	if err != nil {
		return nil, errors.New(errors.NetworkError, "failed to fetch contributions", err)
	}

	if response.User.Login == "" {
		return nil, errors.New(errors.ValidationError, "received empty username from GitHub API", nil)
	}

	return &response, nil
}

// FetchOrgContributions retrieves contribution data filtered by organization for a given username and year.
//
// GitHub's GraphQL API limits contributionsByRepository queries to 100 repositories per request.
// To work around this, we query each quarter separately and merge the results. This allows us to
// capture contributions across up to 400 repos per year (100 per quarter), which covers virtually
// all real-world usage patterns. The quarterly approach also reduces the chance of hitting the
// per-repo contribution limit (100 nodes) since contributions are spread across shorter time ranges.
func (c *Client) FetchOrgContributions(username string, org string, year int) (*types.OrgContributionsResponse, error) {
	if username == "" {
		return nil, errors.New(errors.ValidationError, "username cannot be empty", nil)
	}
	if org == "" {
		return nil, errors.New(errors.ValidationError, "org cannot be empty", nil)
	}
	if year < 2008 {
		return nil, errors.New(errors.ValidationError, "year cannot be before GitHub's launch (2008)", nil)
	}

	// Query each quarter separately to work around the 100 repo limit per query.
	// Each query can return up to 100 unique repos, so quarterly queries give us up to 400 repos/year.
	quarters := []struct {
		start string
		end   string
	}{
		{fmt.Sprintf("%d-01-01T00:00:00Z", year), fmt.Sprintf("%d-03-31T23:59:59Z", year)},
		{fmt.Sprintf("%d-04-01T00:00:00Z", year), fmt.Sprintf("%d-06-30T23:59:59Z", year)},
		{fmt.Sprintf("%d-07-01T00:00:00Z", year), fmt.Sprintf("%d-09-30T23:59:59Z", year)},
		{fmt.Sprintf("%d-10-01T00:00:00Z", year), fmt.Sprintf("%d-12-31T23:59:59Z", year)},
	}

	query := `
    query ContributionsByRepo($username: String!, $from: DateTime!, $to: DateTime!) {
        user(login: $username) {
            login
            contributionsCollection(from: $from, to: $to) {
                commitContributionsByRepository(maxRepositories: 100) {
                    repository {
                        name
                        owner { login }
                    }
                    contributions(first: 100) {
                        totalCount
                        nodes { occurredAt }
                    }
                }
                issueContributionsByRepository(maxRepositories: 100) {
                    repository { owner { login } }
                    contributions(first: 100) { nodes { occurredAt } }
                }
                pullRequestContributionsByRepository(maxRepositories: 100) {
                    repository { owner { login } }
                    contributions(first: 100) { nodes { occurredAt } }
                }
                pullRequestReviewContributionsByRepository(maxRepositories: 100) {
                    repository { owner { login } }
                    contributions(first: 100) { nodes { occurredAt } }
                }
            }
        }
    }`

	var merged types.OrgContributionsResponse

	for _, q := range quarters {
		variables := map[string]interface{}{
			"username": username,
			"from":     q.start,
			"to":       q.end,
		}

		var response types.OrgContributionsResponse
		err := c.api.Do(query, variables, &response)
		if err != nil {
			return nil, errors.New(errors.NetworkError, "failed to fetch org contributions", err)
		}

		if response.User.Login == "" {
			return nil, errors.New(errors.ValidationError, "received empty username from GitHub API", nil)
		}

		// Merge quarterly results into the combined response
		merged.User.Login = response.User.Login
		merged.User.ContributionsCollection.CommitContributionsByRepository = append(
			merged.User.ContributionsCollection.CommitContributionsByRepository,
			response.User.ContributionsCollection.CommitContributionsByRepository...)
		merged.User.ContributionsCollection.IssueContributionsByRepository = append(
			merged.User.ContributionsCollection.IssueContributionsByRepository,
			response.User.ContributionsCollection.IssueContributionsByRepository...)
		merged.User.ContributionsCollection.PullRequestContributionsByRepository = append(
			merged.User.ContributionsCollection.PullRequestContributionsByRepository,
			response.User.ContributionsCollection.PullRequestContributionsByRepository...)
		merged.User.ContributionsCollection.PullRequestReviewContributionsByRepository = append(
			merged.User.ContributionsCollection.PullRequestReviewContributionsByRepository,
			response.User.ContributionsCollection.PullRequestReviewContributionsByRepository...)
	}

	return &merged, nil
}

// GetUserID fetches a user's GitHub node ID, required for author filtering in commit queries.
func (c *Client) GetUserID(username string) (string, error) {
	if username == "" {
		return "", errors.New(errors.ValidationError, "username cannot be empty", nil)
	}

	query := `
    query UserID($username: String!) {
        user(login: $username) {
            id
        }
    }`

	variables := map[string]interface{}{
		"username": username,
	}

	var response types.UserIDResponse
	err := c.api.Do(query, variables, &response)
	if err != nil {
		return "", errors.New(errors.NetworkError, "failed to fetch user ID", err)
	}

	if response.User.ID == "" {
		return "", errors.New(errors.ValidationError, "received empty user ID from GitHub API", nil)
	}

	return response.User.ID, nil
}

// FetchOrgRepoContributions retrieves contributions by querying each repository in the organization.
// This method is used when querying another user's contributions to private org repos,
// as the contributionsCollection API only exposes public contributions for non-self queries.
func (c *Client) FetchOrgRepoContributions(username string, org string, year int) (map[string]int, error) {
	if username == "" {
		return nil, errors.New(errors.ValidationError, "username cannot be empty", nil)
	}
	if org == "" {
		return nil, errors.New(errors.ValidationError, "org cannot be empty", nil)
	}
	if year < 2008 {
		return nil, errors.New(errors.ValidationError, "year cannot be before GitHub's launch (2008)", nil)
	}

	userID, err := c.GetUserID(username)
	if err != nil {
		return nil, err
	}

	repos, err := c.fetchOrgRepos(org)
	if err != nil {
		return nil, err
	}

	startDate := fmt.Sprintf("%d-01-01T00:00:00Z", year)
	endDate := fmt.Sprintf("%d-12-31T23:59:59Z", year)

	dailyCounts := make(map[string]int)

	err = c.fetchRepoCommitDatesBatched(org, repos, userID, startDate, endDate, dailyCounts)
	if err != nil {
		return nil, err
	}

	return dailyCounts, nil
}

func (c *Client) fetchOrgRepos(org string) ([]string, error) {
	query := `
    query OrgRepos($org: String!, $cursor: String) {
        organization(login: $org) {
            repositories(first: 100, after: $cursor) {
                pageInfo {
                    hasNextPage
                    endCursor
                }
                nodes {
                    name
                    defaultBranchRef {
                        name
                    }
                }
            }
        }
    }`

	var repos []string
	var cursor *string

	for {
		variables := map[string]interface{}{
			"org":    org,
			"cursor": cursor,
		}

		var response types.OrgReposResponse
		err := c.api.Do(query, variables, &response)
		if err != nil {
			return nil, errors.New(errors.NetworkError, "failed to fetch org repositories", err)
		}

		for _, repo := range response.Organization.Repositories.Nodes {
			if repo.DefaultBranchRef != nil {
				repos = append(repos, repo.Name)
			}
		}

		if !response.Organization.Repositories.PageInfo.HasNextPage {
			break
		}
		cursor = &response.Organization.Repositories.PageInfo.EndCursor
	}

	return repos, nil
}

func (c *Client) fetchRepoCommitDates(org, repoName, userID, startDate, endDate string, dailyCounts map[string]int) error {
	query := `
    query RepoCommits($owner: String!, $name: String!, $authorID: ID!, $since: GitTimestamp!, $until: GitTimestamp!, $cursor: String) {
        repository(owner: $owner, name: $name) {
            defaultBranchRef {
                target {
                    ... on Commit {
                        history(first: 100, after: $cursor, author: {id: $authorID}, since: $since, until: $until) {
                            pageInfo {
                                hasNextPage
                                endCursor
                            }
                            nodes {
                                committedDate
                            }
                        }
                    }
                }
            }
        }
    }`

	var cursor *string

	for {
		variables := map[string]interface{}{
			"owner":    org,
			"name":     repoName,
			"authorID": userID,
			"since":    startDate,
			"until":    endDate,
			"cursor":   cursor,
		}

		var response types.RepoCommitsResponse
		err := c.api.Do(query, variables, &response)
		if err != nil {
			return err
		}

		if response.Repository.DefaultBranchRef == nil {
			break
		}

		history := response.Repository.DefaultBranchRef.Target.History
		for _, node := range history.Nodes {
			if len(node.CommittedDate) >= 10 {
				dateStr := node.CommittedDate[:10]
				dailyCounts[dateStr]++
			}
		}

		if !history.PageInfo.HasNextPage {
			break
		}
		cursor = &history.PageInfo.EndCursor
	}

	return nil
}

const batchSize = 10

func (c *Client) fetchRepoCommitDatesBatched(org string, repos []string, userID, startDate, endDate string, dailyCounts map[string]int) error {
	reposNeedingPagination := make(map[string]bool)

	for i := 0; i < len(repos); i += batchSize {
		end := i + batchSize
		if end > len(repos) {
			end = len(repos)
		}
		batch := repos[i:end]

		query := c.buildBatchQuery(batch)

		var response map[string]interface{}
		err := c.api.Do(query, map[string]interface{}{
			"owner":    org,
			"authorID": userID,
			"since":    startDate,
			"until":    endDate,
		}, &response)
		if err != nil {
			continue
		}

		c.extractDatesFromBatchResponse(response, batch, dailyCounts, reposNeedingPagination)
	}

	for repoName := range reposNeedingPagination {
		c.fetchRepoCommitDates(org, repoName, userID, startDate, endDate, dailyCounts)
	}

	return nil
}

func (c *Client) buildBatchQuery(repos []string) string {
	var sb strings.Builder
	sb.WriteString("query BatchRepoCommits($owner: String!, $authorID: ID!, $since: GitTimestamp!, $until: GitTimestamp!) {\n")

	for i, repo := range repos {
		sb.WriteString(fmt.Sprintf(`  repo%d: repository(owner: $owner, name: "%s") {
    defaultBranchRef {
      target {
        ... on Commit {
          history(first: 100, author: {id: $authorID}, since: $since, until: $until) {
            totalCount
            nodes { committedDate }
          }
        }
      }
    }
  }
`, i, repo))
	}

	sb.WriteString("}")
	return sb.String()
}

func (c *Client) extractDatesFromBatchResponse(response map[string]interface{}, repos []string, dailyCounts map[string]int, reposNeedingPagination map[string]bool) {
	for i, repoName := range repos {
		key := fmt.Sprintf("repo%d", i)
		repoData, ok := response[key].(map[string]interface{})
		if !ok {
			continue
		}

		branchRef, ok := repoData["defaultBranchRef"].(map[string]interface{})
		if !ok || branchRef == nil {
			continue
		}

		target, ok := branchRef["target"].(map[string]interface{})
		if !ok {
			continue
		}

		history, ok := target["history"].(map[string]interface{})
		if !ok {
			continue
		}

		if totalCount, ok := history["totalCount"].(float64); ok && totalCount > 100 {
			reposNeedingPagination[repoName] = true
			continue
		}

		nodes, ok := history["nodes"].([]interface{})
		if !ok {
			continue
		}

		for _, node := range nodes {
			nodeMap, ok := node.(map[string]interface{})
			if !ok {
				continue
			}
			dateStr, ok := nodeMap["committedDate"].(string)
			if !ok || len(dateStr) < 10 {
				continue
			}
			dailyCounts[dateStr[:10]]++
		}
	}
}

// GetUserJoinYear fetches the year a user joined GitHub using the GitHub API.
func (c *Client) GetUserJoinYear(username string) (int, error) {
	if username == "" {
		return 0, errors.New(errors.ValidationError, "username cannot be empty", nil)
	}

	// GraphQL query to fetch the user's account creation date.
	query := `
    query UserJoinDate($username: String!) {
        user(login: $username) {
            createdAt
        }
    }`

	variables := map[string]interface{}{
		"username": username,
	}

	var response struct {
		User struct {
			CreatedAt time.Time `json:"createdAt"`
		} `json:"user"`
	}

	// Execute the GraphQL query.
	err := c.api.Do(query, variables, &response)
	if err != nil {
		return 0, errors.New(errors.NetworkError, "failed to fetch user's join date", err)
	}

	// Parse the join date
	joinYear := response.User.CreatedAt.Year()
	if joinYear == 0 {
		return 0, errors.New(errors.ValidationError, "invalid join date received from GitHub API", nil)
	}

	return joinYear, nil
}
