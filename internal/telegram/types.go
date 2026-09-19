package telegram

import (
	"context"
	"time"
)

type TransferDirection string

const (
	TransferUpload   TransferDirection = "upload"
	TransferDownload TransferDirection = "download"
)

type TransferProgress struct {
	Direction   TransferDirection
	Name        string
	Transferred int64
	Total       int64
	Elapsed     time.Duration
}

type progressContextKey struct{}

func WithTransferProgress(ctx context.Context, handler func(TransferProgress)) context.Context {
	return context.WithValue(ctx, progressContextKey{}, handler)
}

func reportTransferProgress(ctx context.Context, progress TransferProgress) {
	if handler, ok := ctx.Value(progressContextKey{}).(func(TransferProgress)); ok && handler != nil {
		handler(progress)
	}
}

type RemoteFile struct {
	Name     string
	Size     int64
	Download func(context.Context, string) error
}

type Event struct {
	Sender string
	Text   string
	File   *RemoteFile
	Err    error
}

type Session interface {
	Send(ctx context.Context, text string, files []string) error
	Events() <-chan Event
}

type Service interface {
	Run(ctx context.Context, interactive bool, fn func(Session) error) error
}
