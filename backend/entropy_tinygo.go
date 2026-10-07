//go:build tinygo

package main

import "crypto/rand"

// Only the WASM backend installs this reader; native workers retain OS entropy.
func configureEntropy(db *scopedDB) { rand.Reader = &databaseEntropyReader{db: db} }
