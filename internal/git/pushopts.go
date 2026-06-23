package git

import (
	"bufio"
	"bytes"
	"io"
	"net/http"

	"github.com/go-git/go-git/v6/plumbing/format/pktline"
	"github.com/go-git/go-git/v6/plumbing/protocol/packp"
)

type pushOptionKey struct{}

// PushOptionsFromRequest returns the push options extracted from the request
// context, if any. Set by the push option extraction middleware.
func PushOptionsFromRequest(r *http.Request) []string {
	if opts, ok := r.Context().Value(pushOptionKey{}).([]string); ok {
		return opts
	}
	return nil
}

// ExtractPushOptions parses push options from a receive-pack request body.
// The body is buffered and a new ReadCloser is returned for the backend to consume.
// Push options appear in the pkt-line stream after the command list (flush-pkt)
// and before the packfile (PACK magic).
//
// Stream format:
//
//	[shallow] command+caps command* flush-pkt [push-options flush-pkt] [packfile]
func ExtractPushOptions(body io.ReadCloser) ([]string, io.ReadCloser, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, err
	}
	_ = body.Close()

	opts := parsePushOptions(data)
	return opts, io.NopCloser(bytes.NewReader(data)), nil
}

// parsePushOptions scans the receive-pack pkt-line stream for push options.
// It reads: commands (until flush-pkt), then push options (until next flush-pkt
// or PACK magic), then stops.
func parsePushOptions(data []byte) []string {
	rd := bufio.NewReader(bytes.NewReader(data))

	// Skip the command list: read pkt-lines until we hit a flush-pkt.
	capsLine := true
	for {
		l, _, err := pktline.ReadLine(rd)
		if err != nil {
			return nil
		}
		if l == pktline.Flush {
			break
		}
		// The first command line contains capabilities after NUL.
		// Check if "push-options" is in the capability list.
		if capsLine {
			capsLine = false
			if !bytes.Contains(data, []byte("push-options")) {
				return nil
			}
		}
	}

	// Now positioned after the command flush-pkt. If push-options capability
	// was negotiated, the next section is push options terminated by flush-pkt.
	// Otherwise it's the packfile (starts with "PACK" magic).
	var opts packp.PushOptions
	if err := opts.Decode(rd); err != nil {
		return nil
	}
	return opts.Options
}
