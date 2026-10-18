package whatsapp

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/types"
)

func TestSessionSnapshotAndInstall(t *testing.T) {
	ctx := context.Background()
	source := filepath.Join(t.TempDir(), "source.db")
	client, err := Open(ctx, source, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	id := types.NewJID("123456789", types.DefaultUserServer)
	client.wa.Store.ID = &id
	client.wa.Store.Account = &waAdv.ADVSignedDeviceIdentity{
		Details: []byte{1}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64),
	}
	if err := client.wa.Store.Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := ExportSession(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot) == 0 {
		t.Fatal("empty snapshot")
	}
	target := filepath.Join(t.TempDir(), "nested", "whatsapp.db")
	if err := InstallSession(ctx, target, snapshot); err != nil {
		t.Fatal(err)
	}
	linked, err := IsLinked(ctx, target)
	if err != nil || !linked {
		t.Fatalf("installed session linked=%t err=%v", linked, err)
	}
	if err := InstallSession(ctx, target, snapshot); err == nil {
		t.Fatal("existing session was replaced")
	}
	if err := RemoveInstalledSession(target); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("session remains after removal: %v", err)
	}
}
