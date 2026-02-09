package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/steipete/gogcli/internal/config"
	"github.com/steipete/gogcli/internal/outfmt"
	"github.com/steipete/gogcli/internal/ui"
)

type GmailBatchAttachmentsCmd struct {
	Query          []string      `arg:"" name:"query" help:"Search query"`
	Max            int64         `name:"max" aliases:"limit" help:"Max messages to scan" default:"10"`
	OutputDir      OutputDirFlag `embed:""`
	FilenameFilter string        `name:"filename-filter" help:"Only download attachments whose filename contains this substring (case-insensitive)"`
}

func (c *GmailBatchAttachmentsCmd) Run(ctx context.Context, flags *RootFlags) error {
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
			return outfmt.WriteJSON(os.Stdout, map[string]any{"attachments": []any{}, "count": 0})
		}
		u.Err().Println("No messages found")
		return nil
	}

	// Resolve output directory.
	outDir := "."
	if d := strings.TrimSpace(c.OutputDir.Dir); d != "" {
		expanded, err := config.ExpandPath(d)
		if err != nil {
			return err
		}
		outDir = filepath.Clean(expanded)
	}

	// Fetch messages concurrently to collect attachments.
	const maxConcurrency = 10
	sem := make(chan struct{}, maxConcurrency)

	type msgAttachments struct {
		messageID   string
		attachments []attachmentInfo
		fetchErr    string
	}

	type result struct {
		index int
		data  msgAttachments
	}

	results := make(chan result, len(msgIDs))
	var wg sync.WaitGroup

	cancelled := false
	for i, id := range msgIDs {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			cancelled = true
		}
		if cancelled {
			break
		}

		wg.Add(1)
		go func(idx int, messageID string) {
			defer wg.Done()
			defer func() { <-sem }()

			msg, err := svc.Users.Messages.Get("me", messageID).Format("full").Context(ctx).Do()
			if err != nil {
				results <- result{index: idx, data: msgAttachments{messageID: messageID, fetchErr: err.Error()}}
				return
			}

			atts := collectAttachments(msg.Payload)
			results <- result{index: idx, data: msgAttachments{messageID: messageID, attachments: atts}}
		}(i, id)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	ordered := make([]msgAttachments, len(msgIDs))
	for r := range results {
		ordered[r.index] = r.data
	}

	filter := strings.ToLower(strings.TrimSpace(c.FilenameFilter))

	var allSummaries []attachmentDownloadSummary
	for _, ma := range ordered {
		if ma.messageID == "" {
			continue
		}
		if ma.fetchErr != "" {
			allSummaries = append(allSummaries, attachmentDownloadSummary{
				MessageID:     ma.messageID,
				DownloadError: fmt.Sprintf("fetch: %s", ma.fetchErr),
			})
			continue
		}
		for _, att := range ma.attachments {
			if filter != "" && !strings.Contains(strings.ToLower(att.Filename), filter) {
				continue
			}
			outPath, cached, err := downloadAttachment(ctx, svc, ma.messageID, att, outDir)
			if err != nil {
				allSummaries = append(allSummaries, attachmentDownloadSummary{
					MessageID:     ma.messageID,
					AttachmentID:  att.AttachmentID,
					Filename:      att.Filename,
					MimeType:      att.MimeType,
					Size:          att.Size,
					DownloadError: err.Error(),
				})
				continue
			}
			allSummaries = append(allSummaries, attachmentDownloadSummary{
				MessageID:    ma.messageID,
				AttachmentID: att.AttachmentID,
				Filename:     att.Filename,
				MimeType:     att.MimeType,
				Size:         att.Size,
				Path:         outPath,
				Cached:       cached,
			})
		}
	}

	if outfmt.IsJSON(ctx) {
		return outfmt.WriteJSON(os.Stdout, map[string]any{
			"attachments": allSummaries,
			"count":       len(allSummaries),
		})
	}

	if len(allSummaries) == 0 {
		u.Out().Println("No attachments found")
		return nil
	}

	u.Out().Printf("Downloaded %d attachment(s):", len(allSummaries))
	for _, s := range allSummaries {
		if s.DownloadError != "" {
			u.Out().Printf("  Error: %s (%s) - %s", s.Filename, formatBytes(s.Size), s.DownloadError)
			continue
		}
		status := "Saved"
		if s.Cached {
			status = "Cached"
		}
		u.Out().Printf("  %s: %s (%s) - %s", status, s.Filename, formatBytes(s.Size), s.Path)
	}
	return nil
}
