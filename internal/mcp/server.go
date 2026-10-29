// Package mcp serves read-only bill tools over stdio and Streamable HTTP.
// Both transports read the same SQLite database as the HTTP API. The stdio
// process does not check a token: anyone who can spawn it can already read
// the file. POST /mcp is a separate bearer (MCP_TOKEN) and stays closed
// when that variable is unset.
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
func Serve(r io.Reader, w io.Writer, st *store.Store, loc *time.Location) error {
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		body, _, notification := handleMessage(context.Background(), st, loc, line)
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
