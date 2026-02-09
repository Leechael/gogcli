package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/steipete/gogcli/internal/outfmt"
	"github.com/steipete/gogcli/internal/ui"
)

type GmailMessagesBatchGetCmd struct {
	Query        []string `arg:"" name:"query" help:"Search query"`
	Max          int64    `name:"max" aliases:"limit" help:"Max messages" default:"10"`
	Format       string   `name:"format" enum:"full,metadata" help:"Message format" default:"full"`
	BodyMaxChars int      `name:"body-max-chars" help:"Truncate body to N characters (0 = no truncation)" default:"2000"`
	Timezone     string   `name:"timezone" short:"z" help:"Output timezone (IANA name, e.g. America/New_York, UTC). Default: local"`
	Local        bool     `name:"local" help:"Use local timezone (default behavior, useful to override --timezone)"`
}

func (c *GmailMessagesBatchGetCmd) Run(ctx context.Context, flags *RootFlags) error {
	u := ui.FromContext(ctx)
	account, err := requireAccount(flags)
	if err != nil {
		return err
	}
	query := strings.TrimSpace(strings.Join(c.Query, " "))
	if query == "" {
		return usage("missing query")
	}

	svc, err := newGmailService(ctx, account)
	if err != nil {
		return err
	}

	// Collect message IDs via paginated search.
	var msgIDs []string
	pageToken := ""
	for {
		call := svc.Users.Messages.List("me").Q(query).Fields("messages(id),nextPageToken").Context(ctx)
		if pageToken != "" {
			call = call.PageToken(pageToken)
		}
		resp, err := call.Do()
		if err != nil {
			return err
		}
		for _, m := range resp.Messages {
			if m != nil && m.Id != "" {
				msgIDs = append(msgIDs, m.Id)
			}
		}
		if c.Max > 0 && int64(len(msgIDs)) >= c.Max {
			msgIDs = msgIDs[:c.Max]
			break
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	if len(msgIDs) == 0 {
		if outfmt.IsJSON(ctx) {
			return outfmt.WriteJSON(os.Stdout, map[string]any{"messages": []any{}, "count": 0})
		}
		u.Err().Println("No results")
		return nil
	}

	idToName, err := fetchLabelIDToName(svc)
	if err != nil {
		return err
	}

	loc, err := resolveOutputLocation(c.Timezone, c.Local)
	if err != nil {
		return err
	}

	includeBody := c.Format == "full"

	// Fetch message details concurrently.
	const maxConcurrency = 10
	sem := make(chan struct{}, maxConcurrency)

	type result struct {
		index int
		item  batchGetItem
		err   error
	}

	results := make(chan result, len(msgIDs))
	var wg sync.WaitGroup

	for i, id := range msgIDs {
		wg.Add(1)
		go func(idx int, messageID string) {
			defer wg.Done()

			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results <- result{index: idx, err: ctx.Err()}
				return
			}

			call := svc.Users.Messages.Get("me", messageID)
			if includeBody {
				call = call.Format("full")
			} else {
				call = call.Format("metadata").
					MetadataHeaders("From", "To", "Subject", "Date").
					Fields("id,threadId,labelIds,payload(headers)")
			}
			msg, err := call.Context(ctx).Do()
			if err != nil {
				results <- result{index: idx, err: fmt.Errorf("message %s: %w", messageID, err)}
				return
			}

			item := batchGetItem{
				ID:       messageID,
				ThreadID: msg.ThreadId,
				From:     sanitizeTab(headerValue(msg.Payload, "From")),
				To:       sanitizeTab(headerValue(msg.Payload, "To")),
				Subject:  sanitizeTab(headerValue(msg.Payload, "Subject")),
				Date:     formatGmailDateInLocation(headerValue(msg.Payload, "Date"), loc),
			}

			if includeBody {
				body := bestBodyText(msg.Payload)
				if c.BodyMaxChars > 0 {
					body = truncateRunes(body, c.BodyMaxChars)
				}
				item.Body = body
			}

			if len(msg.LabelIds) > 0 {
				names := make([]string, 0, len(msg.LabelIds))
				for _, lid := range msg.LabelIds {
					if n, ok := idToName[lid]; ok {
						names = append(names, n)
					} else {
						names = append(names, lid)
					}
				}
				item.Labels = names
			}

			results <- result{index: idx, item: item}
		}(i, id)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	ordered := make([]batchGetItem, len(msgIDs))
	var firstErr error
	for r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		ordered[r.index] = r.item
	}
	if firstErr != nil {
		return firstErr
	}

	items := make([]batchGetItem, 0, len(ordered))
	for _, item := range ordered {
		if item.ID != "" {
			items = append(items, item)
		}
	}

	if outfmt.IsJSON(ctx) {
		return outfmt.WriteJSON(os.Stdout, map[string]any{
			"messages": items,
			"count":    len(items),
		})
	}

	for _, item := range items {
		u.Out().Printf("=== %s ===", item.ID)
		u.Out().Printf("From: %s", item.From)
		u.Out().Printf("To: %s", item.To)
		u.Out().Printf("Subject: %s", item.Subject)
		u.Out().Printf("Date: %s", item.Date)
		if len(item.Labels) > 0 {
			u.Out().Printf("Labels: %s", strings.Join(item.Labels, ", "))
		}
		if item.Body != "" {
			u.Out().Println("")
			u.Out().Println(item.Body)
		}
		u.Out().Println("")
	}
	return nil
}

type batchGetItem struct {
	ID       string   `json:"id"`
	ThreadID string   `json:"threadId,omitempty"`
	Date     string   `json:"date,omitempty"`
	From     string   `json:"from,omitempty"`
	To       string   `json:"to,omitempty"`
	Subject  string   `json:"subject,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	Body     string   `json:"body,omitempty"`
}
