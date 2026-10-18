// Command open-prs prints every pull request Sinatra has open and waiting for
// review, in each of your workspaces, as a Markdown list you can paste into a
// standup or a Slack channel.
//
//	go run ./examples/open-prs
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/sinatra-dev/sinatra-go"
)

func main() {
	ctx := context.Background()
	client, err := sinatra.NewClient() // reads SINATRA_TOKEN
	check(err)
	me, err := client.Me.Get(ctx)
	check(err)

	for _, workspace := range me.Workspaces {
		var lines []string
		for task, err := range client.Tasks.ListAll(ctx, workspace.Id, &sinatra.TasksListParams{Status: sinatra.Ptr("pr_open")}) {
			check(err)
			if task.PullRequest == nil {
				continue
			}
			title, repo := task.PullRequest.Url, "?"
			if task.Title != nil {
				title = *task.Title
			}
			if task.Repo != nil {
				repo = *task.Repo
			}
			lines = append(lines, fmt.Sprintf("- [%s](%s) in %s", title, task.PullRequest.Url, repo))
		}
		plural := "s"
		if len(lines) == 1 {
			plural = ""
		}
		fmt.Printf("## %s: %d open pull request%s\n", workspace.Name, len(lines), plural)
		for _, line := range lines {
			fmt.Println(line)
		}
		fmt.Println()
	}
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
