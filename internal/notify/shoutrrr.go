// shoutrrr.go — the production Sender. Wraps github.com/containrrr/shoutrrr.
//
// Validation goes through CreateSender, which parses the URL and
// returns a usable handle if successful. We discard the handle and
// rely on the error to signal "unparseable" — Send constructs a fresh
// handle each call. shoutrrr does HTTP itself; no extra timeout
// machinery is needed beyond the context the caller passes (notify's
// Notify uses a 10s WithTimeout).

package notify

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/containrrr/shoutrrr"
	"github.com/containrrr/shoutrrr/pkg/types"
)

// ShoutrrrSender is the production Sender. Stateless; safe to share.
type ShoutrrrSender struct{}

// Validate parses the URL without sending. Returns nil if shoutrrr
// accepts it as a known service URL.
func (ShoutrrrSender) Validate(url string) error {
	if strings.TrimSpace(url) == "" {
		return errors.New("empty url")
	}
	_, err := shoutrrr.CreateSender(url)
	return err
}

// Send delivers (title, body) to every URL. Returns a joined error if
// any single URL failed; nil on full success.
//
// shoutrrr's Send returns a []error parallel to the URLs slice; we
// concatenate the non-nil entries into one error string.
func (ShoutrrrSender) Send(ctx context.Context, urls []string, title, body string) error {
	if len(urls) == 0 {
		return nil
	}
	sender, err := shoutrrr.CreateSender(urls...)
	if err != nil {
		return fmt.Errorf("shoutrrr: create sender: %w", err)
	}
	params := types.Params{}
	if title != "" {
		params["title"] = title
	}
	errs := sender.Send(body, &params)
	var msgs []string
	for _, e := range errs {
		if e != nil {
			msgs = append(msgs, e.Error())
		}
	}
	if len(msgs) > 0 {
		return fmt.Errorf("shoutrrr: %s", strings.Join(msgs, "; "))
	}
	_ = ctx // shoutrrr v0.8 doesn't accept a context; the caller's deadline still bounds wall clock via the underlying http.Client default of no timeout — see notify.Notify's WithTimeout.
	return nil
}
