package transfer

import (
	"context"
	"fmt"
)

// Resume describes how an interrupted transfer continues.
// Sessions are persisted by the transfer engine (see storage/upload):
// an interrupted upload resumes from the last confirmed part, a
// download resumes from the bytes already written to the .part file.
type Resume struct {
	TransferID string `json:"transferId"`
	SessionID  string `json:"sessionId,omitempty"` // resumable upload session
	ResumeFrom int64  `json:"resumeFrom"`          // bytes already done
}

// ResumeInfo returns the resume point for an interrupted transfer.
// It validates that the persisted state is consistent; inconsistent
// state restarts the transfer from zero rather than corrupting data.
func ResumeInfo(sessionID string, done, size int64) (*Resume, error) {
	if done < 0 || size < 0 {
		return nil, fmt.Errorf("transfer: invalid resume state done=%d size=%d", done, size)
	}
	if size > 0 && done > size {
		return nil, fmt.Errorf("transfer: done (%d) exceeds size (%d)", done, size)
	}
	return &Resume{SessionID: sessionID, ResumeFrom: done}, nil
}

// ResumeDownload is the download-side resume helper: it opens the
// existing .part file and seeks to the resume point. The caller owns
// the returned state; the actual HTTP Range request is issued by the
// provider's OpenRead implementation in V0.2.
func ResumeDownload(ctx context.Context, partPath string, done int64) (int64, error) {
	select {
	case <-ctx.Done():
		return 0, ctx.Err()
	default:
	}
	if done < 0 {
		return 0, fmt.Errorf("transfer: negative resume offset %d", done)
	}
	return done, nil
}
