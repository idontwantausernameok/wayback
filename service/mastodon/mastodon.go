// Copyright 2021 Wayback Archiver. All rights reserved.
// Use of this source code is governed by the GNU GPL v3
// license that can be found in the LICENSE file.

package mastodon // import "github.com/wabarc/wayback/service/mastodon"

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"time"

	"github.com/mattn/go-mastodon"
	"github.com/wabarc/helper"
	"github.com/wabarc/logger"
	"github.com/wabarc/wayback"
	"github.com/wabarc/wayback/config"
	"github.com/wabarc/wayback/errors"
	"github.com/wabarc/wayback/metrics"
	"github.com/wabarc/wayback/pooling"
	"github.com/wabarc/wayback/publish"
	"github.com/wabarc/wayback/storage"
	"golang.org/x/net/html"
)

type Mastodon struct {
	sync.RWMutex

	ctx    context.Context
	pool   pooling.Pool
	client *mastodon.Client
	store  *storage.Storage

	archiving map[mastodon.ID]bool
}

// New mastodon struct.
func New(ctx context.Context, store *storage.Storage, pool pooling.Pool) *Mastodon {
	if !config.Opts.PublishToMastodon() {
		logger.Fatal("[mastodon] missing required environment variable")
	}
	if store == nil {
		logger.Fatal("[mastodon] must initialize storage")
	}
	if pool == nil {
		logger.Fatal("[mastodon] must initialize pooling")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	client := mastodon.NewClient(&mastodon.Config{
		Server:       config.Opts.MastodonServer(),
		ClientID:     config.Opts.MastodonClientKey(),
		ClientSecret: config.Opts.MastodonClientSecret(),
		AccessToken:  config.Opts.MastodonAccessToken(),
	})
	return &Mastodon{
		ctx:    ctx,
		pool:   pool,
		client: client,
		store:  store,
	}
}

// Serve loop request direct messages from the Mastodon instance.
// Serve always returns a nil error.
func (m *Mastodon) Serve() error {
	if m.client == nil {
		return errors.New("Must initialize Mastodon client.")
	}
	logger.Debug("[mastodon] Serving Mastodon instance: %s", config.Opts.MastodonServer())

	// rcv, err := m.client.StreamingUser(m.ctx)
	// if err != nil {
	// 	logger.Error("%v", err)
	// 	return err
	// }
	// for e := range rcv {
	// 	switch t := e.(type) {
	// 	case *mastodon.UpdateEvent:
	// 		logger.Debug("%v", t.Status)

	// 		m.status = t.Status
	// 		go m.process(m.ctx)
	// 	case *mastodon.ErrorEvent:
	// 		logger.Error("%v", e)
	// 	}
	// }

	go func() {
		clearTick := time.NewTicker(10 * time.Minute) // Clear notifications
		fetchTick := time.NewTicker(5 * time.Second)  // Fetch conversations

		m.archiving = make(map[mastodon.ID]bool)
		var mute sync.RWMutex
		var once sync.Once
		for {
			select {
			case <-clearTick.C:
				logger.Debug("[mastodon] clear notifications...")
				m.client.ClearNotifications(m.ctx)
			case <-fetchTick.C:
				convs, err := m.client.GetConversations(m.ctx, nil)
				if err != nil {
					logger.Error("[mastodon] get conversations failure, error: %v", err)
				}
				logger.Debug("[mastodon] conversations: %v", convs)

				for _, conv := range convs {
					if _, exist := m.archiving[conv.ID]; exist {
						continue
					}
					conv := conv
					go m.pool.Roll(func() {
						metrics.IncrementWayback(metrics.ServiceMastodon, metrics.StatusRequest)
						if err := m.process(conv); err != nil {
							logger.Error("[mastodon] process failure, conversation: %#v, error: %v", conv, err)
							metrics.IncrementWayback(metrics.ServiceMastodon, metrics.StatusFailure)
						} else {
							metrics.IncrementWayback(metrics.ServiceMastodon, metrics.StatusSuccess)
						}
					})

					mute.Lock()
					m.archiving[conv.ID] = true
					mute.Unlock()
				}
			case <-m.ctx.Done():
				once.Do(func() {
					logger.Debug("[mastodon] stopping ticker...")
					clearTick.Stop()
					fetchTick.Stop()
				})
			default:
			}
		}
	}()

	select {
	case <-m.ctx.Done():
		logger.Info("[mastodon] stopping service...")
	}

	return errors.New("done")
}

func (m *Mastodon) process(conv *mastodon.Conversation) error {
	if conv.LastStatus == nil || conv.ID == "" {
		logger.Debug("[mastodon] no status or conversation")
		return errors.New("Mastodon: no status or conversation")
	}

	text := textContent(conv.LastStatus.Content)
	logger.Debug("[mastodon] conversation id: %s message: %s", conv.ID, text)
	defer m.client.DeleteConversation(m.ctx, conv.ID)
	defer func() {
		time.Sleep(time.Second)
		delete(m.archiving, conv.ID)
	}()

	urls := helper.MatchURLFallback(text)
	pub := publish.NewMastodon(m.client)
	if len(urls) == 0 {
		logger.Info("[mastodon] archives failure, URL no found.")
		pub.ToMastodon(m.ctx, "URL no found", string(conv.LastStatus.ID))
		return errors.New("Mastodon: URL no found")
	}

	col, err := wayback.Wayback(urls)
	if err != nil {
		logger.Error("[mastodon] archives failure, ", err)
		return err
	}

	// Reply and publish toot as public
	ctx := context.WithValue(m.ctx, "mastodon", m.client)
	go publish.To(ctx, col, "mastodon", string(conv.LastStatus.ID))

	return nil
}

func textContent(s string) string {
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return s
	}
	var buf bytes.Buffer

	var extractText func(node *html.Node, w *bytes.Buffer)
	extractText = func(node *html.Node, w *bytes.Buffer) {
		if node.Type == html.TextNode {
			data := strings.Trim(node.Data, "\r\n")
			if data != "" {
				w.WriteString(data)
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			extractText(c, w)
		}
		if node.Type == html.ElementNode {
			name := strings.ToLower(node.Data)
			if name == "br" {
				w.WriteString("\n")
			}
		}
	}
	extractText(doc, &buf)
	return buf.String()
}
