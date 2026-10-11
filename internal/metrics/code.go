// SPDX-License-Identifier: GPL-3.0-or-later

package metrics

import (
	"context"
	"errors"

	"connectrpc.com/connect"

	"github.com/panelarr/meta/sdk/plugin"
)

func codeOf(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code().String()
	}
	switch {
	case errors.Is(err, context.Canceled):
		return connect.CodeCanceled.String()
	case errors.Is(err, context.DeadlineExceeded):
		return connect.CodeDeadlineExceeded.String()
	}
	return plugin.ErrorCode(err).String()
}
