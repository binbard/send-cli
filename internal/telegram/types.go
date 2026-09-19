package telegram

import "context"

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
