package store

import (
	"context"
	"errors"

	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
)

// RotateSecrets re-encrypts every credential and optional content body in a
// single transaction before serving traffic. A bad old key leaves all rows
// intact. Batches bound memory use for large content-log tables.
func (s *Store) RotateSecrets(ctx context.Context, cipher *gatewaycrypto.Cipher) error {
	if cipher == nil {
		return errors.New("rotation cipher required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `LOCK TABLE providers, content_log IN EXCLUSIVE MODE`); err != nil {
		return err
	}
	var after int64
	for {
		rows, err := tx.Query(ctx, `SELECT id,api_key_enc FROM providers WHERE id>$1 ORDER BY id LIMIT 100`, after)
		if err != nil {
			return err
		}
		type item struct {
			id    int64
			value string
		}
		items := []item{}
		for rows.Next() {
			var i item
			if err := rows.Scan(&i.id, &i.value); err != nil {
				rows.Close()
				return err
			}
			items = append(items, i)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(items) == 0 {
			break
		}
		for _, i := range items {
			after = i.id
			if i.value == "" {
				continue
			}
			plain, e := cipher.Decrypt(i.value)
			if e != nil {
				return errors.New("provider credential rotation failed")
			}
			encoded, e := cipher.Encrypt(plain)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE providers SET api_key_enc=$1 WHERE id=$2`, encoded, i.id); e != nil {
				return e
			}
		}
	}
	afterID := ""
	for {
		rows, err := tx.Query(ctx, `SELECT request_id,prompt,response FROM content_log WHERE request_id>$1 ORDER BY request_id LIMIT 100`, afterID)
		if err != nil {
			return err
		}
		type item struct {
			id               string
			prompt, response []byte
		}
		items := []item{}
		for rows.Next() {
			var i item
			if err := rows.Scan(&i.id, &i.prompt, &i.response); err != nil {
				rows.Close()
				return err
			}
			items = append(items, i)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(items) == 0 {
			break
		}
		for _, i := range items {
			afterID = i.id
			values := [][]byte{i.prompt, i.response}
			for n, value := range values {
				if len(value) == 0 {
					continue
				}
				plain, e := cipher.Decrypt(string(value))
				if e != nil {
					return errors.New("content log rotation failed")
				}
				encoded, e := cipher.Encrypt(plain)
				if e != nil {
					return e
				}
				values[n] = []byte(encoded)
			}
			if _, e := tx.Exec(ctx, `UPDATE content_log SET prompt=$1,response=$2 WHERE request_id=$3`, values[0], values[1], i.id); e != nil {
				return e
			}
		}
	}
	return tx.Commit(ctx)
}
