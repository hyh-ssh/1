package adapter

import (
	"errors"
	"io"
)

const maxResponseBytes = 64 << 20

func readUpstreamBody(r io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxResponseBytes {
		return nil, errors.New("upstream response exceeds size limit")
	}
	return body, nil
}
