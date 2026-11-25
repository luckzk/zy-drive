// SPDX-License-Identifier: MPL-2.0
package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/luckzk/zy-drive/internal/storage/providers/drime"
)

// ListDrimeWorkspaces returns the workspaces a Drime personal access
// token can see, personal workspace (id 0) first — the add-mount
// wizard's picker (V0.9.0, item 5). The user picks from the list
// instead of typing a workspace number; a single workspace (only
// the personal one) means the wizard auto-selects it. An empty or
// wrong token fails here with a plain-language error before any
// mount is created.
func (a *App) ListDrimeWorkspaces(ctx context.Context, accessToken string) ([]drime.Workspace, error) {
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("app: 请先粘贴个人访问令牌，才能拉工作区列表")
	}
	return drime.ListWorkspaces(ctx, "", accessToken)
}
