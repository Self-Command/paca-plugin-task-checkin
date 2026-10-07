//go:build !tinygo

package main

// Native Go uses operating-system randomness, including native Action tests.
func configureEntropy(db *scopedDB) {}
