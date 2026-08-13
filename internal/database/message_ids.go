package database

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
)

var packedMessageIDsMagic = []byte{'D', 'M', 'S', 'G', 'Z', '1', 0}

// packMessageIDs stores message IDs as a compact length-prefixed blob wrapped
// in zstd. The legacy text[] column remains a read fallback during migration.
func packMessageIDs(ids []string) []byte {
	if len(ids) == 0 {
		return nil
	}
	raw := make([]byte, 0, len(ids)*48)
	raw = binary.AppendUvarint(raw, uint64(len(ids)))
	for _, id := range ids {
		raw = binary.AppendUvarint(raw, uint64(len(id)))
		raw = append(raw, id...)
	}
	compressed := nzbZstdEncoder.EncodeAll(raw, make([]byte, 0, len(raw)/3))
	out := make([]byte, 0, len(packedMessageIDsMagic)+len(compressed))
	out = append(out, packedMessageIDsMagic...)
	out = append(out, compressed...)
	return out
}

func unpackMessageIDs(packed []byte, legacyRaw string) ([]string, error) {
	if len(packed) == 0 {
		return parsePostgresArray(legacyRaw), nil
	}
	if !bytes.HasPrefix(packed, packedMessageIDsMagic) {
		return nil, errors.New("message_ids_packed has invalid format")
	}
	body, err := nzbZstdDecoder.DecodeAll(packed[len(packedMessageIDsMagic):], nil)
	if err != nil {
		return nil, fmt.Errorf("decompress message ids: %w", err)
	}
	count, n := binary.Uvarint(body)
	if n <= 0 {
		return nil, errors.New("message_ids_packed missing count")
	}
	body = body[n:]
	out := make([]string, 0, int(count))
	for i := uint64(0); i < count; i++ {
		size, n := binary.Uvarint(body)
		if n <= 0 {
			return nil, errors.New("message_ids_packed truncated length")
		}
		body = body[n:]
		if size > uint64(len(body)) {
			return nil, errors.New("message_ids_packed truncated value")
		}
		out = append(out, string(body[:size]))
		body = body[size:]
	}
	if len(body) != 0 {
		return nil, errors.New("message_ids_packed has trailing data")
	}
	return out, nil
}

// CompactNZBFileMessageIDs converts legacy text[] segment identifiers to the
// packed representation in batches, then clears the legacy array so pg_dump no
// longer carries both copies. Rows already packed are skipped.
func (db *DB) CompactNZBFileMessageIDs(ctx context.Context) (int64, error) {
	const batchSize = 1000
	var total int64
	for {
		rows, err := db.SQL.QueryContext(ctx, `
			select id, coalesce(message_ids::text, '{}')
			from nzb_files
			where message_ids_packed is null
			  and coalesce(array_length(message_ids, 1), 0) > 0
			order by id
			limit $1`, batchSize)
		if err != nil {
			return total, err
		}
		type item struct {
			id  int64
			ids []string
		}
		batch := make([]item, 0, batchSize)
		for rows.Next() {
			var id int64
			var raw string
			if err := rows.Scan(&id, &raw); err != nil {
				_ = rows.Close()
				return total, err
			}
			batch = append(batch, item{id: id, ids: parsePostgresArray(raw)})
		}
		if err := rows.Close(); err != nil {
			return total, err
		}
		if len(batch) == 0 {
			return total, nil
		}
		for _, row := range batch {
			if err := ctx.Err(); err != nil {
				return total, err
			}
			if len(row.ids) == 0 {
				continue
			}
			res, err := db.SQL.ExecContext(ctx, `
				update nzb_files
				set message_ids_packed = $2,
				    message_id_count = $3,
				    message_ids = '{}'
				where id = $1
				  and message_ids_packed is null`,
				row.id, packMessageIDs(row.ids), len(row.ids))
			if err != nil {
				return total, err
			}
			affected, err := res.RowsAffected()
			if err != nil {
				return total, err
			}
			total += affected
		}
	}
}
