// Command search-issues lists open Linear and GitHub issues you could hand to
// Sinatra, yours first. Pass one to ./examples/delegate-issue to delegate it.
//
//	go run ./examples/search-issues                   # everything open
//	go run ./examples/search-issues "flaky test"      # matching text
//	go run ./examples/search-issues "" acme/web       # search acme/web first on GitHub
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/sinatra-dev/sinatra-go"
)

func main() {
	params := &sinatra.TicketsSearchParams{}
	if len(os.Args) > 1 && os.Args[1] != "" {
		params.Q = sinatra.Ptr(os.Args[1])
	}
	if len(os.Args) > 2 && os.Args[2] != "" {
		params.Repo = sinatra.Ptr(os.Args[2])
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

	result, err := client.Tickets.Search(ctx, workspace.Id, params)
	check(err)
	if !result.Linear.Connected && !result.Github.Connected {
		fmt.Printf("%s isn't connected to Linear or GitHub yet.\n", workspace.Name)
	}
	for _, ticket := range result.Tickets {
		mark := " "
		if ticket.Yours {
			mark = "*"
		}
		fmt.Printf("%s %-24s %s\n", mark, ticket.Identifier, ticket.Title)
	}
	plural := "s"
	if len(result.Tickets) == 1 {
		plural = ""
	}
	fmt.Printf("\n%d issue%s. * = assigned to you or written by you.\n", len(result.Tickets), plural)
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
