package termux

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestNameservers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resolv.conf")
	data := "# Termux DNS\nnameserver 8.8.8.8\nnameserver invalid\nnameserver 2001:4860:4860::8888\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := nameservers(path)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"8.8.8.8:53", "[2001:4860:4860::8888]:53"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("nameservers = %v, want %v", got, want)
	}
	if err := os.WriteFile(path, []byte("nameserver invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := nameservers(path); err == nil || !strings.Contains(err.Error(), "no valid nameserver") {
		t.Fatalf("invalid configuration error = %v", err)
	}
}
