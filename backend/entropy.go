package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	plugin "github.com/Paca-AI/plugin-sdk-go"
)

type entropyQuery interface {
	Query(string, ...any) (*plugin.DBQueryResult, error)
}
type databaseEntropyReader struct{ db entropyQuery }

// WASM instances may restore a prior in-memory RNG state. Each block obtains
// fresh PostgreSQL UUIDs instead, with no entropy state in the plugin snapshot.
// Three independent UUIDv4 values provide 366 random bits before SHA-256.
// PostgreSQL's built-in gen_random_uuid requires no extra extension or schema.
func (r *databaseEntropyReader) Read(dst []byte) (int, error) {
	offset := 0
	for offset < len(dst) {
		rows, err := r.db.Query("SELECT gen_random_uuid()::text,gen_random_uuid()::text,gen_random_uuid()::text")
		if err != nil {
			return offset, errors.New("secure randomness unavailable")
		}
		if rows == nil || len(rows.Rows) != 1 || len(rows.Rows[0]) != 3 {
			return offset, errors.New("invalid secure randomness response")
		}
		seed := "paca/" + pluginID + "/entropy/v1\n"
		for _, value := range rows.Rows[0] {
			id, ok := value.(string)
			if !ok || len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
				return offset, errors.New("invalid secure randomness value")
			}
			if raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", "")); err != nil || len(raw) != 16 {
				return offset, errors.New("invalid secure randomness value")
			}
			seed += id + "\n"
		}
		block := sha256.Sum256([]byte(seed))
		offset += copy(dst[offset:], block[:])
	}
	return offset, nil
}
