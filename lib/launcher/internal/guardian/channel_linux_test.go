package guardian

import (
	"os"
	"testing"
)

func TestEntropyRequiresKernelDevice(t *testing.T) {
	regular, err := os.CreateTemp(t.TempDir(), "predictable")
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	if err := validateEntropyDevice(regular); err == nil {
		t.Fatal("regular file accepted as challenge entropy")
	}
	for _, path := range []string{"/dev/zero", "/dev/urandom"} {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		err = validateEntropyDevice(file)
		_ = file.Close()
		if (err == nil) != (path == "/dev/urandom") {
			t.Fatalf("entropy device %s: %v", path, err)
		}
	}
}
