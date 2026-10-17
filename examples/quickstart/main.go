// Command quickstart is README.md's quick start as a whole program: it
// starts a task in your first workspace and prints the conversation until
// the turn ends. scripts/check-readme.sh checks that the README shows the
// body of main exactly.
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/sinatra-dev/sinatra-go"
)

func main() {
	ctx := context.Background()
	client, err := sinatra.NewClient() // reads SINATRA_TOKEN
	check(err)
	me, err := client.Me.Get(ctx)
	check(err)
	if len(me.Workspaces) == 0 {
		log.Fatal("the token's user isn't in any workspace yet")
	}
	ws := me.Workspaces[0].Id
	created, err := client.Tasks.Create(ctx, ws, sinatra.CreateTaskRequest{Prompt: "Fix the flaky login test"},
		sinatra.WithIdempotencyKey("fix-flaky-login-1"))
	check(err)
	working := map[string]bool{"pending": true, "investigating": true, "implementing": true, "rework": true}
	var after *string
	for ; ; time.Sleep(3 * time.Second) {
		task, err := client.Tasks.Get(ctx, ws, created.Task.Id)
		check(err)
		for m, err := range client.Messages.ListAll(ctx, ws, task.Id, &sinatra.MessagesListParams{After: after}) {
			check(err)
			fmt.Printf("%s: %s\n", m.Role, m.Body)
			after = &m.Id
		}
		if !working[task.Status] {
			fmt.Println("status:", task.Status)
			return
		}
	}
}

func check(err error) {
	if err != nil {
		log.Fatal(err)
	}
}
