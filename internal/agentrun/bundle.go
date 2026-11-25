// SPDX-License-Identifier: MPL-2.0
package agentrun

import (
	"github.com/luckzk/zy-drive/internal/agentapi"
)

// ImportBundle decrypts a bundle file and installs its mounts and
// jobs (see app.ImportBundle), then re-registers every job with the
// scheduler so a newly imported daily job starts firing on its own
// clock without an Agent restart.
func (r *Runner) ImportBundle(data []byte, passphrase string) (*agentapi.Bundle, error) {
	bundle, err := r.app.ImportBundle(data, passphrase)
	if err != nil {
		return nil, err
	}
	if err := r.RegisterJobs(); err != nil {
		return bundle, err
	}
	return bundle, nil
}
