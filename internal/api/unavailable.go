// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/genproto/googleapis/rpc/errdetails"

	"github.com/panelarr/meta/internal/host"
)

// ReasonProviderUnavailable is the ErrorInfo reason of calls that fail
// because a provider's plugin can't be reached; metadata["provider"] names
// the provider.
const ReasonProviderUnavailable = "PROVIDER_UNAVAILABLE"

// providerUnavailable turns a "plugin can't be reached" error into
// Unavailable with an ErrorInfo naming the provider, so clients can tell a
// provider that is down from meta itself being unreachable. Other errors
// come back unchanged.
func providerUnavailable(err error) error {
	var ue *host.UnavailableError
	if !errors.As(err, &ue) {
		return err
	}
	cerr := connect.NewError(connect.CodeUnavailable, err)
	info := &errdetails.ErrorInfo{Reason: ReasonProviderUnavailable, Domain: "panelarr.meta", Metadata: map[string]string{"provider": ue.Provider}}
	if d, derr := connect.NewErrorDetail(info); derr == nil {
		cerr.AddDetail(d)
	}
	return cerr
}
