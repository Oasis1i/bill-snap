// Package mcp serves bill tools over stdio and Streamable HTTP.
// Both transports read the same SQLite database as the HTTP API. The stdio
// process does not check a token and stays read-only: anyone who can spawn
// it can already read the file. add_bill is only offered on POST /mcp to the
// MCP_WRITE_TOKEN bearer. The route stays closed when both MCP_TOKEN and
// MCP_WRITE_TOKEN are unset.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"time"

	"github.com/Oasis1i/bill-snap/internal/store"
)

// Serve reads MCP messages from r and writes responses to w until r hits EOF.
// uploadDir is the only directory get_bill_image will read. An empty
// directory disables image reads, and list_bills then reports has_image false.
func Serve(r io.Reader, w io.Writer, st *store.Store, loc *time.Location, uploadDir string) error {
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		body, _, notification := handleMessage(context.Background(), st, loc, uploadDir, Access{Kind: tokenKindStdio}, line)
		if notification {
			continue
		}
		if _, err := bw.Write(append(body, '\n')); err != nil {
			return err
		}
		if err := bw.Flush(); err != nil {
			return err
		}
	}
	return sc.Err()
}
