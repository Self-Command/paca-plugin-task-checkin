package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"

	plugin "github.com/Paca-AI/plugin-sdk-go"
)

type entropyFixture struct {
	calls     int
	fail      bool
	malformed bool
}

func (f *entropyFixture) Query(string, ...any) (*plugin.DBQueryResult, error) {
	if f.fail {
		return nil, errors.New("fixture failure")
	}
	if f.malformed {
		return &plugin.DBQueryResult{}, nil
	}
	f.calls++
	values := []any{}
	for i := 0; i < 3; i++ {
		values = append(values, fmt.Sprintf("%08x-1111-4111-8111-111111111111", f.calls*3+i))
	}
	return &plugin.DBQueryResult{Rows: [][]any{values}}, nil
}

func TestEntropyReaderFillsAcrossBlocksAndUsesFreshQueries(t *testing.T) {
	fixture := &entropyFixture{}
	reader := &databaseEntropyReader{db: fixture}
	first := make([]byte, 65)
	second := make([]byte, 65)
	if n, err := io.ReadFull(reader, first); err != nil || n != len(first) {
		t.Fatalf("first read: %d %v", n, err)
	}
	if _, err := io.ReadFull(reader, second); err != nil {
		t.Fatal(err)
	}
	if fixture.calls != 6 || bytes.Equal(first, second) || bytes.Equal(first[:32], first[32:64]) {
		t.Fatal("entropy block was reused")
	}
}

func TestEntropyReaderFailsClosedAndEmptyReadDoesNotQuery(t *testing.T) {
	for _, fixture := range []*entropyFixture{{fail: true}, {malformed: true}} {
		reader := &databaseEntropyReader{db: fixture}
		if n, err := reader.Read(make([]byte, 32)); n != 0 || err == nil {
			t.Fatal("unavailable entropy accepted")
		}
	}
	fixture := &entropyFixture{}
	if n, err := (&databaseEntropyReader{db: fixture}).Read(nil); n != 0 || err != nil || fixture.calls != 0 {
		t.Fatal("empty read queried entropy")
	}
}
