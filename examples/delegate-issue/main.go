// Command delegate-issue hands Sinatra an issue you already have, the way
// assigning it in Linear or labelling it on GitHub would. Sinatra works on it
// in its own sandbox and opens a draft pull request.
//
//	go run ./examples/delegate-issue ENG-123       # a Linear issue
//	go run ./examples/delegate-issue acme/web#42   # a GitHub issue
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"regexp"
	"strconv"

	"github.com/sinatra-dev/sinatra-go"
)

var githubIssue = regexp.MustCompile(`^([^/\s]+/[^#\s]+)#(\d+)$`)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./examples/delegate-issue <ENG-123 | owner/name#42>")
		os.Exit(2)
	}
	ref := os.Args[1]

	var issue sinatra.DelegateTarget = sinatra.DelegateLinearTicket{Id: ref}
	if m := githubIssue.FindStringSubmatch(ref); m != nil {
		number, err := strconv.Atoi(m[2])
		check(err)
		issue = sinatra.DelegateGithubTicket{Repo: m[1], Number: number}
	}

	ctx := context.Background()
	client, err := sinatra.NewClient() // reads SINATRA_TOKEN
	check(err)
	me, err := client.Me.Get(ctx)
	check(err)
	if len(me.Workspaces) == 0 {
		log.Fatal("the token's user isn't in any workspace yet")
	}
	workspace := me.Workspaces[0]

	_, err = client.Tickets.Delegate(ctx, workspace.Id, issue)
	check(err)
	fmt.Printf("Delegated %s in %s. Sinatra will open a draft pull request.\n", ref, workspace.Name)
	fmt.Println("Follow it in the dashboard, or list open PRs with ./examples/open-prs.")
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
