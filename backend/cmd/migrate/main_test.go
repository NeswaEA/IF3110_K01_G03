package main

import (
	"context"
	"testing"
)

func TestInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"down"}, {"up", "secret"}} {
		if err := run(context.Background(), args); err == nil || err.Error() != "usage: migrate up" {
			t.Fatal("expected safe usage error")
		}
	}
}

func TestMissingMetadataDSN(t *testing.T) {
	t.Setenv("METADATA_DATABASE_URL", "")
	if err := run(context.Background(), []string{"up"}); err == nil || err.Error() != "METADATA_DATABASE_URL is required" {
		t.Fatal("expected required DSN error")
	}
}
