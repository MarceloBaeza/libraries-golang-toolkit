package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/minio/minio-go/v7"
)

func TestNewRequiresSettings(t *testing.T) {
	_, err := New(context.Background(), Settings{})
	if err == nil {
		t.Fatal("expected an error with empty settings")
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Errorf("error = %v, want it to wrap ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), "Endpoint") {
		t.Errorf("error = %q, want it to name the missing fields", err)
	}
}

func TestNewFailsAgainstUnreachableEndpoint(t *testing.T) {
	_, err := New(context.Background(), Settings{
		Endpoint:     "127.0.0.1:1",
		AccessKey:    "key",
		SecretKey:    "secret",
		Bucket:       "attachments",
		InsecureHTTP: true,
	})
	if err == nil {
		t.Fatal("expected a connection error against a closed port")
	}
	if strings.Contains(err.Error(), "missing settings") {
		t.Errorf("error = %q, want a connection failure, not a validation failure", err)
	}
}

func TestObjectKeyUsesFolderPathAsPrefix(t *testing.T) {
	key := objectKey([]string{"2026-07", "Fondo General", "Diezmos"}, "Boleta luz julio.pdf")

	if !strings.HasPrefix(key, "2026-07/fondo-general/diezmos/") {
		t.Errorf("key = %q, want the folder prefix", key)
	}
	if !strings.HasSuffix(key, "-boleta-luz-julio.pdf") {
		t.Errorf("key = %q, want the readable name preserved", key)
	}
}

// Two uploads in the same folder can share a name. Overwriting a key
// silently replaces the previous object, so the generated keys must differ.
func TestObjectKeyAvoidsCollisionsForSameName(t *testing.T) {
	path := []string{"2026-07", "general"}
	first := objectKey(path, "boleta.pdf")
	second := objectKey(path, "boleta.pdf")

	if first == second {
		t.Fatalf("two uploads with the same name produced the same key: %q", first)
	}
}

func TestObjectKeyAlwaysHasPrefix(t *testing.T) {
	for _, path := range [][]string{nil, {}, {""}, {"   "}} {
		key := objectKey(path, "boleta.pdf")
		if !strings.Contains(key, "/") {
			t.Errorf("objectKey(%q, ...) = %q, want at least one prefix segment", path, key)
		}
	}
}

// A name made only of symbols slugifies to nothing; the key must still end
// with a readable segment instead of a bare trailing dash.
func TestObjectKeyFallsBackWhenNameSlugifiesToNothing(t *testing.T) {
	key := objectKey([]string{"2026-07"}, "***")

	if strings.HasSuffix(key, "-") {
		t.Errorf("key = %q, want a fallback name instead of a trailing dash", key)
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Diezmos y Ofrendas":    "diezmos-y-ofrendas",
		"Reparación baño":       "reparacion-bano",
		"Camión   de   mudanza": "camion-de-mudanza",
		"Boleta N° 1234":        "boleta-n-1234",
		"comprobante.PDF":       "comprobante.pdf",
		"  espacios al borde  ": "espacios-al-borde",
		"café à la carte":       "cafe-a-la-carte",
		// Slashes are neutralized so a name can't invent a folder.
		"carpeta/../otra": "carpeta-..-otra",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsMissingObject(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"missing object", minio.ErrorResponse{Code: "NoSuchKey"}, true},
		{"missing bucket", minio.ErrorResponse{Code: "NoSuchBucket"}, true},
		{"invalid credentials", minio.ErrorResponse{Code: "AccessDenied"}, false},
		{"network error", errors.New("connection refused"), false},
		{"no error", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMissingObject(tt.err); got != tt.want {
				t.Errorf("isMissingObject(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// The error Download returns must be recognizable with errors.Is from the
// caller, which is what decides whether to re-upload or retry.
func TestErrObjectMissingWraps(t *testing.T) {
	err := fmt.Errorf("%w: 2026-07/fondo/archivo.jpeg", ErrObjectMissing)
	if !errors.Is(err, ErrObjectMissing) {
		t.Error("the wrapped error should be recognizable with errors.Is")
	}
}
