package slack

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/rauriemo/anthem/internal/channel"
	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

const maxDownloadBytes = 10 * 1024 * 1024 // 10MB

type Adapter struct {
	api       *slackapi.Client
	socket    *socketmode.Client
	botToken  string
	channelID string
	incoming  chan channel.IncomingMessage
	logger    *slog.Logger
	cancel    context.CancelFunc
}

func NewAdapter(botToken, appToken, channelID string, logger *slog.Logger) *Adapter {
	if logger == nil {
		logger = slog.Default()
	}
	api := slackapi.New(botToken, slackapi.OptionAppLevelToken(appToken))
	socket := socketmode.New(api)
	return &Adapter{
		api:       api,
		socket:    socket,
		botToken:  botToken,
		channelID: channelID,
		incoming:  make(chan channel.IncomingMessage, 64),
		logger:    logger,
	}
}

func (a *Adapter) Kind() string { return "slack" }

func (a *Adapter) Start(ctx context.Context) error {
	ctx, a.cancel = context.WithCancel(ctx)
	go func() {
		if err := a.socket.RunContext(ctx); err != nil {
			a.logger.Warn("slack socket mode exited", "error", err)
		}
	}()
	go a.handleEvents(ctx)
	return nil
}

func (a *Adapter) handleEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-a.socket.Events:
			if !ok {
				return
			}
			a.processEvent(ctx, evt)
		}
	}
}

func (a *Adapter) processEvent(ctx context.Context, evt socketmode.Event) {
	switch evt.Type {
	case socketmode.EventTypeEventsAPI:
		eventsAPI, ok := evt.Data.(slackevents.EventsAPIEvent)
		if !ok {
			return
		}
		a.socket.Ack(*evt.Request)
		a.handleEventsAPI(ctx, eventsAPI)

	case socketmode.EventTypeConnecting:
		a.logger.Info("slack connecting")
	case socketmode.EventTypeConnected:
		a.logger.Info("slack connected")
	case socketmode.EventTypeConnectionError:
		a.logger.Warn("slack connection error")
	}
}

func (a *Adapter) handleEventsAPI(ctx context.Context, event slackevents.EventsAPIEvent) {
	if event.InnerEvent.Type != "message" {
		return
	}
	msgEvent, ok := event.InnerEvent.Data.(*slackevents.MessageEvent)
	if !ok || msgEvent == nil {
		return
	}
	if msgEvent.Channel != a.channelID {
		return
	}
	// Ignore bot messages and subtypes (edits, deletes, etc.)
	if msgEvent.BotID != "" || msgEvent.SubType != "" {
		return
	}

	var files []channel.File
	if msgEvent.Message != nil && len(msgEvent.Message.Files) > 0 {
		files = a.downloadFiles(ctx, msgEvent.Message.Files)
	}

	msg := channel.IncomingMessage{
		ChannelKind: "slack",
		SenderID:    msgEvent.User,
		ThreadID:    msgEvent.ThreadTimeStamp,
		Text:        msgEvent.Text,
		Files:       files,
		Timestamp:   time.Now(),
		Raw:         msgEvent,
	}

	select {
	case a.incoming <- msg:
	default:
		a.logger.Warn("dropping slack incoming message, buffer full")
	}
}

func (a *Adapter) Send(ctx context.Context, msg channel.OutgoingMessage) error {
	opts := []slackapi.MsgOption{
		slackapi.MsgOptionText(msg.Text, false),
	}
	if msg.ThreadID != "" {
		opts = append(opts, slackapi.MsgOptionTS(msg.ThreadID))
	}
	_, _, err := a.api.PostMessageContext(ctx, a.channelID, opts...)
	if err != nil {
		return fmt.Errorf("posting slack message: %w", err)
	}
	return nil
}

func (a *Adapter) Incoming() <-chan channel.IncomingMessage {
	return a.incoming
}

func (a *Adapter) Close() error {
	if a.cancel != nil {
		a.cancel()
	}
	return nil
}

func (a *Adapter) downloadFiles(ctx context.Context, files []slackapi.File) []channel.File {
	var result []channel.File
	var totalBytes int64

	for _, f := range files {
		if totalBytes >= maxDownloadBytes {
			a.logger.Warn("file download limit reached, skipping remaining files")
			break
		}

		url := f.URLPrivateDownload
		if url == "" {
			url = f.URLPrivate
		}
		if url == "" {
			continue
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			a.logger.Warn("creating file download request failed", "file", f.Name, "error", err)
			continue
		}
		req.Header.Set("Authorization", "Bearer "+a.botToken)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			a.logger.Warn("downloading slack file failed", "file", f.Name, "error", err)
			continue
		}

		remaining := maxDownloadBytes - totalBytes
		body, err := io.ReadAll(io.LimitReader(resp.Body, remaining))
		resp.Body.Close()
		if err != nil {
			a.logger.Warn("reading slack file failed", "file", f.Name, "error", err)
			continue
		}

		totalBytes += int64(len(body))
		result = append(result, channel.File{
			Name:     f.Name,
			Content:  body,
			MimeType: f.Mimetype,
		})
	}

	return result
}
