package cache

import (
	"fmt"
	"strings"

	"github.com/luckzk/zy-drive/internal/domain"
)

// SearchLocal searches the node cache by name substring. Results are
// explicitly local-cache results: the UI labels them "本地结果" when the
// provider has no server-side search (see storage.Searcher).
func (db *DB) SearchLocal(mountID, query string, limit int) ([]domain.Node, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := db.SQL.Query(`SELECT path, ref, is_dir, size, mod_time, hash, hash_kind, mime
		FROM nodes WHERE mount_id = ? AND path LIKE ? ORDER BY path ASC LIMIT ?`,
		mountID, "%"+escapeLike(query)+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("cache: search local: %w", err)
	}
	defer rows.Close()
	var out []domain.Node
	for rows.Next() {
		var n domain.Node
		var isDir int
		var modTime string
		if err := rows.Scan(&n.Path, &n.Ref, &isDir, &n.Size, &modTime, &n.Hash, &n.HashKind, &n.Mime); err != nil {
			return nil, err
		}
		n.IsDir = isDir != 0
		n.Name = baseName(n.Path)
		out = append(out, n)
	}
	return out, rows.Err()
}

func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	s = strings.ReplaceAll(s, `_`, `\_`)
	return s
}
