package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"google.golang.org/api/gmail/v1"

	"github.com/steipete/gogcli/internal/outfmt"
	"github.com/steipete/gogcli/internal/ui"
)

type GmailBatchCleanupCmd struct {
	Query  []string `arg:"" optional:"" name:"query" help:"Additional search query"`
	Label  string   `name:"label" required:"" help:"Label to filter by"`
	Days   int      `name:"days" required:"" help:"Messages older than N days"`
	Delete bool     `name:"delete" help:"Permanently delete instead of trash"`
	Max    int64    `name:"max" aliases:"limit" help:"Max messages to process (0 = all)" default:"0"`
	DryRun bool     `name:"dry-run" help:"Only show what would be done"`
}

const batchLimit = 1000

func (c *GmailBatchCleanupCmd) Run(ctx context.Context, flags *RootFlags) error {
	u := ui.FromContext(ctx)
	account, err := requireAccount(flags)
	if err != nil {
		return err
	}

	label := strings.TrimSpace(c.Label)
	if label == "" {
		return usage("missing --label")
	}
	if c.Days <= 0 {
		return usage("--days must be positive")
	}

	cutoff := time.Now().AddDate(0, 0, -c.Days)
	before := cutoff.Format("2006/01/02")

	extra := strings.TrimSpace(strings.Join(c.Query, " "))
	quotedLabel := label
	if strings.ContainsAny(label, " \t") {
		quotedLabel = `"` + label + `"`
	}
	q := fmt.Sprintf("label:%s before:%s", quotedLabel, before)
	if extra != "" {
		q = fmt.Sprintf("label:%s %s before:%s", quotedLabel, extra, before)
	}

	svc, err := newGmailService(ctx, account)
	if err != nil {
		return err
	}

	var ids []string
	pageToken := ""
	for {
		call := svc.Users.Messages.List("me").Q(q).Fields("messages(id),nextPageToken").Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		if err != nil {
			return err
		}
		for _, m := range resp.Messages {
			if m != nil && m.Id != "" {
				ids = append(ids, m.Id)
			}
		}
		if c.Max > 0 && int64(len(ids)) >= c.Max {
			ids = ids[:c.Max]
			break
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	if c.DryRun {
		if outfmt.IsJSON(ctx) {
			return outfmt.WriteJSON(os.Stdout, map[string]any{
				"dryRun": true,
				"query":  q,
				"count":  len(ids),
				"ids":    ids,
			})
		}
		u.Out().Printf("Dry run: %d messages match query: %s", len(ids), q)
		for _, id := range ids {
			u.Out().Println(id)
		}
		return nil
	}

	if len(ids) == 0 {
		if outfmt.IsJSON(ctx) {
			return outfmt.WriteJSON(os.Stdout, map[string]any{
				"query": q,
				"count": 0,
			})
		}
		u.Out().Printf("No messages match query: %s", q)
		return nil
	}

	action := "trashed"
	for i := 0; i < len(ids); i += batchLimit {
		end := i + batchLimit
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[i:end]

		if c.Delete {
			action = "deleted"
			err = svc.Users.Messages.BatchDelete("me", &gmail.BatchDeleteMessagesRequest{
				Ids: batch,
			}).Context(ctx).Do()
		} else {
			err = svc.Users.Messages.BatchModify("me", &gmail.BatchModifyMessagesRequest{
				Ids:         batch,
				AddLabelIds: []string{"TRASH"},
			}).Context(ctx).Do()
		}
		if err != nil {
			return fmt.Errorf("batch %d–%d: %w", i, end-1, err)
		}
	}

	if outfmt.IsJSON(ctx) {
		return outfmt.WriteJSON(os.Stdout, map[string]any{
			"action": action,
			"query":  q,
			"count":  len(ids),
			"ids":    ids,
		})
	}

	actionLabel := strings.ToUpper(action[:1]) + action[1:]
	u.Out().Printf("%s %d messages (query: %s)", actionLabel, len(ids), q)
	return nil
}
